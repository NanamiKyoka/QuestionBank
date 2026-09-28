package schema

// VectorStatusResponse 知识点向量索引状态。
type VectorStatusResponse struct {
	SubjectID uint `json:"subject_id"`

	// Total 学科下知识点总数。
	Total int64 `json:"total"`
	// Indexed 已完成索引的数量。
	Indexed int64 `json:"indexed"`
	// Missing 尚未索引的数量。
	Missing int64 `json:"missing"`

	// Ready 为真表示向量检索可用（配了向量模型且有空索引）。
	Ready bool `json:"ready"`

	// ModelID / ModelName 当前配置的向量模型。
	ModelID   uint   `json:"model_id"`
	ModelName string `json:"model_name"`

	// NeedsRebuild 为真表示索引不完整或混用了多个模型，需要重建。
	//
	// 混用模型是有害的：不同模型的向量空间不可比，
	// 拿 A 模型的向量去和 B 模型的查询向量算余弦，结果没有意义。
	NeedsRebuild bool `json:"needs_rebuild"`

	// Message 面向用户的说明。
	Message string `json:"message"`
}

// ReindexResponse 重建索引结果。
type ReindexResponse struct {
	Total     int   `json:"total"`
	Indexed   int   `json:"indexed"`
	Skipped   int   `json:"skipped"`
	Failed    int   `json:"failed"`
	ElapsedMs int64 `json:"elapsed_ms"`

	// Failures 失败的知识点与原因（前若干条）。
	Failures []ReindexFailure `json:"failures,omitempty"`
}

// ReindexFailure 单条索引失败。
type ReindexFailure struct {
	KnowledgePointID uint   `json:"knowledge_point_id"`
	Name             string `json:"name"`
	Error            string `json:"error"`
}

// VectorSearchItem 一条检索结果。
type VectorSearchItem struct {
	KnowledgePointID uint    `json:"knowledge_point_id"`
	Name             string  `json:"name"`
	Path             string  `json:"path"`
	Score            float32 `json:"score"`
}

// VectorSearchResponse 语义检索结果。
type VectorSearchResponse struct {
	Query   string             `json:"query"`
	Results []VectorSearchItem `json:"results"`
	// ModelName 使用的向量模型，便于排查"为什么结果不准"。
	ModelName string `json:"model_name"`
}

// KnowledgeSuggestion 知识点匹配建议。
type KnowledgeSuggestion struct {
	KnowledgePointID uint    `json:"knowledge_point_id"`
	Name             string  `json:"name"`
	Path             string  `json:"path"`
	Score            float32 `json:"score"`
	// Source 建议来源：vector（向量检索）| rerank（LLM 重排后确认）。
	Source string `json:"source"`
}
