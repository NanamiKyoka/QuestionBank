package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func init() {
	// 让重试用例不需要真等退避时间。
	backoffBase = time.Millisecond
}

// sseServer 起一个假的 OpenAI 兼容端点。
//
// handler 收到请求体原文，返回要写入的 SSE 帧（可为空表示非流式响应）。
func sseServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, body []byte)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		handler(w, r, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func writeSSE(w http.ResponseWriter, frames ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	for _, f := range frames {
		fmt.Fprint(w, f)
		if flusher != nil {
			flusher.Flush()
		}
	}
}

func newProvider(t *testing.T, srv *httptest.Server, maxRetries int) *OpenAIProvider {
	t.Helper()
	return NewOpenAI(Config{
		Name:          "test",
		InterfaceType: TypeOpenAI,
		BaseURL:       srv.URL + "/v1",
		APIKey:        "test-key",
		Timeout:       5 * time.Second,
		MaxRetries:    maxRetries,
	})
}

// ---- 流式对话 ----

func TestChatStreamAccumulatesText(t *testing.T) {
	srv := sseServer(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		writeSSE(w,
			"data: {\"choices\":[{\"delta\":{\"content\":\"你好\"}}]}\n\n",
			"data: {\"choices\":[{\"delta\":{\"content\":\"，世界\"}}]}\n\n",
			"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n",
			"data: [DONE]\n\n",
		)
	})

	var got strings.Builder
	var finish string
	err := newProvider(t, srv, 0).ChatStream(context.Background(), ChatRequest{
		Model:    "test-model",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, func(c Chunk) error {
		got.WriteString(c.Text)
		if c.FinishReason != "" {
			finish = c.FinishReason
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ChatStream 失败: %v", err)
	}
	if got.String() != "你好，世界" {
		t.Fatalf("文本增量拼接错误: %q", got.String())
	}
	if finish != "stop" {
		t.Fatalf("应传递结束原因，实际 %q", finish)
	}
}

// 工具调用的参数是分片到达的，必须按 index 聚合完整后再发出。
func TestChatStreamAggregatesToolCallFragments(t *testing.T) {
	srv := sseServer(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		writeSSE(w,
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"search_questions","arguments":""}}]}}]}`+"\n\n",
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"keyword\":"}}]}}]}`+"\n\n",
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"函数\"}"}}]}}]}`+"\n\n",
			`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n",
			"data: [DONE]\n\n",
		)
	})

	var calls []ToolCall
	err := newProvider(t, srv, 0).ChatStream(context.Background(), ChatRequest{
		Model:    "m",
		Messages: []Message{{Role: RoleUser, Content: "找题"}},
	}, func(c Chunk) error {
		if c.ToolCall != nil {
			calls = append(calls, *c.ToolCall)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ChatStream 失败: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("应聚合出 1 个工具调用，实际 %d", len(calls))
	}
	if calls[0].ID != "call_1" || calls[0].Name != "search_questions" {
		t.Fatalf("工具调用标识错误: %+v", calls[0])
	}
	if calls[0].Arguments != `{"keyword":"函数"}` {
		t.Fatalf("参数未正确拼接: %q", calls[0].Arguments)
	}
}

// 多个工具调用要按 index 分别聚合，不能串在一起。
func TestChatStreamAggregatesMultipleToolCalls(t *testing.T) {
	srv := sseServer(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		writeSSE(w,
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"t1","arguments":"{}"}},{"index":1,"id":"b","function":{"name":"t2","arguments":"{}"}}]}}]}`+"\n\n",
			`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n",
			"data: [DONE]\n\n",
		)
	})

	var names []string
	_ = newProvider(t, srv, 0).ChatStream(context.Background(), ChatRequest{Model: "m"},
		func(c Chunk) error {
			if c.ToolCall != nil {
				names = append(names, c.ToolCall.Name)
			}
			return nil
		})
	if len(names) != 2 || names[0] != "t1" || names[1] != "t2" {
		t.Fatalf("应按 index 顺序输出两个工具调用，实际 %v", names)
	}
}

