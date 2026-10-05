package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// OpenAIProvider 实现 OpenAI 兼容协议。
//
// 之所以只实现这一套就能覆盖绝大多数厂商：DeepSeek、通义千问、智谱、
// 月之暗面、硅基流动等都提供 OpenAI 兼容端点，差异只在 base_url 与模型名。
type OpenAIProvider struct {
	cfg    Config
	client *http.Client
}

// 默认官方地址。
const openAIBaseURL = "https://api.openai.com/v1"

// NewOpenAI 构造。
func NewOpenAI(cfg Config) *OpenAIProvider {
	cfg = cfg.withDefaults()
	return &OpenAIProvider{
		cfg: cfg,
		// 不设 Timeout：超时由每个请求的 context 控制，
		// 这样才能对流式响应做"整体超时"而不是"单次读取超时"。
		client: &http.Client{
			// 拨号前校验目标网段，重定向前重新校验（见 ssrf.go）。
			Transport:     sharedTransport,
			CheckRedirect: checkRedirect,
		},
	}
}

func (p *OpenAIProvider) Name() string { return p.cfg.Name }

func (p *OpenAIProvider) endpoint(path string) string {
	base := strings.TrimSpace(p.cfg.BaseURL)
	if base == "" {
		base = openAIBaseURL
	}
	return strings.TrimRight(base, "/") + path
}

// ---- 对话（流式）----

func (p *OpenAIProvider) ChatStream(ctx context.Context, req ChatRequest, emit func(Chunk) error) error {
	if err := p.cfg.validate(); err != nil {
		return err
	}

	body, err := p.buildChatBody(req, true)
	if err != nil {
		return err
	}

	resp, err := p.doWithRetry(ctx, p.endpoint("/chat/completions"), body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return p.readStream(ctx, resp.Body, emit)
}

// buildChatBody 组装请求体。
func (p *OpenAIProvider) buildChatBody(req ChatRequest, stream bool) ([]byte, error) {
	messages := make([]oaiMessage, 0, len(req.Messages)+1)
	if req.SystemPrompt != "" {
		messages = append(messages, oaiMessage{Role: "system", Content: req.SystemPrompt})
	}
	for _, m := range req.Messages {
		messages = append(messages, toOAIMessage(m))
	}

	payload := oaiChatRequest{
		Model:       req.Model,
		Messages:    messages,
		Stream:      stream,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
	}
	if req.JSONMode {
		payload.ResponseFormat = &oaiResponseFormat{Type: "json_object"}
	}
	for _, t := range req.Tools {
		payload.Tools = append(payload.Tools, oaiTool{
			Type: "function",
			Function: oaiFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
			},
		})
	}
	// 部分厂商只在显式请求时返回用量；不支持的厂商会忽略该字段。
	if stream {
		payload.StreamOptions = &oaiStreamOptions{IncludeUsage: true}
	}

	return json.Marshal(payload)
}

func toOAIMessage(m Message) oaiMessage {
	out := oaiMessage{
		Role:       string(m.Role),
		ToolCallID: m.ToolCallID,
		Name:       m.Name,
	}
	if len(m.ToolCalls) > 0 {
		for _, tc := range m.ToolCalls {
			out.ToolCalls = append(out.ToolCalls, oaiToolCall{
				ID:   tc.ID,
				Type: "function",
				Function: oaiFunctionCall{
					Name:      tc.Name,
					Arguments: tc.Arguments,
				},
			})
		}
	}
	// 多模态：content 变成 parts 数组。
	if len(m.Images) > 0 {
		parts := make([]oaiContentPart, 0, len(m.Images)+1)
		if m.Content != "" {
			parts = append(parts, oaiContentPart{Type: "text", Text: m.Content})
		}
		for _, img := range m.Images {
			parts = append(parts, oaiContentPart{
				Type:     "image_url",
				ImageURL: &oaiImageURL{URL: img},
			})
		}
		out.Content = parts
		return out
	}
	// 纯工具调用轮的 assistant 消息 content 必须为 null 而不是空字符串。
	if m.Content == "" && len(out.ToolCalls) > 0 {
		out.Content = nil
	} else {
		out.Content = m.Content
	}
	return out
}

