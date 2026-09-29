package model

import "time"

// PaperFolder 试卷目录，按学科 + 空间组织的树。
type PaperFolder struct {
	SoftBase

	SubjectID uint       `gorm:"not null;index:idx_folder_scope,priority:1" json:"subject_id"`
	Scope     PaperScope `gorm:"size:16;not null;index:idx_folder_scope,priority:2" json:"scope"`
	// OwnerID 仅 personal 空间有值；shared 空间为 NULL。
	// 归属由 (subject_id, scope, owner_id) 三者共同确定，不存在独立的"空间表"。
	OwnerID  *uint `gorm:"index:idx_folder_scope,priority:3" json:"owner_id"`
	ParentID *uint `gorm:"index" json:"parent_id"`

	Name      string `gorm:"size:128;not null" json:"name"`
	SortOrder int    `gorm:"not null;default:0" json:"sort_order"`
	CreatedBy uint   `gorm:"not null" json:"created_by"`
}

func (PaperFolder) TableName() string { return "paper_folders" }

// Paper 试卷（稿件）。
//
// 并发控制：revision 字段配合条件更新实现乐观锁。
// 自动保存的请求带上客户端最后看到的 revision，服务端执行
//
//	UPDATE papers SET ..., revision = revision + 1 WHERE id = ? AND revision = ?
//
// 影响行数为 0 即表示期间有人改过，返回 409，由前端保留本地编辑并提示冲突。
// 这样不需要悲观锁，也不需要长事务。
type Paper struct {
	SoftBase

	SubjectID uint       `gorm:"not null;index:idx_paper_scope,priority:1" json:"subject_id"`
	Scope     PaperScope `gorm:"size:16;not null;index:idx_paper_scope,priority:2" json:"scope"`
	OwnerID   *uint      `gorm:"index:idx_paper_scope,priority:3" json:"owner_id"`
	FolderID  *uint      `gorm:"index" json:"folder_id"`

	Title       string `gorm:"size:200;not null" json:"title"`
	Description string `gorm:"size:512" json:"description"`

	Status PaperStatus `gorm:"size:16;not null;default:draft;index" json:"status"`

	// ---- 排版选项 ----
	// NumberingEnabled 是否自动编号（一、二、三… 与 1. 2. 3.…）
	NumberingEnabled bool `gorm:"not null;default:true" json:"numbering_enabled"`
	// ScoringEnabled 是否显示与统计分值
	ScoringEnabled bool `gorm:"not null;default:false" json:"scoring_enabled"`
	// QuestionDisplay 题目下展示哪些附加字段
	QuestionDisplay JSON `json:"question_display"`
	// TotalScore 分值合计（ScoringEnabled 为真时由服务端重算）
	TotalScore float64 `gorm:"not null;default:0" json:"total_score"`

	// Revision 乐观锁版本号，每次节点树或元信息变更 +1。
	Revision int64 `gorm:"not null;default:1" json:"revision"`
	// FinalizedVersion 最后一次定稿的版本号（0 表示从未定稿）。
	FinalizedVersion int `gorm:"not null;default:0" json:"finalized_version"`

	CreatedBy uint `gorm:"not null;index" json:"created_by"`
	UpdatedBy uint `json:"updated_by"`
}

func (Paper) TableName() string { return "papers" }