func TestChatStreamParsesUsage(t *testing.T) {
	srv := sseServer(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		writeSSE(w,
			`data: {"choices":[{"delta":{"content":"x"}}]}`+"\n\n",
			`data: {"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`+"\n\n",
			"data: [DONE]\n\n",
		)
	})

	var usage *Usage
	_ = newProvider(t, srv, 0).ChatStream(context.Background(), ChatRequest{Model: "m"},
		func(c Chunk) error {
			if c.Usage != nil {
				usage = c.Usage
			}
			return nil
		})
	if usage == nil || usage.PromptTokens != 10 || usage.TotalTokens != 15 {
		t.Fatalf("用量解析错误: %+v", usage)
	}
}

// 调用方返回错误时应立即中止，不再继续读取。
func TestChatStreamStopsOnEmitError(t *testing.T) {
	srv := sseServer(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		writeSSE(w,
			"data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n",
			"data: {\"choices\":[{\"delta\":{\"content\":\"b\"}}]}\n\n",
			"data: [DONE]\n\n",
		)
	})

	stop := errors.New("用户中断")
	count := 0
	err := newProvider(t, srv, 0).ChatStream(context.Background(), ChatRequest{Model: "m"},
		func(Chunk) error {
			count++
			return stop
		})
	if !errors.Is(err, stop) {
		t.Fatalf("应返回调用方错误，实际 %v", err)
	}
	if count != 1 {
		t.Fatalf("应在第一帧后停止，实际处理了 %d 帧", count)
	}
}

// 单帧 JSON 损坏不应中断整个流。
func TestChatStreamToleratesMalformedFrame(t *testing.T) {
	srv := sseServer(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		writeSSE(w,
			"data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n",
			"data: {这不是 JSON}\n\n",
			"data: {\"choices\":[{\"delta\":{\"content\":\"继续\"}}]}\n\n",
			"data: [DONE]\n\n",
		)
	})

	var got strings.Builder
	if err := newProvider(t, srv, 0).ChatStream(context.Background(), ChatRequest{Model: "m"},
		func(c Chunk) error { got.WriteString(c.Text); return nil }); err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if got.String() != "ok继续" {
		t.Fatalf("坏帧后的内容应继续输出，实际 %q", got.String())
	}
}

// ---- 重试 ----

func TestChatStreamRetriesOnRateLimit(t *testing.T) {
	var attempts int32
	srv := sseServer(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":{"message":"rate limit exceeded"}}`)
			return
		}
		writeSSE(w, "data: {\"choices\":[{\"delta\":{\"content\":\"成功\"}}]}\n\n", "data: [DONE]\n\n")
	})

	var got strings.Builder
	err := newProvider(t, srv, 2).ChatStream(context.Background(), ChatRequest{Model: "m"},
		func(c Chunk) error { got.WriteString(c.Text); return nil })
	if err != nil {
		t.Fatalf("限流后应重试成功: %v", err)
	}
	if got.String() != "成功" {
		t.Fatalf("重试后内容错误: %q", got.String())
	}
	if atomic.LoadInt32(&attempts) != 2 {
		t.Fatalf("应请求 2 次，实际 %d", attempts)
	}
}

func TestChatStreamRetriesOnServerError(t *testing.T) {
	var attempts int32
	srv := sseServer(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		if atomic.AddInt32(&attempts, 1) <= 2 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		writeSSE(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n", "data: [DONE]\n\n")
	})

	if err := newProvider(t, srv, 3).ChatStream(context.Background(), ChatRequest{Model: "m"},
		func(Chunk) error { return nil }); err != nil {
		t.Fatalf("5xx 应重试成功: %v", err)
	}
	if atomic.LoadInt32(&attempts) != 3 {
		t.Fatalf("应请求 3 次，实际 %d", attempts)
	}
}

