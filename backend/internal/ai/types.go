// Package ai 定义与具体厂商无关的模型调用接口。
//
// 设计目标：
//   - 业务代码只依赖 Provider 接口，不感知 OpenAI / Gemini 的差异；
//   - 流式输出用回调而不是 channel，调用方可以直接中断（返回 error 即停）；
//   - 超时与重试在适配器内部统一处理，上层不必各写一遍。
package ai

import (
	"context"
	"errors"
	"time"
)

// Role 消息角色。
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message 一条对话消息。
type Message struct {
	Role Role `json:"role"`
	// Content 文本内容。assistant 的纯工具调用轮可能为空。
	Content string `json:"content"`
	// Images 图片数据（多模态）。以 data URL 或公网地址形式传给模型。
	Images []string `json:"images,omitempty"`

	// ToolCalls assistant 发起的工具调用。
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// ToolCallID 仅 RoleTool 时有值，对应某次工具调用。
	ToolCallID string `json:"tool_call_id,omitempty"`
	Name       string `json:"name,omitempty"`
}

// ToolSpec 暴露给模型的工具定义。
type ToolSpec struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Parameters 是面向模型的 JSON Schema。
	// 刻意手写而不是由 Go 结构体反射生成：不同厂商对 $ref / anyOf 的支持
	// 参差不齐，手写 schema 最可控。
	Parameters map[string]any `json:"parameters"`
}

// ToolCall 模型请求的一次工具调用。
type ToolCall struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Arguments 是模型产出的 JSON 字符串（可能不完整，由调用方解析并容错）。
	Arguments string `json:"arguments"`
}

// Usage token 用量。
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Chunk 流式输出的一帧：文本增量或工具调用增量。
type Chunk struct {
	// Text 文本增量。为空表示本帧没有文本。
	Text string `json:"text,omitempty"`
	// ToolCall 已聚合完成的工具调用（只在参数收齐后发出一次）。
	ToolCall *ToolCall `json:"tool_call,omitempty"`
	// Usage 通常只在最后一帧出现。
	Usage *Usage `json:"usage,omitempty"`
	// FinishReason 结束原因：stop / length / tool_calls。
	FinishReason string `json:"finish_reason,omitempty"`
}

// ChatRequest 一次对话请求。
type ChatRequest struct {
	Messages []Message
	Tools    []ToolSpec
	// Model 是调用 API 时使用的模型名。
	Model string
	// Temperature 采样温度；指针为 nil 表示用服务端默认值。
	Temperature *float64
	MaxTokens   int
	// JSONMode 要求模型输出严格 JSON（用于结构化抽取）。
	JSONMode bool
	// SystemPrompt 会作为首条 system 消息插入。
	SystemPrompt string
}

// ExtractRequest 结构化抽取请求。
type ExtractRequest struct {
	Model  string
	Prompt string
	// Content 待抽取的文本内容。
	Content string
	// Images 图片（多模态识图录题）。
	Images []string
	// Schema 期望的输出结构说明（JSON Schema 文本），拼进提示词里约束模型。
	Schema string
	// MaxTokens 单次输出上限。0 表示交给服务端默认值。
	//
	// 这一项对抽取至关重要：一份卷子的答案与解析动辄上万 token，
	// 而不少兼容端点在不指定时默认只有 4096——超出的部分会被**静默截断**，
	// 表现为后半份卷子凭空消失。
	MaxTokens int
}

