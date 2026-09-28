package model

// Question 题目——系统的核心实体。
//
// 内容字段全部使用 JSON 列，因为五种题型的答案结构差异很大：
//
//	single_choice   {kind:"single",   correct:"opt_xxx"}
//	multiple_choice {kind:"multiple", correct:["opt_x","opt_y"]}
//	true_false      {kind:"boolean",  correct:true}
//	fill_blank      {kind:"blanks",   blanks:[{id:"blk_1",accepts:[["答案1","答案2"]]}]}
//	essay           {kind:"reference",text:"..."}
//
// 这些结构之间的不变式（correct 必须是选项 id 的子集、blanks 顺序必须与题干
// 空位一致等）由 domain 层校验，数据库不做约束——这是刻意的取舍：
// 数据库负责完整性，领域负责业务规则。
type Question struct {
	SoftBase

	SubjectID uint `gorm:"not null;index:idx_q_subject_status,priority:1" json:"subject_id"`
	// ParentID 母题；为空表示独立题。子题通过它挂在母题下。
	ParentID *uint `gorm:"index" json:"parent_id"`

	QuestionType QuestionType   `gorm:"size:32;not null;index" json:"question_type"`
	Difficulty   int            `gorm:"not null;default:3;index" json:"difficulty"` // 1-5
	Status       QuestionStatus `gorm:"size:16;not null;index:idx_q_subject_status,priority:2" json:"status"`
	Visibility   Visibility     `gorm:"size:16;not null;default:public;index" json:"visibility"`

	// ---- 内容 ----
	// Stem 题干，RichDoc 结构：{"type":"doc","content":[...]}
	Stem JSON `gorm:"not null" json:"stem"`
	// Options 选项数组：[{"id":"opt_ab12cd34","label":"A","content":{...}}]
	Options JSON `json:"options"`
	// Answer 答案，结构随题型变化（见上方注释）。
	Answer JSON `gorm:"not null" json:"answer"`
	// Analysis 解析；Solution 解题思路；Summary 小结。
	Analysis JSON `json:"analysis"`
	Solution JSON `json:"solution"`
	Summary  JSON `json:"summary"`

	// StemText 题干的纯文本投影，用于列表展示与关键词检索。
	// 冗余存储是刻意为之：避免列表查询时反序列化整棵 RichDoc。
	StemText string `gorm:"type:text" json:"stem_text"`

	// ContentVersion 富文本 schema 版本，用于将来做内容格式迁移。
	ContentVersion int `gorm:"not null;default:1" json:"content_version"`
	// ContentRevision 内容修订号：任何影响题目呈现的字段变更都会 +1。
	// 试卷节点记录它引用的题目的 content_revision，据此判断是否需要"同步最新内容"。
	ContentRevision int64 `gorm:"not null;default:1" json:"content_revision"`

	// ReviewCount 累计审核通过次数，达到学科阈值后自动发布。
	ReviewCount int `gorm:"not null;default:0" json:"review_count"`

	// Source 来源描述（原始文件名等）；ImportTaskID 关联导入任务。
	Source       string `gorm:"size:255" json:"source"`
	ImportTaskID *uint  `gorm:"index" json:"import_task_id"`

	CreatedBy uint `gorm:"not null;index" json:"created_by"`
	UpdatedBy uint `json:"updated_by"`

	// ---- 关联（由 repository 层显式加载，不交给 ORM 隐式魔法）----
	Tags            []Tag            `gorm:"-" json:"tags,omitempty"`
	KnowledgePoints []KnowledgePoint `gorm:"-" json:"knowledge_points,omitempty"`
	Children        []Question       `gorm:"-" json:"children,omitempty"`
}

func (Question) TableName() string { return "questions" }

// ReviewLog 审核日志。每次审核动作都留痕，可追溯到人。
type ReviewLog struct {
	Base

	QuestionID uint `gorm:"not null;index" json:"question_id"`
	ReviewerID uint `gorm:"not null;index" json:"reviewer_id"`

	// Action: approve | reject | archive | restore
	Action string `gorm:"size:16;not null" json:"action"`
	// FromStatus / ToStatus 状态流转，便于画审核流程图。
	FromStatus QuestionStatus `gorm:"size:16;not null" json:"from_status"`
	ToStatus   QuestionStatus `gorm:"size:16;not null" json:"to_status"`

	Comment string `gorm:"size:512" json:"comment"`

	Reviewer *User `gorm:"foreignKey:ReviewerID" json:"reviewer,omitempty"`
}