// 鉴权失败重试没有意义，应当立即返回。
func TestChatStreamDoesNotRetryOnUnauthorized(t *testing.T) {
	var attempts int32
	srv := sseServer(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"invalid api key"}}`)
	})

	err := newProvider(t, srv, 3).ChatStream(context.Background(), ChatRequest{Model: "m"},
		func(Chunk) error { return nil })
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("应返回鉴权错误，实际 %v", err)
	}
	if atomic.LoadInt32(&attempts) != 1 {
		t.Fatalf("鉴权失败不应重试，实际请求 %d 次", attempts)
	}
}

func TestChatStreamGivesUpAfterMaxRetries(t *testing.T) {
	var attempts int32
	srv := sseServer(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusTooManyRequests)
	})

	err := newProvider(t, srv, 2).ChatStream(context.Background(), ChatRequest{Model: "m"},
		func(Chunk) error { return nil })
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("应返回限流错误，实际 %v", err)
	}
	if atomic.LoadInt32(&attempts) != 3 { // 首次 + 2 次重试
		t.Fatalf("应请求 3 次，实际 %d", attempts)
	}
}

// 响应体里带 error 对象时也要识别为限流。
func TestChatStreamClassifiesErrorInBody(t *testing.T) {
	srv := sseServer(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		writeSSE(w, `data: {"error":{"message":"You exceeded your current quota","type":"insufficient_quota"}}`+"\n\n")
	})

	err := newProvider(t, srv, 1).ChatStream(context.Background(), ChatRequest{Model: "m"},
		func(Chunk) error { return nil })
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("应识别为限流，实际 %v", err)
	}
}

func TestParseRetryAfter(t *testing.T) {
	if d := parseRetryAfter("5"); d != 5*time.Second {
		t.Fatalf("秒数解析错误: %v", d)
	}
	if d := parseRetryAfter(""); d != 0 {
		t.Fatalf("空值应为 0，实际 %v", d)
	}
	if d := parseRetryAfter("-3"); d != 0 {
		t.Fatalf("负数应为 0，实际 %v", d)
	}
	future := time.Now().Add(10 * time.Second).UTC().Format(http.TimeFormat)
	if d := parseRetryAfter(future); d <= 0 || d > 11*time.Second {
		t.Fatalf("HTTP 日期解析错误: %v", d)
	}
}

// ---- 请求体构造 ----

func TestChatStreamSendsAuthAndTools(t *testing.T) {
	var captured map[string]any
	var authHeader string
	srv := sseServer(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		authHeader = r.Header.Get("Authorization")
		_ = json.Unmarshal(body, &captured)
		writeSSE(w, "data: [DONE]\n\n")
	})

	_ = newProvider(t, srv, 0).ChatStream(context.Background(), ChatRequest{
		Model:        "gpt-test",
		SystemPrompt: "你是助手",
		Messages:     []Message{{Role: RoleUser, Content: "hi"}},
		Tools: []ToolSpec{{
			Name:        "search",
			Description: "检索",
			Parameters:  map[string]any{"type": "object"},
		}},
	}, func(Chunk) error { return nil })

	if authHeader != "Bearer test-key" {
		t.Fatalf("应发送 Bearer 鉴权，实际 %q", authHeader)
	}
	if captured["model"] != "gpt-test" {
		t.Fatalf("模型名错误: %v", captured["model"])
	}
	if captured["stream"] != true {
		t.Fatal("应请求流式响应")
	}
	// system 提示词应作为第一条消息插入。
	msgs, _ := captured["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("应有 2 条消息（system + user），实际 %d", len(msgs))
	}
	first, _ := msgs[0].(map[string]any)
	if first["role"] != "system" || first["content"] != "你是助手" {
		t.Fatalf("首条消息应为 system: %v", first)
	}
	if _, ok := captured["stream_options"]; !ok {
		t.Fatal("应请求返回用量")
	}
}

// 多模态消息的 content 应变成 parts 数组。
func TestChatStreamBuildsMultimodalMessage(t *testing.T) {
	var captured map[string]any
	srv := sseServer(t, func(w http.ResponseWriter, _ *http.Request, body []byte) {
		_ = json.Unmarshal(body, &captured)
		writeSSE(w, "data: [DONE]\n\n")
	})

	_ = newProvider(t, srv, 0).ChatStream(context.Background(), ChatRequest{
		Model: "m",
		Messages: []Message{{
			Role:    RoleUser,
			Content: "看图",
			Images:  []string{"data:image/png;base64,AAA"},
		}},
	}, func(Chunk) error { return nil })

	msgs, _ := captured["messages"].([]any)
	parts, ok := msgs[0].(map[string]any)["content"].([]any)
	if !ok {
		t.Fatalf("含图片时 content 应为数组: %v", msgs[0])
	}
	if len(parts) != 2 {
		t.Fatalf("应有文本 + 图片两个 part，实际 %d", len(parts))
	}
}

// 纯工具调用轮的 assistant 消息 content 必须为 null。
func TestToOAIMessageToolCallHasNullContent(t *testing.T) {
	m := toOAIMessage(Message{
		Role:      RoleAssistant,
		ToolCalls: []ToolCall{{ID: "c1", Name: "t", Arguments: "{}"}},
	})
	if m.Content != nil {
		t.Fatalf("纯工具调用轮 content 应为 null，实际 %#v", m.Content)
	}
}

// ---- 结构化抽取 ----

func TestExtractQuestionsParsesVariants(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    int
	}{
		{"裸数组", `[{"stem":"题目一"},{"stem":"题目二"}]`, 2},
		{"包一层 questions", `{"questions":[{"stem":"题目一"}]}`, 1},
		{"包一层 data", `{"data":[{"stem":"题目一"}]}`, 1},
		{"Markdown 围栏", "```json\n[{\"stem\":\"题目一\"}]\n```", 1},
		{"前后有解释", "好的，以下是结果：\n[{\"stem\":\"题目一\"}]\n希望对你有帮助。", 1},
		{"带语言标记的围栏", "```JSON\n{\"questions\":[{\"stem\":\"x\"}]}\n```", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseExtractedQuestions(tc.content)
			if err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			if len(got) != tc.want {
				t.Fatalf("应解析出 %d 道题，实际 %d", tc.want, len(got))
			}
		})
	}
}

