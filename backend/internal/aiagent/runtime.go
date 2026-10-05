package aiagent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/NanamiKyoka/questionmind/internal/ai"
	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
)

// RunStore 运行轨迹的持久化接口。
//
// 定义成接口而不是直接依赖 repository：运行时不应该知道
// agent_runs / agent_steps 表长什么样。传 nil 表示不落库。
type RunStore interface {
	// StartRun 记录一次运行的开始。
	StartRun(ctx context.Context, run *RunRecord) error
	// AddStep 记录一步（模型输出或工具调用）。
	AddStep(ctx context.Context, step *StepRecord) error
	// FinishRun 记录运行结束。
	FinishRun(ctx context.Context, runID string, status model.AgentRunStatus, stopReason string, errMsg string, usage ai.Usage) error
}

// RunRecord 一次运行的元信息。
type RunRecord struct {
	ID           string
	SessionID    *uint
	UserID       uint
	SubjectID    *uint
	Surface      string
	ModelID      *uint
	ModelName    string
	ProviderName string
}

// StepRecord 运行中的一步。
type StepRecord struct {
	RunID     string
	Idx       int
	StepType  string
	ToolName  string
	Args      any
	Result    any
	LatencyMs int
}

// Runtime Agent 运行时。
type Runtime struct {
	provider ai.Provider
	registry *Registry
	channel  *Channel
	budget   Budget
	store    RunStore

	// modelID / providerName 仅用于落库时的运行快照。
	modelID      *uint
	providerName string
}

// NewRuntime 构造。
func NewRuntime(provider ai.Provider, registry *Registry, channel *Channel, budget Budget) *Runtime {
	return &Runtime{
		provider: provider,
		registry: registry,
		channel:  channel,
		budget:   budget.Normalize(),
	}
}

// WithStore 设置运行轨迹存储。
func (r *Runtime) WithStore(store RunStore, modelID *uint, providerName string) *Runtime {
	r.store = store
	r.modelID = modelID
	r.providerName = providerName
	return r
}

// Budget 返回生效的预算。
func (r *Runtime) Budget() Budget { return r.budget }

// RunRequest 一次运行请求。
type RunRequest struct {
	// RunID 由调用方生成，运行期间就要用于 SSE 事件与前端票据。
	RunID string
	// SessionID 关联的会话（可为 nil）。
	SessionID *uint

	Model        string
	SystemPrompt string
	Messages     []ai.Message
	Scene        Scene

	// Call 提供用户与学科作用域，工具实现据此鉴权。
	Call ToolCall

	MaxTokens   int
	Temperature *float64
}

// emitError 包装"事件发送失败"。
//
// 单独一个类型是为了把两种情况区分开：
//   - 模型调用失败 → 应该发 RunFailed 告知前端；
//   - 事件本身发不出去（客户端已断开）→ 再发任何帧都没有意义，
//     只会白白多做一次写入尝试。
//
// Unwrap 保留原始错误，调用方仍可用 errors.Is 判断具体原因。
type emitError struct{ cause error }

func (e *emitError) Error() string { return "aiagent: 事件发送失败: " + e.cause.Error() }
func (e *emitError) Unwrap() error { return e.cause }

func asEmitError(err error) (*emitError, bool) {
	var e *emitError
	ok := errors.As(err, &e)
	return e, ok
}