func (ReviewLog) TableName() string { return "review_logs" }

// ---- 内容值对象（存进 JSON 列，不单独建表）----

// Option 选项。
//
// ID 由服务端按 (序号, 标签, 内容) 哈希生成，保证同样的输入永远得到同样的 id。
// 这一点很重要：AI 抽取与前端再编辑可能对同一道题产生完全相同的选项，
// 稳定的 id 让答案引用在往返过程中不会失效。
type Option struct {
	ID      string `json:"id"`
	Label   string `json:"label"`   // A/B/C/D
	Content JSON   `json:"content"` // RichDoc
}

// Blank 填空题的一个空位。
//
// Accepts 是"可接受答案"的分组：外层是"或"（任一答案正确即可），
// 内层是"与"（多个空按顺序对应）。例如 [["3","三"],["x=1"]] 表示
// 第一个空填 "3" 或 "三"，第二个空填 "x=1"。
type Blank struct {
	ID      string   `json:"id"` // blk_1, blk_2 ...
	Accepts [][]JSON `json:"accepts"`
}

// AnswerSpec 答案的判别联合，用 Kind 区分具体结构。
type AnswerSpec struct {
	Kind string `json:"kind"` // single | multiple | boolean | blanks | reference | unresolved

	// single / multiple
	Correct []string `json:"correct,omitempty"`

	// boolean
	BoolAnswer *bool `json:"bool_answer,omitempty"`

	// blanks
	Blanks []Blank `json:"blanks,omitempty"`

	// reference（解答题参考答案）
	Reference JSON `json:"reference,omitempty"`

	// unresolved 表示无法解析的历史答案，只读，禁止再写入。
	Raw string `json:"raw,omitempty"`
}

// QuestionDisplay 试卷中题目字段的显示开关。
// 始终规范化为四个布尔值，避免前端处理 undefined。
type QuestionDisplay struct {
	ShowAnalysis  bool `json:"show_analysis"`
	ShowSolution  bool `json:"show_solution"`
	ShowSummary   bool `json:"show_summary"`
	ShowKnowledge bool `json:"show_knowledge"`
}

// DefaultQuestionDisplay 新试卷的默认显示设置：默认展示解析，其余关闭。
func DefaultQuestionDisplay() QuestionDisplay {
	return QuestionDisplay{ShowAnalysis: true}
}

// QuestionFilter 题目列表的筛选条件。
// 放在 model 包是因为它同时被 repository 与 service 使用。
type QuestionFilter struct {
	SubjectID uint
	// Keyword 会在题干纯文本中做模糊匹配。
	Keyword string
	// Type / Status / Difficulty 为空/0 表示不过滤。
	Type       QuestionType
	Status     QuestionStatus
	Difficulty int

	KnowledgePointID uint
	// KnowledgePointID 命中时是否包含其子知识点。
	IncludeSubKnowledge bool

	TagIDs []uint
	// TagIDs 多个标签之间的关系：and（同时具备） / or（任一具备）
	TagMatch string

	CreatedBy uint
	// RootOnly 只返回独立题（不含子题）。
	RootOnly bool
	// IncludeDeleted 是否包含已软删除的题目。
	IncludeDeleted bool
}

// QuestionSort 排序方式。
type QuestionSort string

const (
	SortByCreatedDesc    QuestionSort = "created_desc"
	SortByCreatedAsc     QuestionSort = "created_asc"
	SortByUpdatedDesc    QuestionSort = "updated_desc"
	SortByDifficultyAsc  QuestionSort = "difficulty_asc"
	SortByDifficultyDesc QuestionSort = "difficulty_desc"
)

// Normalize 校验并回落默认排序。
func (s QuestionSort) Normalize() QuestionSort {
	switch s {
	case SortByCreatedAsc, SortByUpdatedDesc, SortByDifficultyAsc, SortByDifficultyDesc:
		return s
	default:
		return SortByCreatedDesc
	}
}

// OrderClause 返回 SQL 片段。集中在一处，避免把用户输入拼进 SQL。
func (s QuestionSort) OrderClause() string {
	switch s {
	case SortByCreatedAsc:
		return "questions.created_at ASC"
	case SortByUpdatedDesc:
		return "questions.updated_at DESC"
	case SortByDifficultyAsc:
		return "questions.difficulty ASC, questions.id DESC"
	case SortByDifficultyDesc:
		return "questions.difficulty DESC, questions.id DESC"
	default:
		return "questions.created_at DESC"
	}
}
