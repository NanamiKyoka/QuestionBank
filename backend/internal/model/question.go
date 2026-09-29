package model

import "log/slog"

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

	// Action: approve | reject | archive | restore | edit
	Action string `gorm:"size:16;not null" json:"action"`
	// FromStatus / ToStatus 状态流转，便于画审核流程图。
	FromStatus QuestionStatus `gorm:"size:16;not null" json:"from_status"`
	ToStatus   QuestionStatus `gorm:"size:16;not null" json:"to_status"`

	Comment string `gorm:"size:512" json:"comment"`

	Reviewer *User `gorm:"foreignKey:ReviewerID" json:"reviewer,omitempty"`
}

func (ReviewLog) TableName() string { return "review_logs" }

// 审核日志的 action 取值全集。
//
// 之所以用常量而不是散落在 service 里的字面量：这个列同时被
// 审核历史的界面、未来的统计报表和单题/批量两条写入路径消费，
// 一旦某条路径拼错一个字母，历史里就会多出一个人类看不懂的动作。
const (
	ReviewActionApprove = "approve"
	ReviewActionReject  = "reject"
	ReviewActionArchive = "archive"
	ReviewActionRestore = "restore"
	// ReviewActionEdit 内容被直接修改（已发布题目的勘误）。
	// 状态没有变化，因此 FromStatus == ToStatus，界面上要按 action 而非状态区分。
	ReviewActionEdit = "edit"
)

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

	// KnowledgePointIDs 由 service 解析后的知识点 id 集合
	// （含子树的展开结果），repository 只按集合过滤。
	KnowledgePointIDs []uint

	TagIDs []uint
	// TagMatch 多个标签之间的关系：and（同时具备）/ or（任一具备）
	TagMatch string

	CreatedBy uint
	// Source 来源模糊匹配（原始文件名等）。典型场景是"这批题是哪次录入带来的"。
	Source string
	// ReviewCount 精确匹配已通过的审核次数。用来定位"过了一道复核仍没发布"的题。
	//
	// 用指针而不是 int：0 本身是一个有意义的取值（"还没被任何人审过"），
	// 裸 int 的零值同时又表示"不过滤"，两者无法区分。
	ReviewCount *int
	// ReviewerID 被此人审核过（repository 走 review_logs 子查询）。
	//
	// 为什么不用 questions 表上的列：谁审过一道题是多对多关系，
	// 一次审核对应一行 review_logs 记录，凭空在题目上加一列会立刻失真。
	ReviewerID uint
	// RootOnly 只返回独立题（不含子题）。
	RootOnly bool
	// IncludeDeleted 是否包含已软删除的题目（回收站）。
	IncludeDeleted bool
	// OnlyDeleted 只返回已软删除的题目（回收站）。与 IncludeDeleted 互斥。
	//
	// 为什么需要这一个独立开关：IncludeDeleted 得到的是"活的 + 删掉的"混在一起的结果，
	// 而 SoftBase.DeletedAt 带 json:"-"，客户端拿到的题目对象上没有任何字段
	// 能区分两者——同一条题目在列表里长一个样，用户看不出哪条已经进了回收站。
	OnlyDeleted bool

	// ---- 可见性 ----
	// ViewerID 私有题判定：私有题仅创建者可见。
	ViewerID uint
	// CanViewPrivateAll 拥有 view_private_any 权限（管理员）可看他人私有题。
	CanViewPrivateAll bool
	// SubjectIDs 非 nil 时把结果限制在这些学科内；nil 表示不限制。
	SubjectIDs map[uint]bool
}

// QuestionSort 排序方式。
type QuestionSort string

const (
	SortByCreatedDesc    QuestionSort = "created_desc"
	SortByCreatedAsc     QuestionSort = "created_asc"
	SortByUpdatedDesc    QuestionSort = "updated_desc"
	SortByDifficultyAsc  QuestionSort = "difficulty_asc"
	SortByDifficultyDesc QuestionSort = "difficulty_desc"

	// SortByUpdatedAtDesc 是"按更新时间倒序"的历史命名，与 SortByUpdatedDesc
	// 语义完全相同。保留它是因为前端题库页一直发送这个值
	// （前端视图 QuestionsView.vue 的默认排序），删掉只会让它静默退化成
	// created_desc —— 一个没人会发现的错误排序。
	SortByUpdatedAtDesc QuestionSort = "updated_at_desc"
)

// validQuestionSorts 合法排序值全集。
//
// 用一张表而不是逐个 case，是为了保证"白名单"和"能落到 SQL 的取值"始终同源：
// 新增取值时忘了登记进 Normalize，编译期发现不了，而线上表现是静默回落。
var validQuestionSorts = map[QuestionSort]bool{
	SortByCreatedDesc:    true,
	SortByCreatedAsc:     true,
	SortByUpdatedDesc:    true,
	SortByUpdatedAtDesc:  true,
	SortByDifficultyAsc:  true,
	SortByDifficultyDesc: true,
}

// Valid 是否为受支持的排序方式。
func (s QuestionSort) Valid() bool { return validQuestionSorts[s] }

// Normalize 校验并回落默认排序。
//
// 回落必须**可观测**：题库页一直声明按"更新时间"排序、实际始终是创建时间倒序，
// 就是早期这里静默回落造成的——既没有报错也没有日志，排查时除了翻代码没有线索。
// 因此非法取值一律打一条 warn 日志（带上用户实际传的值），让它能被检索到。
func (s QuestionSort) Normalize() QuestionSort {
	if validQuestionSorts[s] {
		return s
	}
	// 空前串是"没传排序参数"，属于正常路径，不必刷日志。
	if s != "" {
		slog.Warn("题目排序参数不合法，已回落到默认排序",
			"requested", string(s),
			"fallback", string(SortByCreatedDesc))
	}
	return SortByCreatedDesc
}

// OrderClause 返回 SQL 片段。集中在一处，避免把用户输入拼进 SQL。
func (s QuestionSort) OrderClause() string {
	switch s {
	case SortByCreatedAsc:
		return "questions.created_at ASC"
	case SortByUpdatedDesc, SortByUpdatedAtDesc:
		return "questions.updated_at DESC"
	case SortByDifficultyAsc:
		return "questions.difficulty ASC, questions.id DESC"
	case SortByDifficultyDesc:
		return "questions.difficulty DESC, questions.id DESC"
	default:
		return "questions.created_at DESC"
	}
}
