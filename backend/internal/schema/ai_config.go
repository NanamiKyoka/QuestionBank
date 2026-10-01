package schema

import (
	"fmt"
	"time"

	"github.com/NanamiKyoka/questionmind/internal/model"
)

// ---- 供应商 ----

type ProviderCreateRequest struct {
	Name          string `json:"name" binding:"required,min=1,max=64"`
	InterfaceType string `json:"interface_type" binding:"required,oneof=openai gemini"`
	BaseURL       string `json:"base_url" binding:"omitempty,url,max=255"`
	APIKey        string `json:"api_key" binding:"required,min=1,max=512"`

	IsActive  *bool `json:"is_active"`
	SortOrder *int  `json:"sort_order"`
}

// ProviderUpdateRequest 更新供应商。
//
// APIKey 的特殊约定：传空表示不修改；传掩码（含 * 或 …）同样视为不修改；
// 只有传了新的明文才真正覆盖。这样前端可以把接口返回的掩码原样回传，
// 不会因为"编辑了别处"就把密钥写成掩码串。
type ProviderUpdateRequest struct {
	Name          *string `json:"name" binding:"omitempty,min=1,max=64"`
	InterfaceType *string `json:"interface_type" binding:"omitempty,oneof=openai gemini"`
	BaseURL       *string `json:"base_url" binding:"omitempty,max=255"`
	APIKey        *string `json:"api_key" binding:"omitempty,max=512"`

	IsActive  *bool `json:"is_active"`
	SortOrder *int  `json:"sort_order"`
}

// ProviderResponse 供应商响应。密钥永不返回，只给掩码与"是否已配置"。
type ProviderResponse struct {
	ID            uint   `json:"id"`
	Name          string `json:"name"`
	InterfaceType string `json:"interface_type"`
	BaseURL       string `json:"base_url"`

	// KeyHint 形如 "sk-abc…wxyz"，用于辨认是哪把密钥。
	KeyHint string `json:"key_hint"`
	// HasKey 是否已配置密钥（前端据此显示"未配置"提示）。
	HasKey bool `json:"has_key"`

	IsActive  bool `json:"is_active"`
	SortOrder int  `json:"sort_order"`

	Models []ModelResponse `json:"models"`

	CreatedAt time.Time `json:"created_at"`
}

// NewProviderResponse 组装响应。
func NewProviderResponse(p *model.AIProvider) ProviderResponse {
	r := ProviderResponse{
		ID:            p.ID,
		Name:          p.Name,
		InterfaceType: string(p.InterfaceType),
		BaseURL:       p.BaseURL,
		KeyHint:       p.KeyHint,
		HasKey:        p.APIKeyEncrypted != "",
		IsActive:      p.IsActive,
		SortOrder:     p.SortOrder,
		CreatedAt:     p.CreatedAt,
		Models:        make([]ModelResponse, 0, len(p.Models)),
	}
	for i := range p.Models {
		r.Models = append(r.Models, NewModelResponse(&p.Models[i]))
	}
	return r
}

// ---- 模型 ----

type ModelCreateRequest struct {
	ProviderID uint   `json:"provider_id" binding:"required"`
	Name       string `json:"name" binding:"required,min=1,max=128"`

	DisplayName string `json:"display_name" binding:"omitempty,max=128"`

	// Capabilities 模型具备的能力，多选。
	//
	// 与 ModelType 二选一：Capabilities 为空时回退读 ModelType，
	// 这样只发单值的旧客户端不必立刻改造。
	Capabilities []string `json:"capabilities" binding:"omitempty,max=3,dive,oneof=text vision embedding"`
	ModelType    string   `json:"model_type" binding:"omitempty,oneof=text vision embedding"`

	MaxTokens     *int `json:"max_tokens" binding:"omitempty,min=1,max=2000000"`
	ContextWindow *int `json:"context_window" binding:"omitempty,min=1,max=20000000"`

	IsActive  *bool `json:"is_active"`
	SortOrder *int  `json:"sort_order"`
}

// Caps 归一化后的能力集合。调用方先确认过至少有一项，这里不再判空。
func (r *ModelCreateRequest) Caps() ([]string, error) {
	if len(r.Capabilities) > 0 {
		caps, err := model.NormalizeAIModelCaps(r.Capabilities)
		if err != nil {
			return nil, err
		}
		return caps.Values(), nil
	}
	if r.ModelType != "" {
		caps, err := model.NormalizeAIModelCaps([]string{r.ModelType})
		if err != nil {
			return nil, err
		}
		return caps.Values(), nil
	}
	return nil, fmt.Errorf("请至少选择一种模型能力")
}

