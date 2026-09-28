// Package aiagent 实现 AI 助手的 Agent 运行时：工具循环、事件流、前端工具通道。
//
// 分层用意与传输解耦：运行时只产出 Event，由 SSE 适配器翻译成前端协议。
// 将来要接入 WebSocket、MCP 或后台任务，复用同一套运行时即可，
// 不必把传输细节写进业务逻辑。
package aiagent

import "time"

// Event 运行时产生的事件。
type Event interface {
	// EventType 事件类型名，用于日志与 SSE 帧名映射。
	EventType() string
}

// TextDelta 助手输出的文本增量。
type TextDelta struct {
	Text string
}

func (*TextDelta) EventType() string { return "text.delta" }

// ToolCallStarted 开始执行一个服务端工具。
//
// 客户端工具**不发**这个事件：它由 ClientToolRequested 一次性表达
// "开始执行"，否则前端会渲染出两张卡片，第一张永远等不到收尾。
type ToolCallStarted struct {
	ToolCallID string
	Name       string
	Arguments  any
}

func (*ToolCallStarted) EventType() string { return "tool.call" }

// ToolCallFinished 工具执行结束（成功或失败都在这里回传）。
type ToolCallFinished struct {
	ToolCallID string
	Name       string
	Content    string
	UI         []UIDirective
}

func (*ToolCallFinished) EventType() string { return "tool.result" }

// ClientToolRequested 请前端执行一个工具，并把结果回传到 Ticket。
type ClientToolRequested struct {
	RunID      string
	ToolCallID string
	Name       string
	Arguments  any
	Ticket     string
}

func (*ClientToolRequested) EventType() string { return "client_tool.requested" }

// AssistantTurn 一轮带工具调用的助手输出。
//
// 存在的目的是让调用方把它落库——运行时不知道会话消息表长什么样。
// **不产生任何对外的 SSE 帧**。
type AssistantTurn struct {
	RunID     string
	Text      string
	ToolCalls []ToolCallRecord
}

func (*AssistantTurn) EventType() string { return "assistant.turn" }

// ToolCallRecord 落库用的工具调用记录。
type ToolCallRecord struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Usage 用量统计事件。
type Usage struct {
	PromptTokens     int
	CompletionTokens int
}

func (*Usage) EventType() string { return "usage" }

// RunFinished 运行正常结束。
type RunFinished struct {
	RunID      string
	Text       string
	StopReason string
}

func (*RunFinished) EventType() string { return "run.finished" }

// RunFailed 运行失败。
type RunFailed struct {
	RunID string
	Error string
}

func (*RunFailed) EventType() string { return "run.failed" }

// StopReason 结束原因。
const (
	StopCompleted       = "completed"
	StopBudgetExhausted = "budget_exhausted"
	StopToolLimit       = "tool_limit"
	StopCanceled        = "canceled"
)

// UIDirective 需要前端渲染的指令。
//
// 与 ClientToolRequested 的区别：UI 指令是"顺带告诉前端画点什么"
// （如题目提案卡片），而客户端工具是"请前端真的去做一件事并回传结果"。
type UIDirective struct {
	Kind    string         `json:"kind"`
	Payload map[string]any `json:"payload"`
}

// ProposalDirective 构造题目提案指令。
func ProposalDirective(proposalType string, ids []uint) UIDirective {
	return UIDirective{
		Kind: "proposal",
		Payload: map[string]any{
			"type": proposalType,
			"ids":  ids,
		},
	}
}

// Budget 一次运行的预算。
//
// 四重限制而不是单看轮次：模型可能在一轮里发起几十个工具调用，
// 也可能每一轮都很慢。只看轮次挡不住这两种失控。
type Budget struct {
	MaxTurns     int
	MaxToolCalls int
	WallClock    time.Duration
}

// DefaultBudget 默认预算：5 轮对话、20 次工具调用、3 分钟。
func DefaultBudget() Budget {
	return Budget{MaxTurns: 5, MaxToolCalls: 20, WallClock: 3 * time.Minute}
}

// Normalize 补齐未设置的限制，避免 0 值导致"一次都跑不了"。
func (b Budget) Normalize() Budget {
	if b.MaxTurns <= 0 {
		b.MaxTurns = 5
	}
	if b.MaxToolCalls <= 0 {
		b.MaxToolCalls = 20
	}
	if b.WallClock <= 0 {
		b.WallClock = 3 * time.Minute
	}
	return b
}