// readStream 读取 SSE 流并转换为 Chunk。
func (p *OpenAIProvider) readStream(ctx context.Context, body io.Reader, emit func(Chunk) error) error {
	scanner := bufio.NewScanner(body)
	// SSE 单行可能很长（工具参数是完整 JSON），默认 64KB 不够。
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	// 工具调用的参数是分片到达的，按 index 聚合到全部收齐再发出。
	pending := map[int]*ToolCall{}
	var order []int

	flushTools := func() error {
		for _, idx := range order {
			tc := pending[idx]
			if tc == nil || tc.Name == "" {
				continue
			}
			if err := emit(Chunk{ToolCall: tc}); err != nil {
				return err
			}
		}
		pending = map[int]*ToolCall{}
		order = nil
		return nil
	}

	for scanner.Scan() {
		// 调用方取消时及时退出，避免继续读完整个响应。
		if err := ctx.Err(); err != nil {
			return mapContextErr(err)
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		// 兼容部分厂商发送的 "event: xxx" 行。
		if strings.HasPrefix(line, "event:") {
			continue
		}
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			return flushTools()
		}

		var chunk oaiStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			// 单帧解析失败不应中断整个流：厂商偶尔会插入非标准帧。
			continue
		}
		if chunk.Error != nil {
			return classifyAPIError(chunk.Error.Message, chunk.Error.Type)
		}

		if chunk.Usage != nil {
			if err := emit(Chunk{Usage: &Usage{
				PromptTokens:     chunk.Usage.PromptTokens,
				CompletionTokens: chunk.Usage.CompletionTokens,
				TotalTokens:      chunk.Usage.TotalTokens,
			}}); err != nil {
				return err
			}
		}

		for _, choice := range chunk.Choices {
			// reasoning_content 是部分厂商的思维链字段，不并入正文。
			if choice.Delta.Content != "" {
				if err := emit(Chunk{Text: choice.Delta.Content}); err != nil {
					return err
				}
			}
			for _, tc := range choice.Delta.ToolCalls {
				cur, ok := pending[tc.Index]
				if !ok {
					cur = &ToolCall{}
					pending[tc.Index] = cur
					order = append(order, tc.Index)
				}
				if tc.ID != "" {
					cur.ID = tc.ID
				}
				if tc.Function.Name != "" {
					cur.Name = tc.Function.Name
				}
				cur.Arguments += tc.Function.Arguments
			}
			if choice.FinishReason != "" {
				if err := flushTools(); err != nil {
					return err
				}
				if err := emit(Chunk{FinishReason: choice.FinishReason}); err != nil {
					return err
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		if ctx.Err() != nil {
			return mapContextErr(ctx.Err())
		}
		return fmt.Errorf("ai: 读取响应流失败: %w", err)
	}
	return flushTools()
}

// ---- 结构化抽取 ----

func (p *OpenAIProvider) ExtractQuestions(ctx context.Context, req ExtractRequest) ([]ExtractedQuestion, error) {
	if err := p.cfg.validate(); err != nil {
		return nil, err
	}

	system := req.Prompt
	if req.Schema != "" {
		// 明确要求"只输出 JSON"，并给出结构说明。
		// 这是与"把 schema 交给 response_format"等效的兼容做法：
		// 多数兼容端点并不支持完整的 json_schema 约束。
		system += "\n\n严格只输出一个 JSON 对象，不要包含任何解释文字或 Markdown 代码块。" +
			"\n输出结构要求：\n" + req.Schema
	}

	chatReq := ChatRequest{
		Model:        req.Model,
		SystemPrompt: system,
		JSONMode:     true,
		Messages: []Message{{
			Role:    RoleUser,
			Content: req.Content,
			Images:  req.Images,
		}},
		Temperature: floatPtr(0.2),
		MaxTokens:   req.MaxTokens,
	}
	if chatReq.Messages[0].Content == "" && len(req.Images) == 0 {
		return nil, errors.New("ai: 抽取内容为空")
	}

	body, err := p.buildChatBody(chatReq, false)
	if err != nil {
		return nil, err
	}

	resp, err := p.doWithRetry(ctx, p.endpoint("/chat/completions"), body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("ai: 读取响应失败: %w", err)
	}

	var parsed oaiChatResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("ai: 响应不是合法 JSON: %w", err)
	}
	if parsed.Error != nil {
		return nil, classifyAPIError(parsed.Error.Message, parsed.Error.Type)
	}
	if len(parsed.Choices) == 0 {
		return nil, errors.New("ai: 模型没有返回任何内容")
	}

	// 撞到输出上限时**必须**报出来，而不是把截断的结果当成功返回。
	//
	// 抽取动辄要上万 token，而不少兼容端点默认只给 4096。超出的部分
	// 被静默丢掉时，解析多半直接失败（JSON 残缺），于是用户看到的是
	// 一句"响应不是合法 JSON"——指向一个根本不是原因的原因。
	// 偶尔也会碰巧解析成功，那更糟：后半份卷子凭空消失，用户毫无察觉。
	if reason := parsed.Choices[0].FinishReason; reason == "length" {
		limit := "服务端默认"
		if req.MaxTokens > 0 {
			limit = strconv.Itoa(req.MaxTokens)
		}
		return nil, fmt.Errorf(
			"%w：输出被上限 %s token 截断，结果不完整；请在 AI 设置里调大该模型的「最大输出」，或减少一次导入的内容",
			ErrTruncated, limit)
	}

	return parseExtractedQuestions(parsed.Choices[0].Message.Content)
}

// parseExtractedQuestions 解析模型返回的题目 JSON。
//
// 容错是刻意的：模型经常包一层 Markdown 代码块，或者返回 {"questions": [...]}
// 而不是裸数组。这些都要能识别，否则一次抽取就白跑了。
func parseExtractedQuestions(content string) ([]ExtractedQuestion, error) {
	clean := cleanJSONResponse(content)
	if clean == "" {
		return nil, errors.New("ai: 模型返回内容为空")
	}

	// 形态一：裸数组
	var list []ExtractedQuestion
	if err := json.Unmarshal([]byte(clean), &list); err == nil {
		return list, nil
	}

	// 形态二：包了一层对象
	var wrapper struct {
		Questions []ExtractedQuestion `json:"questions"`
		Data      []ExtractedQuestion `json:"data"`
		Items     []ExtractedQuestion `json:"items"`
		Extracted []ExtractedQuestion `json:"extracted_questions"`
	}
	if err := json.Unmarshal([]byte(clean), &wrapper); err == nil {
		for _, cand := range [][]ExtractedQuestion{
			wrapper.Questions, wrapper.Data, wrapper.Items, wrapper.Extracted,
		} {
			if len(cand) > 0 {
				return cand, nil
			}
		}
	}

	return nil, fmt.Errorf("ai: 无法解析模型返回的题目结构: %s", truncate(clean, 200))
}

// cleanJSONResponse 去掉模型可能包裹的 Markdown 代码块与前后说明文字。
func cleanJSONResponse(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}

	// 去掉 ```json ... ``` 围栏。
	if i := strings.Index(s, "```"); i >= 0 {
		rest := s[i+3:]
		// 跳过语言标记那一行
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			rest = rest[nl+1:]
		}
		if j := strings.Index(rest, "```"); j >= 0 {
			rest = rest[:j]
		}
		s = strings.TrimSpace(rest)
	}

	// 截取第一个 { 或 [ 到最后一个 } 或 ] 之间的内容，
	// 以容忍模型在 JSON 前后多写了一句解释。
	start := strings.IndexAny(s, "{[")
	if start < 0 {
		return ""
	}
	end := strings.LastIndexAny(s, "}]")
	if end < start {
		return ""
	}
	return strings.TrimSpace(s[start : end+1])
}