type ModelUpdateRequest struct {
	Name        *string `json:"name" binding:"omitempty,min=1,max=128"`
	DisplayName *string `json:"display_name" binding:"omitempty,max=128"`

	// Capabilities / ModelType 语义同 ModelCreateRequest，二选一。
	// 传了空数组表示"清空"——那是非法的（没有任何能力的模型用不了），
	// 与"不传"必须区分开，因此这里只认 nil。
	Capabilities []string `json:"capabilities" binding:"omitempty,max=3,dive,oneof=text vision embedding"`
	ModelType    *string  `json:"model_type" binding:"omitempty,oneof=text vision embedding"`

	MaxTokens     *int `json:"max_tokens" binding:"omitempty,min=1,max=2000000"`
	ContextWindow *int `json:"context_window" binding:"omitempty,min=1,max=20000000"`

	IsActive  *bool `json:"is_active"`
	SortOrder *int  `json:"sort_order"`
}

// CapsUpdate 返回要写入的规范化集合；两个字段都没传时返回 (nil, false, nil)。
func (r *ModelUpdateRequest) CapsUpdate() ([]string, bool, error) {
	if r.Capabilities != nil {
		caps, err := model.NormalizeAIModelCaps(r.Capabilities)
		if err != nil {
			return nil, false, err
		}
		return caps.Values(), true, nil
	}
	if r.ModelType != nil {
		caps, err := model.NormalizeAIModelCaps([]string{*r.ModelType})
		if err != nil {
			return nil, false, err
		}
		return caps.Values(), true, nil
	}
	return nil, false, nil
}

type ModelResponse struct {
	ID           uint   `json:"id"`
	ProviderID   uint   `json:"provider_id"`
	ProviderName string `json:"provider_name"`
	Name         string `json:"name"`
	DisplayName  string `json:"display_name"`

	// Capabilities 该模型具备的全部能力，供界面多选框与用途下拉使用。
	Capabilities []string `json:"capabilities"`
	// ModelType 归一化存储形式（如 "text,vision"）。
	//
	// 与 Capabilities 同源、不是第二份状态：只是把同一个集合按
	// 数据库里的写法原样回显，便于用户手工核对。
	ModelType string `json:"model_type"`

	MaxTokens     int `json:"max_tokens"`
	ContextWindow int `json:"context_window"`

	IsActive  bool `json:"is_active"`
	SortOrder int  `json:"sort_order"`

	// InUse 该模型当前被选为默认模型的用途（可多个）。
	InUse bool `json:"in_use"`
	// InUseFor 当前被选为默认的具体用途，便于界面直接标出是哪一栏在用它。
	InUseFor []string `json:"in_use_for"`

	CreatedAt time.Time `json:"created_at"`
}

// NewModelResponse 组装响应。
func NewModelResponse(m *model.AIModel) ModelResponse {
	caps := m.Caps()
	r := ModelResponse{
		ID:            m.ID,
		ProviderID:    m.ProviderID,
		Name:          m.Name,
		DisplayName:   m.DisplayName,
		Capabilities:  caps.Values(),
		ModelType:     caps.String(),
		MaxTokens:     m.MaxTokens,
		ContextWindow: m.ContextWindow,
		IsActive:      m.IsActive,
		SortOrder:     m.SortOrder,
		CreatedAt:     m.CreatedAt,
	}
	if m.Provider != nil {
		r.ProviderName = m.Provider.Name
	}
	return r
}

// ---- 当前生效配置 ----

// ActiveConfigResponse AI 运行配置总览。
type ActiveConfigResponse struct {
	// TextModelID / VisionModelID / EmbeddingModelID 为 0 表示未配置。
	TextModelID      uint `json:"text_model_id"`
	VisionModelID    uint `json:"vision_model_id"`
	EmbeddingModelID uint `json:"embedding_model_id"`

	Text      *ModelResponse `json:"text,omitempty"`
	Vision    *ModelResponse `json:"vision,omitempty"`
	Embedding *ModelResponse `json:"embedding,omitempty"`

	// Ready 为真表示至少配置了文本模型，AI 功能可用。
	Ready bool `json:"ready"`
	// Message 面向用户的说明（如"未配置文本模型，智能录题不可用"）。
	Message string `json:"message"`
}

// SetActiveConfigRequest 设置当前生效的模型。
// 字段用指针区分"不修改"与"清空"。
type SetActiveConfigRequest struct {
	TextModelID      *uint `json:"text_model_id"`
	VisionModelID    *uint `json:"vision_model_id"`
	EmbeddingModelID *uint `json:"embedding_model_id"`
}

// ProviderTestResponse 连通性测试结果。
type ProviderTestResponse struct {
	OK    bool   `json:"ok"`
	Model string `json:"model,omitempty"`
	// Reply 模型返回的简短内容（截断）。
	Reply string `json:"reply,omitempty"`
	// Error 失败原因。
	Error string `json:"error,omitempty"`
	// LatencyMs 耗时。
	LatencyMs int64 `json:"latency_ms"`
}

// ProviderTestRequest 连通性测试。
type ProviderTestRequest struct {
	// ModelID 指定用哪个模型测试；为空则取该供应商下第一个启用的文本模型。
	ModelID uint `json:"model_id"`
	// Prompt 自定义测试提示词。
	Prompt string `json:"prompt" binding:"omitempty,max=500"`
}
