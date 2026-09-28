package schema

import (
	"time"

	"github.com/NanamiKyoka/questionmind/internal/model"
)

// SessionCreateRequest 新建对话会话。
type SessionCreateRequest struct {
	Title string `json:"title" binding:"omitempty,max=200"`
	// SubjectID 会话所属学科；影响工具能访问的数据范围。
	SubjectID *uint `json:"subject_id"`
	// Scene 前端页面场景，决定这一轮暴露哪些工具。
	// 由客户端自报，因此只用于收敛工具集，不作为授权依据。
	Scene string `json:"scene" binding:"omitempty,max=32"`
}

// SessionUpdateRequest 修改会话。
type SessionUpdateRequest struct {
	Title *string `json:"title" binding:"omitempty,max=200"`
}

// SessionResponse 会话。
type SessionResponse struct {
	ID        uint   `json:"id"`
	UserID    uint   `json:"user_id"`
	SubjectID *uint  `json:"subject_id"`
	Title     string `json:"title"`
	Scene     string `json:"scene"`

	MessageCount  int        `json:"message_count"`
	LastMessageAt *time.Time `json:"last_message_at"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// NewSessionResponse 组装响应。
func NewSessionResponse(s *model.ChatSession) SessionResponse {
	return SessionResponse{
		ID:            s.ID,
		UserID:        s.UserID,
		SubjectID:     s.SubjectID,
		Title:         s.Title,
		Scene:         s.Scene,
		MessageCount:  s.MessageCount,
		LastMessageAt: s.LastMessageAt,
		CreatedAt:     s.CreatedAt,
		UpdatedAt:     s.UpdatedAt,
	}
}

// SendMessageRequest 发送一条消息。
type SendMessageRequest struct {
	// Content 文本内容。
	Content string `json:"content" binding:"max=20000"`
	// Images 图片（data URL 或 http 地址），支持多模态提问。
	Images []string `json:"images" binding:"max=6"`
	// Scene 页面场景，决定本轮暴露的工具集。
	Scene string `json:"scene" binding:"omitempty,max=32"`
}

// MessageResponse 消息。
type MessageResponse struct {
	ID        uint           `json:"id"`
	SessionID uint           `json:"session_id"`
	Role      model.ChatRole `json:"role"`
	Content   string         `json:"content"`

	ToolCalls model.JSON `json:"tool_calls,omitempty"`
	Images    model.JSON `json:"images,omitempty"`

	// AgentRunID 该消息对应的运行 id，便于把消息与运行轨迹对上。
	AgentRunID *string `json:"agent_run_id,omitempty"`

	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`

	CreatedAt time.Time `json:"created_at"`
}

// NewMessageResponse 组装响应。
func NewMessageResponse(m *model.ChatMessage) MessageResponse {
	r := MessageResponse{
		ID:               m.ID,
		SessionID:        m.SessionID,
		Role:             m.Role,
		Content:          m.Content,
		ToolCalls:        m.ToolCalls,
		AgentRunID:       m.AgentRunID,
		PromptTokens:     m.PromptTokens,
		CompletionTokens: m.CompletionTokens,
		CreatedAt:        m.CreatedAt,
	}
	if !m.Attachments.IsEmpty() {
		r.Images = m.Attachments
	}
	return r
}

// ClientToolResultRequest 前端回传客户端工具的执行结果。
type ClientToolResultRequest struct {
	// Ticket 后端下发的一次性票据。
	Ticket string `json:"ticket" binding:"required"`
	// OK 前端是否执行成功。
	OK *bool `json:"ok"`
	// Content 回喂给模型的文本描述。
	Content string `json:"content" binding:"max=4000"`
	// Data 结构化结果（前端自用与日志）。
	Data map[string]any `json:"data"`
}

// AgentRunResponse 运行记录，供排障展示。
type AgentRunResponse struct {
	ID         string               `json:"id"`
	SessionID  *uint                `json:"session_id"`
	Status     model.AgentRunStatus `json:"status"`
	StopReason string               `json:"stop_reason"`
	Error      string               `json:"error,omitempty"`

	ModelName    string `json:"model_name"`
	ProviderName string `json:"provider_name"`

	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// NewAgentRunResponse 组装响应。
func NewAgentRunResponse(r *model.AgentRun) AgentRunResponse {
	return AgentRunResponse{
		ID:               r.ID,
		SessionID:        r.SessionID,
		Status:           r.Status,
		StopReason:       r.StopReason,
		Error:            r.Error,
		ModelName:        r.ModelName,
		ProviderName:     r.ProviderName,
		PromptTokens:     r.PromptTokens,
		CompletionTokens: r.CompletionTokens,
		CreatedAt:        r.CreatedAt,
		UpdatedAt:        r.UpdatedAt,
	}
}
