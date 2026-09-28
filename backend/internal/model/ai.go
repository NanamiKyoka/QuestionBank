package model

// AIProvider AI 供应商配置。
//
// 重要设计决策：配置存数据库而非环境变量，因此可以在界面上热切换，
// 不需要重启服务。新增供应商只需实现 Provider 接口并联调，不动其它代码。
type AIProvider struct {
	SoftBase

	Name string `gorm:"size:64;not null;uniqueIndex" json:"name"`
	// InterfaceType 决定用哪个客户端实现：openai（兼容 OpenAI 协议）或 gemini。
	InterfaceType AIInterfaceType `gorm:"size:16;not null" json:"interface_type"`
	// BaseURL 自定义网关地址；为空则用官方地址。
	BaseURL string `gorm:"size:255" json:"base_url"`
	// APIKeyEncrypted 使用 AES-GCM 加密后存储。
	// json:"-" 保证密钥永不随接口返回；对外的脱敏展示由 service 层生成。
	APIKeyEncrypted string `gorm:"size:512" json:"-"`
	// KeyHint 形如 "sk-…a1b2"，用于界面上识别是哪个 key，不泄露完整值。
	KeyHint string `gorm:"size:32" json:"key_hint"`

	IsActive  bool `gorm:"not null;default:true" json:"is_active"`
	SortOrder int  `gorm:"not null;default:0" json:"sort_order"`

	Models []AIModel `gorm:"foreignKey:ProviderID" json:"models,omitempty"`
}

func (AIProvider) TableName() string { return "ai_providers" }

// AIModel 具体模型。
type AIModel struct {
	SoftBase

	ProviderID uint `gorm:"not null;index" json:"provider_id"`

	// Name 是调用 API 时传的模型名，如 "deepseek-chat"、"gpt-4o"。
	Name string `gorm:"size:128;not null" json:"name"`
	// DisplayName 界面上展示的名字。
	DisplayName string `gorm:"size:128" json:"display_name"`
	// ModelType 决定它能否被用于对应场景（对话/识图/向量化）。
	ModelType AIModelType `gorm:"size:16;not null;index" json:"model_type"`

	// MaxTokens 单次输出上限；ContextWindow 上下文窗口。
	MaxTokens     int `gorm:"not null;default:4096" json:"max_tokens"`
	ContextWindow int `gorm:"not null;default:32768" json:"context_window"`

	IsActive  bool `gorm:"not null;default:true" json:"is_active"`
	SortOrder int  `gorm:"not null;default:0" json:"sort_order"`

	Provider *AIProvider `gorm:"foreignKey:ProviderID" json:"provider,omitempty"`
}

func (AIModel) TableName() string { return "ai_models" }

// VectorDoc 知识点向量索引的元数据。
//
// 向量本身存在向量库里，这张表只做"账本"：记录哪些知识点已经索引、
// 用的哪个 embedding 模型、内容的哈希是多少。
// 有了它才能判断"换了个 embedding 模型，需要重建索引"。
type VectorDoc struct {
	Base

	KnowledgePointID uint `gorm:"not null;uniqueIndex" json:"knowledge_point_id"`
	SubjectID        uint `gorm:"not null;index" json:"subject_id"`

	// ModelID 生成该向量时使用的模型，模型变了就要重建。
	ModelID uint `gorm:"not null" json:"model_id"`
	// ContentHash 被向量化文本的哈希，文本没变就不必重新调用 API。
	ContentHash string `gorm:"size:64;not null" json:"content_hash"`
	// Dim 向量维度，用于校验一致性。
	Dim int `gorm:"not null" json:"dim"`
}

func (VectorDoc) TableName() string { return "vector_docs" }