// Run 执行一次 Agent 运行，通过 emit 输出事件。
//
// 预算三重限制（轮次 / 工具调用数 / 墙钟时间）在每一轮开始时检查。
// 工具失败不中断运行：把错误文本回喂给模型，让它自我纠正——
// 这比直接给用户报错有用得多。
func (r *Runtime) Run(ctx context.Context, req RunRequest, emit func(Event) error) error {
	started := time.Now()

	messages := make([]ai.Message, 0, len(req.Messages)+8)
	messages = append(messages, req.Messages...)

	if r.store != nil {
		rec := &RunRecord{
			ID:           req.RunID,
			SessionID:    req.SessionID,
			UserID:       req.Call.User.ID,
			Surface:      "chat",
			ModelID:      r.modelID,
			ModelName:    req.Model,
			ProviderName: r.providerName,
		}
		if req.Call.SubjectID > 0 {
			sid := req.Call.SubjectID
			rec.SubjectID = &sid
		}
		if err := r.store.StartRun(ctx, rec); err != nil {
			// 落库失败不应阻断对话本身。
			slog.Warn("记录运行开始失败", "run_id", req.RunID, "error", err)
		}
	}

	var (
		// fullText 整轮对话累计的文本，仅用于日志与预算耗尽时的兜底展示。
		fullText string
		// finalText 最后一轮的文本，也就是"最终回答"。
		//
		// 必须与 fullText 分开：中间的带工具调用轮已经通过 AssistantTurn
		// 单独落库了，如果 RunFinished 再带上累计文本，调用方会重复保存一遍。
		finalText     string
		stopReason    = StopCompleted
		toolCallsUsed int
		stepIdx       int
		usage         ai.Usage
		// answered 标记模型给出了最终回答（而不是被预算掐断）。
		// Go 没有 for-else，只能用一个显式标志表达"循环自然结束"。
		answered bool
	)

	finish := func(status model.AgentRunStatus, errMsg string) {
		if r.store == nil {
			return
		}
		if err := r.store.FinishRun(ctx, req.RunID, status, stopReason, errMsg, usage); err != nil {
			slog.Warn("记录运行结束失败", "run_id", req.RunID, "error", err)
		}
	}

	addStep := func(step *StepRecord) {
		if r.store == nil {
			return
		}
		step.RunID = req.RunID
		step.Idx = stepIdx
		stepIdx++
		if err := r.store.AddStep(ctx, step); err != nil {
			slog.Warn("记录运行步骤失败", "run_id", req.RunID, "error", err)
		}
	}

	schemas := r.registry.Schemas(req.Scene, r.channel == nil || r.channel.Enabled())

	for turn := 0; turn < r.budget.MaxTurns; turn++ {
		if err := ctx.Err(); err != nil {
			stopReason = StopCanceled
			finish(model.AgentRunFailed, err.Error())
			_ = emit(&RunFailed{RunID: req.RunID, Error: "对话已取消"})
			return nil
		}
		if time.Since(started) > r.budget.WallClock {
			stopReason = StopTimeLimit
			break
		}

		turnText, toolCalls, turnUsage, err := r.streamTurn(ctx, req, messages, schemas, emit)
		if err != nil {
			// 事件发不出去说明消费端已经没了（客户端断开）。
			// 此时再发 RunFailed 也只是多一次无用的写入尝试，直接返回。
			if _, isEmit := asEmitError(err); isEmit {
				finish(model.AgentRunFailed, "客户端断开")
				return err
			}
			// 取消不算失败：用户主动中断是正常操作。
			if errors.Is(err, ai.ErrCanceled) || ctx.Err() != nil {
				stopReason = StopCanceled
				finish(model.AgentRunFailed, "用户取消")
				_ = emit(&RunFinished{RunID: req.RunID, Text: fullText, StopReason: StopCanceled, Budget: r.budget})
				return nil
			}
			finish(model.AgentRunFailed, err.Error())
			_ = emit(&RunFailed{RunID: req.RunID, Error: HumanizeError(err)})
			return err
		}

		fullText += turnText
		finalText = turnText
		usage.PromptTokens += turnUsage.PromptTokens
		usage.CompletionTokens += turnUsage.CompletionTokens

		// 没有工具调用 → 这一轮就是最终回答。
		if len(toolCalls) == 0 {
			answered = true
			break
		}

		if toolCallsUsed+len(toolCalls) > r.budget.MaxToolCalls {
			stopReason = StopToolLimit
			break
		}

		// 让调用方把这一轮落库（含工具调用记录）。
		records := make([]ToolCallRecord, 0, len(toolCalls))
		for _, tc := range toolCalls {
			records = append(records, ToolCallRecord{ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments})
		}
		if err := emit(&AssistantTurn{RunID: req.RunID, Text: turnText, ToolCalls: records}); err != nil {
			return err
		}
		addStep(&StepRecord{
			StepType: "assistant",
			Result: map[string]any{
				"text":       truncate(turnText, 2000),
				"tool_calls": toolNames(toolCalls),
			},
		})

		messages = append(messages, assistantMessage(turnText, toolCalls))

		for _, tc := range toolCalls {
			toolCallsUsed++

			began := time.Now()
			result := r.dispatch(ctx, req, tc, emit)
			latency := int(time.Since(began).Milliseconds())

			addStep(&StepRecord{
				StepType:  "tool_call",
				ToolName:  tc.Name,
				Args:      parseArgs(tc.Arguments),
				Result:    map[string]any{"content": truncate(result.Content, 2000)},
				LatencyMs: latency,
			})

			if err := emit(&ToolCallFinished{
				ToolCallID: tc.ID,
				Name:       tc.Name,
				Content:    result.Content,
				UI:         result.UI,
			}); err != nil {
				return err
			}

			messages = append(messages, ai.Message{
				Role:       ai.RoleTool,
				Content:    result.Content,
				ToolCallID: tc.ID,
				Name:       tc.Name,
			})
		}
	}

	// 既没给出最终回答、也没被其它预算条件中止，就是用满了轮次。
	if !answered && stopReason == StopCompleted {
		stopReason = StopBudgetExhausted
	}

	finish(model.AgentRunDone, "")
	return emit(&RunFinished{RunID: req.RunID, Text: finalText, StopReason: stopReason, Budget: r.budget})
}

