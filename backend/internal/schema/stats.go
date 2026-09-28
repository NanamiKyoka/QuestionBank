package schema

// QuestionStats 题目统计。
type QuestionStats struct {
	Total int64 `json:"total"`

	// 扁平字段便于前端直接展示卡片，不必在对象里再查一次。
	Draft     int64 `json:"draft"`
	Pending   int64 `json:"pending"`
	Published int64 `json:"published"`
	Archived  int64 `json:"archived"`

	// ByType / ByDifficulty 用于画分布图。键是枚举字符串。
	ByType       map[string]int64 `json:"by_type"`
	ByDifficulty map[string]int64 `json:"by_difficulty"`
}

// TrendPoint 趋势图上的一个点。
type TrendPoint struct {
	Day   string `json:"day"`
	Total int64  `json:"total"`
}

// SubjectStatsResponse 学科维度的统计概览。
type SubjectStatsResponse struct {
	SubjectID   uint   `json:"subject_id"`
	SubjectName string `json:"subject_name"`

	Questions QuestionStats `json:"questions"`

	KnowledgePoints int64 `json:"knowledge_points"`
	Tags            int64 `json:"tags"`
	Papers          int64 `json:"papers"`

	// PapersByStatus 试卷状态分布。
	PapersByStatus map[string]int64 `json:"papers_by_status"`

	// ImportTotal / ImportSaved 导入任务数与累计入库题数。
	ImportTotal int64 `json:"import_total"`
	ImportSaved int64 `json:"import_saved"`

	// QuestionTrend 最近 30 天的建题趋势。
	//
	// 只包含有数据的日期，前端需要自行补齐空缺——这里是刻意不补零的：
	// 补零属于展示层的决定，服务端补了反而限制了前端换图表库时的自由度。
	QuestionTrend []TrendPoint `json:"question_trend"`

	// RecentPapers 最近更新的试卷。
	RecentPapers []RecentPaperItem `json:"recent_papers"`
}

// RecentPaperItem 最近试卷条目。
type RecentPaperItem struct {
	ID        uint   `json:"id"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	UpdatedAt string `json:"updated_at"`
}

// GlobalStatsResponse 全局统计概览（管理员视角）。
type GlobalStatsResponse struct {
	Subjects        int64 `json:"subjects"`
	Questions       int64 `json:"questions"`
	KnowledgePoints int64 `json:"knowledge_points"`
	Tags            int64 `json:"tags"`
	Papers          int64 `json:"papers"`
	Users           int64 `json:"users"`

	// AI AI 用量，仅管理员可见（涉及成本）。
	AI AIUsageStats `json:"ai"`

	// TopActions 最近 30 天最常见的操作类型。
	TopActions []ActionCount `json:"top_actions"`
}

// AIUsageStats AI 调用用量。
type AIUsageStats struct {
	Runs             int64 `json:"runs"`
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

// ActionCount 操作类型计数。
type ActionCount struct {
	Action string `json:"action"`
	Total  int64  `json:"total"`
}