// ---- 向量化 ----

func (p *OpenAIProvider) Embed(ctx context.Context, model string, texts []string) ([][]float32, error) {
	if err := p.cfg.validate(); err != nil {
		return nil, err
	}
	if len(texts) == 0 {
		return nil, nil
	}

	// 批量上限：多数兼容端点单次最多 20 条左右，取 10 更保险。
	const batchSize = 10
	out := make([][]float32, 0, len(texts))

	for start := 0; start < len(texts); start += batchSize {
		end := start + batchSize
		if end > len(texts) {
			end = len(texts)
		}

		payload := oaiEmbeddingRequest{
			Model: model,
			Input: sanitizeEmbeddingInput(texts[start:end]),
		}
		body, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}

		resp, err := p.doWithRetry(ctx, p.endpoint("/embeddings"), body)
		if err != nil {
			return nil, err
		}

		raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("ai: 读取向量响应失败: %w", err)
		}

		var parsed oaiEmbeddingResponse
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return nil, fmt.Errorf("ai: 向量响应不是合法 JSON: %w", err)
		}
		if parsed.Error != nil {
			return nil, classifyAPIError(parsed.Error.Message, parsed.Error.Type)
		}
		// 按 index 排序，保证与输入顺序一致。
		vectors := make([][]float32, len(payload.Input))
		for _, d := range parsed.Data {
			if d.Index >= 0 && d.Index < len(vectors) {
				vectors[d.Index] = d.Embedding
			}
		}
		for i, v := range vectors {
			if v == nil {
				return nil, fmt.Errorf("ai: 第 %d 条文本没有返回向量", start+i)
			}
			out = append(out, v)
		}
	}

	return out, nil
}

