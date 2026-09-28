package model

import "time"

// ChatSession AI 对话会话。
type ChatSession struct {
	SoftBase

	UserID    uint  `gorm:"not null;index" json:"user_id"`
	SubjectID *uint `gorm:"index" json:"subject_id"`

	Title string `gorm:"size:200" json:"title"`
	// Scene 记录会话是在哪个页面场景下创建的（题库页/组卷页/录题页），
	// 用于决定给模型暴露哪些工具。注意：这只是"减少干扰"，
	// 不是权限机制——scene 由客户端上报，不可信，真正的门禁在 service 层。
	Scene string `gorm:"size:32" json:"scene"`

	MessageCount  int        `gorm:"not null;default:0" json:"message_count"`
	LastMessageAt *time.Time `json:"last_message_at"`
}

func (ChatSession) TableName() string { return "chat_sessions" }

// ChatMessage 对话消息。
//
// 关于 tool 消息：工具调用与工具结果都会落库，但**不可见**——
// 查询历史消息时必须过滤掉 role='tool' 的记录，只保留 user/assistant。
// 这样前端渲染简单，同时排障时数据还在。
type ChatMessage struct {
	Base

	SessionID uint `gorm:"not null;index" json:"session_id"`

	Role ChatRole `gorm:"size:16;not null" json:"role"`

	// Content 文本内容。assistant 消息可能为空（纯工具调用轮）。
	Content string `gorm:"type:text" json:"content"`
	// Reasoning 部分模型返回的思维链，可为空。
	Reasoning string `gorm:"type:text" json:"reasoning,omitempty"`

	// ToolCalls assistant 发起的工具调用列表（JSON）。
	ToolCalls JSON `json:"tool_calls,omitempty"`
	// ToolCallID 仅 role=tool 时有值，对应某次工具调用。
	ToolCallID string `gorm:"size:64;index" json:"tool_call_id,omitempty"`

	// Attachments 用户上传的附件（图片等）。多模态对话需要。
	Attachments JSON `json:"attachments,omitempty"`

	// AgentRunID 关联本次运行，便于把消息与运行轨迹对上。
	AgentRunID *string `gorm:"size:36;index" json:"agent_run_id,omitempty"`

	// Token 用量统计。
	PromptTokens     int `gorm:"not null;default:0" json:"prompt_tokens"`
	CompletionTokens int `gorm:"not null;default:0" json:"completion_tokens"`
}

func (ChatMessage) TableName() string { return "chat_messages" }

// AgentRun 一次助手运行。
//
// 一次"用户发一句话"可能触发多轮模型调用与多次工具调用，整体记为一次 Run。
// 落库的目的是排障：能完整还原模型每一步看到了什么、做了什么、花了多久。
type AgentRun struct {
	// ID 由服务端生成的 UUID，运行期间就要用于 SSE 事件与客户端票据，
	// 因此不能等数据库自增。
	ID string `gorm:"size:36;primaryKey" json:"id"`

	SessionID *uint `gorm:"index" json:"session_id"`
	UserID    uint  `gorm:"not null;index" json:"user_id"`
	SubjectID *uint `gorm:"index" json:"subject_id"`

	// Surface 调用入口：api | chat | worker。用于审计"是谁触发的"。
	Surface string `gorm:"size:16;not null" json:"surface"`

	Status AgentRunStatus `gorm:"size:16;not null;index" json:"status"`
	Error  string         `gorm:"type:text" json:"error,omitempty"`

	// ---- 模型快照 ----
	// 记录运行时用的模型信息而非外键，这样即使模型配置后来被删掉，
	// 历史运行记录依然能说明当时用的是哪个模型。
	ModelID      *uint  `json:"model_id"`
	ModelName    string `gorm:"size:128" json:"model_name"`
	ProviderName string `gorm:"size:64" json:"provider_name"`

	// StopReason: completed | budget_exhausted | error
	StopReason string `gorm:"size:32" json:"stop_reason"`

	// 用量汇总。
	PromptTokens     int `gorm:"not null;default:0" json:"prompt_tokens"`
	CompletionTokens int `gorm:"not null;default:0" json:"completion_tokens"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (AgentRun) TableName() string { return "agent_runs" }

// AgentStep 运行中的一步：一次模型输出或一次工具调用。
type AgentStep struct {
	Base

	RunID string `gorm:"size:36;not null;index:idx_step_run_idx,priority:1" json:"run_id"`
	// Idx 步序号，从 0 开始，用于还原执行顺序。
	Idx int `gorm:"not null;index:idx_step_run_idx,priority:2" json:"idx"`

	// StepType: assistant | tool_call
	StepType string `gorm:"size:16;not null" json:"step_type"`

	// ToolName 仅 tool_call 步有值。
	ToolName string `gorm:"size:64;index" json:"tool_name,omitempty"`
	// Args 工具入参；Result 工具产出（长文本会截断后存储）。
	Args   JSON `json:"args,omitempty"`
	Result JSON `json:"result,omitempty"`

	// LatencyMs 该步耗时，用于分析性能瓶颈。
	LatencyMs int `gorm:"not null;default:0" json:"latency_ms"`
}

func (AgentStep) TableName() string { return "agent_steps" }

// PromptTemplate 提示词模板。管理员可在线编辑，无需改代码。
type PromptTemplate struct {
	SoftBase

	// Key 唯一标识，如 extract_questions / solve_questions。
	Key string `gorm:"size:64;not null;uniqueIndex" json:"key"`
	// Name 展示名。
	Name string `gorm:"size:128;not null" json:"name"`
	// Scene 适用场景，便于界面上分组。
	Scene string `gorm:"size:32" json:"scene"`

	SystemPrompt string `gorm:"type:text" json:"system_prompt"`
	UserPrompt   string `gorm:"type:text" json:"user_prompt"`

	IsBuiltin bool `gorm:"not null;default:false" json:"is_builtin"`
	UpdatedBy uint `json:"updated_by"`
}

func (PromptTemplate) TableName() string { return "prompt_templates" }
