package aiagent

import (
	"encoding/json"
	"strings"
)

// SSEFrame 一帧 SSE 文本。
//
// 协议约定（前端按此解析）：
//
//	event: <name>\n
//	data: <紧凑 JSON>\n
//	\n
//
// 两处刻意与常见做法不同，都是为了少踩坑：
//
//  1. **所有事件的 data 都是 JSON 对象**（包括 message）。有些实现把
//     message 的 data 写成被引号包起来的字符串，前端必须记得"这一种
//     要再多 parse 一次"，很容易在别的事件上照抄出错。
//  2. **必须有终止帧 done**。前端据此判断流是正常结束还是被中断；
//     缺失 done 一律按失败处理，避免"转圈转到超时"。
type SSEFrame struct {
	Event string
	Data  any
}

// Render 把帧渲染为符合 SSE 规范的文本。
func (f SSEFrame) Render() string {
	payload, err := json.Marshal(f.Data)
	if err != nil {
		// 事件负载都是我们自己的结构，序列化失败属于编程错误；
		// 退化为一个错误帧而不是丢帧，避免前端一直等不到终止信号。
		payload = []byte(`{"message":"内部错误：事件序列化失败"}`)
		f.Event = EventError
	}
	var sb strings.Builder
	sb.WriteString("event: ")
	sb.WriteString(f.Event)
	sb.WriteString("\ndata: ")
	sb.Write(payload)
	sb.WriteString("\n\n")
	return sb.String()
}

// 对外事件名。
const (
	EventMessage    = "message"
	EventToolCall   = "tool_call"
	EventToolResult = "tool_result"
	EventClientTool = "client_tool"
	EventUsage      = "usage"
	EventDone       = "done"
	EventError      = "error"
)

// ToFrames 把运行时事件翻译成 SSE 帧。
//
// 返回空切片表示该事件不对外发送（如 AssistantTurn 只用于落库）。
// done 帧由调用方在 RunFinished 时发出，这里负责生成。
func ToFrames(ev Event) []SSEFrame {
	switch e := ev.(type) {
	case *TextDelta:
		if e.Text == "" {
			return nil
		}
		return []SSEFrame{{Event: EventMessage, Data: map[string]any{"text": e.Text}}}

	case *ToolCallStarted:
		return []SSEFrame{{Event: EventToolCall, Data: map[string]any{
			"tool_call_id": e.ToolCallID,
			"tool":         e.Name,
			"input":        e.Arguments,
		}}}

	case *ToolCallFinished:
		frames := make([]SSEFrame, 0, len(e.UI)+1)
		// UI 指令先发：前端可以先把卡片画出来，再收到结果的文字摘要。
		for _, d := range e.UI {
			frames = append(frames, SSEFrame{Event: d.Kind, Data: d.Payload})
		}
		frames = append(frames, SSEFrame{Event: EventToolResult, Data: map[string]any{
			"tool_call_id": e.ToolCallID,
			"tool":         e.Name,
			"output":       truncate(e.Content, 200),
		}})
		return frames

	case *ClientToolRequested:
		return []SSEFrame{{Event: EventClientTool, Data: map[string]any{
			"run_id":       e.RunID,
			"tool_call_id": e.ToolCallID,
			"ticket":       e.Ticket,
			"tool":         e.Name,
			"input":        e.Arguments,
		}}}

	case *Usage:
		return []SSEFrame{{Event: EventUsage, Data: map[string]any{
			"prompt_tokens":     e.PromptTokens,
			"completion_tokens": e.CompletionTokens,
		}}}

	case *RunFinished:
		return []SSEFrame{{Event: EventDone, Data: map[string]any{
			"run_id":      e.RunID,
			"stop_reason": e.StopReason,
			"text":        e.Text,
		}}}

	case *RunFailed:
		// 失败也要以 done 收尾：前端只认 done 作为"流结束"。
		return []SSEFrame{
			{Event: EventError, Data: map[string]any{"run_id": e.RunID, "message": e.Error}},
			{Event: EventDone, Data: map[string]any{"run_id": e.RunID, "stop_reason": "failed"}},
		}

	case *AssistantTurn:
		// 只用于落库，不产生对外帧。
		return nil
	}
	return nil
}

// RenderFrames 便捷方法：把事件渲染为可直接写入响应的文本。
func RenderFrames(ev Event) string {
	var sb strings.Builder
	for _, f := range ToFrames(ev) {
		sb.WriteString(f.Render())
	}
	return sb.String()
}

// DoneFrame 生成终止帧，供调用方在非运行时的异常路径上使用。
func DoneFrame(runID, stopReason string) SSEFrame {
	return SSEFrame{Event: EventDone, Data: map[string]any{
		"run_id":      runID,
		"stop_reason": stopReason,
	}}
}

// ErrorFrame 生成错误帧。
func ErrorFrame(message string) SSEFrame {
	return SSEFrame{Event: EventError, Data: map[string]any{"message": message}}
}

// SSEContentType SSE 响应的 Content-Type。
const SSEContentType = "text/event-stream; charset=utf-8"

// SSEHeaders 返回 SSE 响应需要的头。
//
// X-Accel-Buffering: no 是给 nginx 看的：不关掉响应缓冲的话，
// 反代会把整段流缓存到结束才吐给浏览器，"流式"就名存实亡。
func SSEHeaders() map[string]string {
	return map[string]string{
		"Content-Type":      SSEContentType,
		"Cache-Control":     "no-cache, no-transform",
		"Connection":        "keep-alive",
		"X-Accel-Buffering": "no",
	}
}