// streamTurn 跑一轮模型调用，收集文本与工具调用。
func (r *Runtime) streamTurn(
	ctx context.Context,
	req RunRequest,
	messages []ai.Message,
	schemas []ai.ToolSpec,
	emit func(Event) error,
) (string, []ai.ToolCall, ai.Usage, error) {
	var (
		text      string
		toolCalls []ai.ToolCall
		usage     ai.Usage
	)

	err := r.provider.ChatStream(ctx, ai.ChatRequest{
		Model:        req.Model,
		SystemPrompt: req.SystemPrompt,
		Messages:     messages,
		Tools:        schemas,
		MaxTokens:    req.MaxTokens,
		Temperature:  req.Temperature,
	}, func(chunk ai.Chunk) error {
		switch {
		case chunk.Text != "":
			text += chunk.Text
			if err := emit(&TextDelta{Text: chunk.Text}); err != nil {
				return &emitError{cause: err}
			}
		case chunk.ToolCall != nil:
			toolCalls = append(toolCalls, *chunk.ToolCall)
		case chunk.Usage != nil:
			usage.PromptTokens += chunk.Usage.PromptTokens
			usage.CompletionTokens += chunk.Usage.CompletionTokens
			if err := emit(&Usage{
				PromptTokens:     chunk.Usage.PromptTokens,
				CompletionTokens: chunk.Usage.CompletionTokens,
			}); err != nil {
				return &emitError{cause: err}
			}
		}
		return nil
	})

	return text, toolCalls, usage, err
}

// dispatch 执行一次工具调用。
func (r *Runtime) dispatch(ctx context.Context, req RunRequest, tc ai.ToolCall, emit func(Event) error) ToolResult {
	tool, ok := r.registry.Get(tc.Name)
	if !ok {
		// 模型可能幻觉出不存在的工具。回喂错误让它改用可用工具。
		return ErrorResult("工具 %s 不存在，请使用已提供的工具", tc.Name)
	}

	args, err := parseArgsStrict(tc.Arguments)
	if err != nil {
		return ErrorResult("参数不是合法的 JSON：%s", truncate(tc.Arguments, 200))
	}

	call := req.Call
	call.Arguments = args
	call.RawArguments = tc.Arguments
	call.ToolCallID = tc.ID

	if tool.Executor == ExecutorClient {
		return r.dispatchClientTool(ctx, req, tool, call, emit)
	}

	if err := emit(&ToolCallStarted{ToolCallID: tc.ID, Name: tc.Name, Arguments: args}); err != nil {
		return ErrorResult("事件发送失败")
	}

	result, err := tool.Handler(ctx, call)
	if err != nil {
		// 领域错误对用户是"没有权限"这类信息，对模型则是明确的纠正信号。
		return ErrorResult("%s", HumanizeError(err))
	}
	return result
}