// ExtractedQuestion 模型抽取出的题目草稿。
//
// 字段刻意保持宽松（全部为字符串或宽松结构）：模型输出的格式不可能完全规整，
// 归一化与校验交给 domain 层，这里只负责"把模型说的搬回来"。
type ExtractedQuestion struct {
	// TempID 模型给出的临时编号，用于关联子题。
	TempID string `json:"temp_id,omitempty"`
	// ParentTempID 子题指向母题。
	ParentTempID string `json:"parent_temp_id,omitempty"`

	QuestionType string            `json:"question_type,omitempty"`
	Stem         string            `json:"stem"`
	Options      []ExtractedOption `json:"options,omitempty"`
	Answer       string            `json:"answer,omitempty"`
	Analysis     string            `json:"analysis,omitempty"`
	Difficulty   int               `json:"difficulty,omitempty"`

	KnowledgePoints []string `json:"knowledge_points,omitempty"`
	Tags            []string `json:"tags,omitempty"`

	// Warnings 抽取过程中的问题（如答案无法解析），供人工审核时提示。
	Warnings []string `json:"warnings,omitempty"`
}

// ExtractedOption 抽取出的选项。
type ExtractedOption struct {
	Label   string `json:"label"`
	Content string `json:"content"`
}

// Provider 模型供应商接口。
type Provider interface {
	// Name 供应商标识，用于日志与错误信息。
	Name() string
	// ChatStream 流式对话。emit 返回非 nil 错误时立即中止请求。
	ChatStream(ctx context.Context, req ChatRequest, emit func(Chunk) error) error
	// ExtractQuestions 结构化抽取题目。
	ExtractQuestions(ctx context.Context, req ExtractRequest) ([]ExtractedQuestion, error)
	// Embed 文本向量化。
	Embed(ctx context.Context, model string, texts []string) ([][]float32, error)
}

// 供应商类型。新增厂商时在这里登记，并在工厂里加一个分支。
const (
	TypeOpenAI = "openai" // OpenAI 兼容协议，覆盖 DeepSeek / 通义 / 智谱 等
	TypeGemini = "gemini"
)

// 领域错误。上层据此决定是重试、降级还是提示用户。
var (
	// ErrRateLimited 触发限流，值得退避重试。
	ErrRateLimited = errors.New("ai: 请求被限流")
	// ErrUnauthorized API Key 无效或权限不足，重试无意义。
	ErrUnauthorized = errors.New("ai: 鉴权失败")
	// ErrModelNotFound 模型不存在或未开通。
	ErrModelNotFound = errors.New("ai: 模型不存在")
	// ErrTimeout 请求超时。
	ErrTimeout = errors.New("ai: 请求超时")
	// ErrUnavailable 服务端故障，值得重试。
	ErrUnavailable = errors.New("ai: 服务不可用")
	// ErrCanceled 调用方主动取消。
	ErrCanceled = errors.New("ai: 调用已取消")
	// ErrTruncated 输出撞到了 max_tokens 上限，结果不完整。
	//
	// 单独成一个领域错误而不是复用解析失败：用户要做的不是"再试一次"，
	// 而是去调大上限——重试只会得到一模一样的截断结果。
	ErrTruncated = errors.New("ai: 输出被上限截断")
)

// Config 供应商运行配置。由 ai_config 服务从数据库装载。
type Config struct {
	// ID 供应商记录 id，用于日志定位。
	ID uint
	// Name 展示名。
	Name string
	// InterfaceType TypeOpenAI / TypeGemini。
	InterfaceType string
	// BaseURL 自定义网关地址，为空时用官方地址。
	BaseURL string
	// APIKey 明文密钥（已在存储层加密，取出时解密）。
	APIKey string

	// Timeout 单次请求超时。
	Timeout time.Duration
	// MaxRetries 失败重试次数（仅对流式请求开始之前生效）。
	MaxRetries int
}

// defaultConfig 给未显式配置的字段填默认值。
func (c Config) withDefaults() Config {
	if c.Timeout <= 0 {
		c.Timeout = 120 * time.Second
	}
	if c.MaxRetries < 0 {
		c.MaxRetries = 0
	}
	return c
}

// validate 检查配置是否可用于发起请求。
func (c Config) validate() error {
	if c.APIKey == "" {
		return ErrUnauthorized
	}
	switch c.InterfaceType {
	case TypeOpenAI, TypeGemini:
	default:
		return errors.New("ai: 未知的供应商类型 " + c.InterfaceType)
	}
	return nil
}