// PaperNode 排版节点——组卷工作台的核心数据结构。
//
// 设计为两层 AST：
//
//	root(block: rich_text / heading / question / page_break / answer_space)
//	  └── module(question_details)          ← 解析模块，可选
//	        └── reference(answer_item)      ← 单题解析，由服务端按规则派生
//
// 关键决策：
//  1. 主键是**客户端生成的 UUID**。自动保存是"整棵树覆盖"，
//     如果 id 由服务端生成，每次保存 id 都会变，前端的脏检查、
//     AI 改动的差异对比、以及 anchor 软指针全部失效。
//  2. position 由服务端按请求顺序重新分配，客户端不传，
//     避免出现重复或空洞导致排序不稳定。
//  3. source_question_node_id / anchor_before_node_id 是**软指针**
//     （刻意不加外键）：它们指向同一棵树的其它节点，硬外键会让
//     整树替换变得极其难写，而整树替换正是自动保存的实现方式。
type PaperNode struct {
	// ID 客户端生成的 UUID（36 字符）。
	ID string `gorm:"size:36;primaryKey" json:"id"`

	PaperID uint `gorm:"not null;index:idx_node_paper_pos,priority:1" json:"paper_id"`
	// ParentID 自引用；非空表示它是某个 module 的子节点。
	ParentID *string `gorm:"size:36;index" json:"parent_id"`

	// Position 同级内的排序序号，从 0 开始。
	Position int `gorm:"not null;index:idx_node_paper_pos,priority:2" json:"position"`

	NodeKind NodeKind `gorm:"size:16;not null" json:"node_kind"`
	NodeType NodeType `gorm:"size:32;not null" json:"node_type"`

	// Content 节点内容（如题目块存 question_id、标题存文本、富文本存 RichDoc）
	Content JSON `json:"content"`
	// Props 节点属性（如分值、是否显示题号、答题区高度）
	Props JSON `json:"props"`

	// ---- 题目块的冻结内容（服务端维护，客户端不参与往返）----
	//
	// 试卷是**稿件**，不是题库的一个视图。题库里改了题干、删了题目、
	// 调了答案，已经排好的卷子不应该跟着变——否则上周定稿发给学生的卷子
	// 今天再导出就变成了另一个样子。因此题目块在加入卷子时把当时的题目
	// 内容冻结一份，之后一直用这份；题库那边改了，只在界面上提示
	// 「可同步最新内容」，由用户决定要不要覆盖。
	//
	// 这两列刻意**不进 Content**：Content 是客户端提交的契约，混进
	// 服务端维护的字段会让"客户端能伪造冻结内容"变成可能。
	QuestionID *uint `gorm:"index" json:"question_id,omitempty"`
	// QuestionContent 冻结的题目内容投影（结构同 PaperQuestionProjection）。
	// 为空表示尚未冻结（历史数据或题目已删），此时退回按 id 实时读取。
	QuestionContent JSON `json:"question_content,omitempty"`

	// SchemaVersion 节点 schema 版本，前端据此决定是否允许空块等情况，
	// 必须原样往返，不能丢。
	SchemaVersion int `gorm:"not null;default:1" json:"schema_version"`

	// SourceQuestionNodeID 指向它所对应的题目块节点，用于解析条目与题目配对。
	SourceQuestionNodeID *string `gorm:"size:36" json:"source_question_node_id"`
	// AnchorBeforeNodeID 解析条目的挂载锚点：插在该题目块的解析之后。
	AnchorBeforeNodeID *string `gorm:"size:36" json:"anchor_before_node_id"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (PaperNode) TableName() string { return "paper_nodes" }

// PaperVersion 定稿快照。
//
// 快照是**不可变**的：定稿时把整棵节点树按前序展开、连同题目内容的投影
// 一起冻结下来。这样即使题库里的题目后来被改动或删除，历史版本依然可读、可导出。
type PaperVersion struct {
	Base

	PaperID   uint `gorm:"not null;uniqueIndex:uk_paper_version,priority:1" json:"paper_id"`
	VersionNo int  `gorm:"not null;uniqueIndex:uk_paper_version,priority:2" json:"version_no"`

	Title string `gorm:"size:200;not null" json:"title"`
	Note  string `gorm:"size:512" json:"note"`

	// Snapshot 冻结的文档：{nodes:[...], questions:{id:投影}, options:{...}}
	Snapshot JSON `json:"snapshot"`
	// SchemaVersion 快照格式版本，用于将来迁移旧快照。
	SchemaVersion int `gorm:"not null;default:1" json:"schema_version"`

	CreatedBy uint `gorm:"not null" json:"created_by"`
}

func (PaperVersion) TableName() string { return "paper_versions" }

// PaperEvent 操作时间线。记录"谁在什么时候对这份卷子做了什么"。
type PaperEvent struct {
	Base

	PaperID uint `gorm:"not null;index" json:"paper_id"`

	// EventType: created | updated | moved | deleted | restored | duplicated |
	//            nodes_replaced | questions_synced | finalized
	EventType string `gorm:"size:32;not null" json:"event_type"`

	ActorID uint `gorm:"not null" json:"actor_id"`
	// Summary 人可读的一句话描述，直接展示在时间线上。
	Summary string `gorm:"size:255" json:"summary"`
	// Payload 结构化细节（改了哪些节点等），供前端展开查看。
	Payload JSON `json:"payload,omitempty"`

	Actor *User `gorm:"foreignKey:ActorID" json:"actor,omitempty"`
}

func (PaperEvent) TableName() string { return "paper_events" }