// dispatchClientTool 把工具调用推给前端并等待回传。
func (r *Runtime) dispatchClientTool(
	ctx context.Context,
	req RunRequest,
	tool *Tool,
	call ToolCall,
	emit func(Event) error,
) ToolResult {
	if r.channel == nil || !r.channel.Enabled() {
		return ErrorResult("当前部署不支持客户端工具，请改用其它方式完成")
	}

	// 开票之前先在服务端跑一遍预处理：校验、归一化、富化。
	// 参数就不合法时直接回喂错误，不推给前端白等一轮超时。
	prepared := call.Arguments
	if tool.Prepare != nil {
		out, err := tool.Prepare(ctx, call)
		if err != nil {
			return ErrorResult("参数校验未通过：%s", HumanizeError(err))
		}
		prepared = out
	}

	ticket := r.channel.Open(req.RunID)

	// ClientToolRequested 本身就是"开始执行"的信号，不再额外发
	// ToolCallStarted，否则前端会渲染出两张卡片。
	if err := emit(&ClientToolRequested{
		RunID:      req.RunID,
		ToolCallID: call.ToolCallID,
		Name:       tool.Name,
		Arguments:  prepared,
		Ticket:     ticket,
	}); err != nil {
		return ErrorResult("事件发送失败")
	}

	payload, ok := r.channel.Wait(ctx, ticket)
	if !ok {
		// 超时不能把 run 挂死：告诉模型改用文字说明。
		return TextResult("客户端未在超时时间内响应（用户可能已离开页面）。" +
			"请不要重试，改为用文字告知用户下一步该怎么做。")
	}

	okFlag := true
	if v, exists := payload["ok"]; exists {
		if b, isBool := v.(bool); isBool {
			okFlag = b
		}
	}
	content, _ := payload["content"].(string)
	if content == "" {
		if okFlag {
			content = "已完成。"
		} else {
			content = "客户端执行失败。"
		}
	}
	return ToolResult{Content: content}
}

// ---- 辅助 ----

func parseArgs(raw string) any {
	if raw == "" {
		return nil
	}
	var out any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return raw
	}
	return out
}

// parseArgsStrict 解析模型给出的参数。
//
// 空字符串按空对象处理：模型在调用无参工具时经常直接给空串。
func parseArgsStrict(raw string) (map[string]any, error) {
	trimmed := raw
	if trimmed == "" || trimmed == "null" {
		return map[string]any{}, nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}

func assistantMessage(text string, toolCalls []ai.ToolCall) ai.Message {
	msg := ai.Message{Role: ai.RoleAssistant, Content: text}
	for _, tc := range toolCalls {
		msg.ToolCalls = append(msg.ToolCalls, ai.ToolCall{
			ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments,
		})
	}
	return msg
}

func toolNames(calls []ai.ToolCall) []string {
	out := make([]string, 0, len(calls))
	for _, c := range calls {
		out = append(out, c.Name)
	}
	return out
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// HumanizeError 把错误翻译成模型或用户能理解的话。
//
// 导出给传输层复用：运行期错误既可能来自领域层（可读文案），
// 也可能来自模型供应商（英文原文）。两处各写一套会造成
// 同一个错误在事件帧与响应体里说法不一致。
func HumanizeError(err error) string {
	if err == nil {
		return ""
	}
	// 领域错误（无权限、找不到、校验失败）本身已经是可读文案。
	var de *apperr.Error
	if errors.As(err, &de) {
		if de.Detail != "" {
			return de.Message + "：" + de.Detail
		}
		return de.Message
	}
	switch {
	case errors.Is(err, ai.ErrRateLimited):
		return "模型服务限流，请稍后重试"
	case errors.Is(err, ai.ErrUnauthorized):
		return "模型服务鉴权失败，请检查 API Key"
	case errors.Is(err, ai.ErrTimeout):
		return "模型服务响应超时"
	case errors.Is(err, ai.ErrUnavailable):
		return "模型服务暂时不可用"
	case errors.Is(err, ai.ErrCanceled):
		return "已取消"
	}
	// 其它错误（多为供应商返回的英文原文）截断后原样透出，
	// 至少能让用户看到"是哪一步出的问题"。
	return truncate(err.Error(), 300)
}