// sanitizeEmbeddingInput 清理待向量化的文本。
//
// 换行会被某些厂商当作多段输入处理，导致返回条数与请求不符；
// 空串则会被直接拒绝。
func sanitizeEmbeddingInput(texts []string) []string {
	out := make([]string, len(texts))
	for i, t := range texts {
		t = strings.ReplaceAll(t, "\n", " ")
		t = strings.TrimSpace(t)
		if t == "" {
			t = "空"
		}
		out[i] = t
	}
	return out
}

// ---- HTTP 与错误处理 ----

// doWithRetry 发起请求并在可重试的错误上退避重试。
//
// 注意：只在**尚未开始流式读取**时重试。一旦响应体开始下发，
// 重试会导致调用方收到重复内容，因此 readStream 里的错误一律不再重试。
func (p *OpenAIProvider) doWithRetry(ctx context.Context, url string, body []byte) (*http.Response, error) {
	cfg := p.cfg.withDefaults()
	var lastErr error

	for attempt := 0; attempt <= cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			delay := backoff(attempt, lastErr)
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, mapContextErr(ctx.Err())
			case <-timer.C:
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("ai: 构造请求失败: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
		req.Header.Set("Accept", "text/event-stream")

		resp, err := p.client.Do(req)
		if err != nil {
			lastErr = mapContextErr(err)
			if !isRetryable(lastErr) {
				return nil, lastErr
			}
			continue
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return resp, nil
		}

		// 读取错误体后关闭，否则连接无法复用。
		msg := readErrorBody(resp.Body)
		retryAfter := resp.Header.Get("Retry-After")
		resp.Body.Close()
		lastErr = classifyHTTPStatus(resp.StatusCode, msg, retryAfter)

		if !isRetryable(lastErr) {
			return nil, lastErr
		}
	}

	return nil, lastErr
}

// backoffBase 退避基数。声明为变量是为了让测试能把它调小，
// 否则每个重试用例都要真等几百毫秒。
var backoffBase = 500 * time.Millisecond

// backoff 计算退避时长：指数增长 + 抖动，避免大量请求同时重试打爆上游。
func backoff(attempt int, lastErr error) time.Duration {
	// 上游明确给了 Retry-After 就听它的。
	var rl *rateLimitError
	if errors.As(lastErr, &rl) && rl.RetryAfter > 0 {
		if rl.RetryAfter > 30*time.Second {
			return 30 * time.Second
		}
		return rl.RetryAfter
	}

	base := time.Duration(1<<uint(attempt-1)) * backoffBase
	if base > 8*time.Second {
		base = 8 * time.Second
	}
	// 抖动 ±25%：让并发请求的退避点错开。
	jitter := time.Duration(rand.Int63n(int64(base/2) + 1))
	return base - base/4 + jitter
}

// isRetryable 判断错误是否值得重试。
func isRetryable(err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, ErrRateLimited),
		errors.Is(err, ErrUnavailable),
		errors.Is(err, ErrTimeout):
		return true
	default:
		return false
	}
}

// rateLimitError 带 Retry-After 的限流错误。
type rateLimitError struct {
	Message    string
	RetryAfter time.Duration
}

func (e *rateLimitError) Error() string { return e.Message }
func (e *rateLimitError) Unwrap() error { return ErrRateLimited }