func TestExtractQuestionsRejectsGarbage(t *testing.T) {
	for _, s := range []string{"", "这不是 JSON", "{}", "```\n```"} {
		if _, err := parseExtractedQuestions(s); err == nil {
			t.Fatalf("%q 应解析失败", s)
		}
	}
}

func TestExtractQuestionsSendsJSONMode(t *testing.T) {
	var captured map[string]any
	srv := sseServer(t, func(w http.ResponseWriter, _ *http.Request, body []byte) {
		_ = json.Unmarshal(body, &captured)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"[{\"stem\":\"题目\"}]"}}]}`)
	})

	got, err := newProvider(t, srv, 0).ExtractQuestions(context.Background(), ExtractRequest{
		Model:   "m",
		Prompt:  "抽取题目",
		Content: "1. 题目内容",
		Schema:  `{"questions":[]}`,
	})
	if err != nil {
		t.Fatalf("抽取失败: %v", err)
	}
	if len(got) != 1 || got[0].Stem != "题目" {
		t.Fatalf("抽取结果错误: %+v", got)
	}
	if captured["stream"] == true {
		t.Fatal("抽取应为非流式请求")
	}
	rf, ok := captured["response_format"].(map[string]any)
	if !ok || rf["type"] != "json_object" {
		t.Fatalf("应要求 JSON 输出: %v", captured["response_format"])
	}
	// schema 说明应拼进 system 提示词。
	msgs, _ := captured["messages"].([]any)
	sys, _ := msgs[0].(map[string]any)
	if !strings.Contains(fmt.Sprint(sys["content"]), "JSON") {
		t.Fatalf("system 提示词应包含 JSON 约束: %v", sys)
	}
}

func TestExtractQuestionsSurfacesAPIError(t *testing.T) {
	srv := sseServer(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"error":{"message":"invalid api key detected","type":"invalid_request_error"}}`)
	})

	_, err := newProvider(t, srv, 0).ExtractQuestions(context.Background(), ExtractRequest{
		Model: "m", Content: "x",
	})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("应识别为鉴权错误，实际 %v", err)
	}
}

func TestExtractQuestionsEmptyContent(t *testing.T) {
	srv := sseServer(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		fmt.Fprint(w, `{"choices":[]}`)
	})
	if _, err := newProvider(t, srv, 0).ExtractQuestions(context.Background(), ExtractRequest{
		Model: "m",
	}); err == nil {
		t.Fatal("空内容应报错")
	}
}

// ---- 向量化 ----

func TestEmbedSplitsIntoBatches(t *testing.T) {
	var requests int32
	srv := sseServer(t, func(w http.ResponseWriter, _ *http.Request, body []byte) {
		atomic.AddInt32(&requests, 1)
		var req struct {
			Input []string `json:"input"`
		}
		_ = json.Unmarshal(body, &req)

		type item struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}
		items := make([]item, len(req.Input))
		// 故意乱序返回，检查是否按 index 还原。
		for i := len(req.Input) - 1; i >= 0; i-- {
			items[len(req.Input)-1-i] = item{Index: i, Embedding: []float32{float32(i)}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": items})
	})

	texts := make([]string, 25)
	for i := range texts {
		texts[i] = fmt.Sprintf("文本%d", i)
	}

	vecs, err := newProvider(t, srv, 0).Embed(context.Background(), "emb", texts)
	if err != nil {
		t.Fatalf("向量化失败: %v", err)
	}
	if len(vecs) != 25 {
		t.Fatalf("应返回 25 个向量，实际 %d", len(vecs))
	}
	if got := atomic.LoadInt32(&requests); got != 3 {
		t.Fatalf("25 条文本按每批 10 条应发 3 次请求，实际 %d", got)
	}
	// 每批内部应按 index 还原顺序。
	if vecs[0][0] != 0 || vecs[9][0] != 9 {
		t.Fatalf("批内顺序未按 index 还原: %v %v", vecs[0], vecs[9])
	}
}

func TestEmbedSanitizesInput(t *testing.T) {
	var captured struct {
		Input []string `json:"input"`
	}
	srv := sseServer(t, func(w http.ResponseWriter, _ *http.Request, body []byte) {
		_ = json.Unmarshal(body, &captured)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"index":0,"embedding":[1]},{"index":1,"embedding":[2]}]}`)
	})

	_, err := newProvider(t, srv, 0).Embed(context.Background(), "e", []string{"a\nb", "   "})
	if err != nil {
		t.Fatalf("失败: %v", err)
	}
	if captured.Input[0] != "a b" {
		t.Fatalf("换行应被替换为空格，实际 %q", captured.Input[0])
	}
	if strings.TrimSpace(captured.Input[1]) == "" {
		t.Fatal("空白文本不应原样发出（会被上游拒绝）")
	}
}

func TestEmbedEmptyInput(t *testing.T) {
	vecs, err := NewOpenAI(Config{InterfaceType: TypeOpenAI, APIKey: "k"}).
		Embed(context.Background(), "m", nil)
	if err != nil || vecs != nil {
		t.Fatalf("空输入应直接返回，实际 %v %v", vecs, err)
	}
}

// ---- 配置与校验 ----

func TestProviderRejectsMissingAPIKey(t *testing.T) {
	p := NewOpenAI(Config{Name: "x", InterfaceType: TypeOpenAI})
	err := p.ChatStream(context.Background(), ChatRequest{Model: "m"}, func(Chunk) error { return nil })
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("缺少密钥应报鉴权错误，实际 %v", err)
	}
}

func TestProviderRejectsUnknownType(t *testing.T) {
	p := NewOpenAI(Config{Name: "x", InterfaceType: "unknown", APIKey: "k"})
	if err := p.ChatStream(context.Background(), ChatRequest{Model: "m"}, func(Chunk) error { return nil }); err == nil {
		t.Fatal("未知供应商类型应报错")
	}
}

// BaseURL 为空时应回落到官方地址。
func TestEndpointDefaultBaseURL(t *testing.T) {
	p := NewOpenAI(Config{Name: "x", InterfaceType: TypeOpenAI, APIKey: "k"})
	if got := p.endpoint("/chat/completions"); got != openAIBaseURL+"/chat/completions" {
		t.Fatalf("默认地址错误: %s", got)
	}
}

// BaseURL 末尾多余的斜杠不应产生双斜杠。
func TestEndpointTrimsTrailingSlash(t *testing.T) {
	p := NewOpenAI(Config{BaseURL: "https://example.com/v1/"})
	if got := p.endpoint("/chat/completions"); got != "https://example.com/v1/chat/completions" {
		t.Fatalf("斜杠处理错误: %s", got)
	}
}

// 请求超时后应返回 ErrTimeout 而不是原始错误。
func TestChatStreamTimeout(t *testing.T) {
	srv := sseServer(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		// 拖过超时时间。
		time.Sleep(200 * time.Millisecond)
		writeSSE(w, "data: [DONE]\n\n")
	})

	p := NewOpenAI(Config{
		InterfaceType: TypeOpenAI, BaseURL: srv.URL, APIKey: "k", Timeout: time.Millisecond,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	err := p.ChatStream(ctx, ChatRequest{Model: "m"}, func(Chunk) error { return nil })
	if !errors.Is(err, ErrTimeout) && !errors.Is(err, ErrCanceled) {
		t.Fatalf("超时应返回 ErrTimeout/ErrCanceled，实际 %v", err)
	}
}