// classifyHTTPStatus 把上游状态码映射为领域错误。
//
// 上游响应体只进服务端日志，绝不进错误串：错误串会经 humanizeAIError
// 原样返回给调用方（连通性测试还包着一层 HTTP 200），等于把上游返回的
// 任意内容——内网地址、堆栈、配置片段——回显到浏览器里。
func classifyHTTPStatus(status int, body, retryAfter string) error {
	detail := strings.TrimSpace(body)
	if len(detail) > 500 {
		detail = detail[:500]
	}
	attrs := []any{"http_status", status, "upstream_body", detail}
	if status >= 500 {
		slog.Warn("AI 上游返回错误", attrs...)
	} else {
		slog.Debug("AI 上游返回错误", attrs...)
	}

	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%w: HTTP %d", ErrUnauthorized, status)
	case http.StatusNotFound:
		return fmt.Errorf("%w: HTTP 404", ErrModelNotFound)
	case http.StatusTooManyRequests:
		return &rateLimitError{
			Message:    "请求被限流: HTTP 429",
			RetryAfter: parseRetryAfter(retryAfter),
		}
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return fmt.Errorf("%w: HTTP %d", ErrTimeout, status)
	default:
		if status >= 500 {
			return fmt.Errorf("%w: HTTP %d", ErrUnavailable, status)
		}
		return fmt.Errorf("%w: HTTP %d", ErrBadRequest, status)
	}
}

// parseRetryAfter 解析 Retry-After 头，支持秒数与 HTTP 日期两种格式。
func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		d := time.Until(t)
		if d < 0 {
			return 0
		}
		return d
	}
	return 0
}

// classifyAPIError 把响应体里的 error 对象映射为领域错误。
//
// 与 classifyHTTPStatus 同样的纪律：上游给的 message 只用来做分类与写日志，
// 不写进错误串——它最终会回到浏览器里。
func classifyAPIError(message, typ string) error {
	lower := strings.ToLower(message + " " + typ)
	switch {
	case strings.Contains(lower, "rate limit"), strings.Contains(lower, "quota"),
		strings.Contains(lower, "too many"):
		return &rateLimitError{Message: "请求被限流"}
	case strings.Contains(lower, "api key"), strings.Contains(lower, "unauthorized"),
		strings.Contains(lower, "authentication"):
		return fmt.Errorf("%w: 上游拒绝了凭证", ErrUnauthorized)
	case strings.Contains(lower, "model"), strings.Contains(lower, "not found"):
		return fmt.Errorf("%w: 上游查无此模型", ErrModelNotFound)
	case strings.Contains(lower, "timeout"):
		return fmt.Errorf("%w: 上游处理超时", ErrTimeout)
	}
	slog.Debug("AI 上游返回未识别的错误", "error_message", message, "error_type", typ)
	return fmt.Errorf("%w: 上游返回了未识别的错误", ErrBadRequest)
}

// mapContextErr 把 context 错误映射为领域错误。
func mapContextErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled):
		return fmt.Errorf("%w: %v", ErrCanceled, err)
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("%w: %v", ErrTimeout, err)
	default:
		return err
	}
}

func readErrorBody(r io.Reader) string {
	b, err := io.ReadAll(io.LimitReader(r, 8<<10))
	if err != nil {
		return ""
	}
	return string(b)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}

func floatPtr(v float64) *float64 { return &v }

// ---- 线上结构 ----

type oaiChatRequest struct {
	Model          string             `json:"model"`
	Messages       []oaiMessage       `json:"messages"`
	Stream         bool               `json:"stream"`
	StreamOptions  *oaiStreamOptions  `json:"stream_options,omitempty"`
	Temperature    *float64           `json:"temperature,omitempty"`
	MaxTokens      int                `json:"max_tokens,omitempty"`
	Tools          []oaiTool          `json:"tools,omitempty"`
	ResponseFormat *oaiResponseFormat `json:"response_format,omitempty"`
}

type oaiStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type oaiResponseFormat struct {
	Type string `json:"type"`
}

type oaiMessage struct {
	Role       string        `json:"role"`
	Content    any           `json:"content"`
	ToolCalls  []oaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
	Name       string        `json:"name,omitempty"`
}

type oaiContentPart struct {
	Type     string       `json:"type"`
	Text     string       `json:"text,omitempty"`
	ImageURL *oaiImageURL `json:"image_url,omitempty"`
}

type oaiImageURL struct {
	URL string `json:"url"`
}

type oaiTool struct {
	Type     string      `json:"type"`
	Function oaiFunction `json:"function"`
}

type oaiFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type oaiToolCall struct {
	ID       string          `json:"id"`
	Type     string          `json:"type"`
	Function oaiFunctionCall `json:"function"`
}

type oaiFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type oaiStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	Error *oaiError `json:"error"`
}

type oaiError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    any    `json:"code"`
}

type oaiChatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error *oaiError `json:"error"`
}

type oaiEmbeddingRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type oaiEmbeddingResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Error *oaiError `json:"error"`
}
