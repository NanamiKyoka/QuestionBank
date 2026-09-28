package model

// 本文件集中定义所有枚举值。用字符串而非整数存储：
//   - 数据库里可读，排查问题不用查文档；
//   - 新增取值无需担心顺序错乱；
//   - 与前端 TS 联合类型天然对齐。
//
// 每个枚举都提供 Valid()，服务层在写入前必须校验。

// QuestionType 题型。
type QuestionType string

const (
	QuestionTypeSingleChoice   QuestionType = "single_choice"   // 单选
	QuestionTypeMultipleChoice QuestionType = "multiple_choice" // 多选
	QuestionTypeFillBlank      QuestionType = "fill_blank"      // 填空
	QuestionTypeTrueFalse      QuestionType = "true_false"      // 判断
	QuestionTypeEssay          QuestionType = "essay"           // 解答
)

// QuestionTypes 全部合法题型，供参数校验使用。
var QuestionTypes = []QuestionType{
	QuestionTypeSingleChoice,
	QuestionTypeMultipleChoice,
	QuestionTypeFillBlank,
	QuestionTypeTrueFalse,
	QuestionTypeEssay,
}

func (t QuestionType) Valid() bool {
	for _, v := range QuestionTypes {
		if v == t {
			return true
		}
	}
	return false
}

// HasOptions 该题型是否带选项。
func (t QuestionType) HasOptions() bool {
	return t == QuestionTypeSingleChoice || t == QuestionTypeMultipleChoice
}

// Label 中文名，用于导出与统计。
func (t QuestionType) Label() string {
	switch t {
	case QuestionTypeSingleChoice:
		return "单选题"
	case QuestionTypeMultipleChoice:
		return "多选题"
	case QuestionTypeFillBlank:
		return "填空题"
	case QuestionTypeTrueFalse:
		return "判断题"
	case QuestionTypeEssay:
		return "解答题"
	}
	return string(t)
}

// QuestionStatus 题目状态。
type QuestionStatus string

const (
	QuestionStatusDraft     QuestionStatus = "draft"     // 草稿
	QuestionStatusPending   QuestionStatus = "pending"   // 待审
	QuestionStatusPublished QuestionStatus = "published" // 已发布
	QuestionStatusArchived  QuestionStatus = "archived"  // 已归档
)

var QuestionStatuses = []QuestionStatus{
	QuestionStatusDraft,
	QuestionStatusPending,
	QuestionStatusPublished,
	QuestionStatusArchived,
}

func (s QuestionStatus) Valid() bool {
	for _, v := range QuestionStatuses {
		if v == s {
			return true
		}
	}
	return false
}

func (s QuestionStatus) Label() string {
	switch s {
	case QuestionStatusDraft:
		return "草稿"
	case QuestionStatusPending:
		return "待审核"
	case QuestionStatusPublished:
		return "已发布"
	case QuestionStatusArchived:
		return "已归档"
	}
	return string(s)
}

// Visibility 可见性。私有题目仅创建者与管理员可见。
type Visibility string

const (
	VisibilityPublic  Visibility = "public"
	VisibilityPrivate Visibility = "private"
)

func (v Visibility) Valid() bool {
	return v == VisibilityPublic || v == VisibilityPrivate
}

// SubjectRole 学科作用域角色。
//
// 权限判定采用「动作权限」而非「比较角色大小」：
// 调用处问 can(user, PermEditQuestion, subjectID)，角色到权限的映射集中在
// Permission 表里。将来要支持自定义角色，只改映射即可。
type SubjectRole string

const (
	SubjectRoleViewer  SubjectRole = "viewer"  // 只读
	SubjectRoleEditor  SubjectRole = "editor"  // 可编辑题目
	SubjectRoleManager SubjectRole = "manager" // 学科负责人
)

var SubjectRoles = []SubjectRole{SubjectRoleViewer, SubjectRoleEditor, SubjectRoleManager}

func (r SubjectRole) Valid() bool {
	for _, v := range SubjectRoles {
		if v == r {
			return true
		}
	}
	return false
}

func (r SubjectRole) Label() string {
	switch r {
	case SubjectRoleViewer:
		return "只读"
	case SubjectRoleEditor:
		return "编辑"
	case SubjectRoleManager:
		return "学科负责人"
	}
	return string(r)
}

// Permission 动作权限。
type Permission string

const (
	PermViewQuestion   Permission = "view_question"
	PermEditQuestion   Permission = "edit_question"
	PermReviewQuestion Permission = "review_question"
	PermManageSubject  Permission = "manage_subject" // 改学科配置/知识点/标签
	PermManageMembers  Permission = "manage_members" // 分配学科成员
	PermViewPrivateAny Permission = "view_private_any"
)

// rolePermissions 唯一的角色→权限映射表。
//
// 三个角色刻意保持职责分离：录入（editor）与审核（manager）不是同一个人。
// 如果把 review_question 也给 editor，那么最常见的编辑角色录入的题会直接发布，
// 审核流程就被架空了——所以这里只给 manager。
var rolePermissions = map[SubjectRole]map[Permission]bool{
	SubjectRoleViewer: {
		PermViewQuestion: true,
	},
	SubjectRoleEditor: {
		PermViewQuestion: true,
		PermEditQuestion: true,
	},
	SubjectRoleManager: {
		PermViewQuestion:   true,
		PermEditQuestion:   true,
		PermReviewQuestion: true,
		PermManageSubject:  true,
		PermManageMembers:  true,
	},
}

// PermissionsOf 返回某角色拥有的权限集合。
func PermissionsOf(role SubjectRole) map[Permission]bool {
	return rolePermissions[role]
}

// PaperScope 试卷所属空间。
//
// personal 空间归属某个用户；shared 属全学科共有。
// 由 (subject_id, scope, owner_id) 三者共同确定归属。
type PaperScope string

const (
	PaperScopePersonal PaperScope = "personal"
	PaperScopeShared   PaperScope = "shared"
)

func (s PaperScope) Valid() bool {
	return s == PaperScopePersonal || s == PaperScopeShared
}

// PaperStatus 试卷状态。
type PaperStatus string

const (
	PaperStatusDraft     PaperStatus = "draft"
	PaperStatusFinalized PaperStatus = "finalized"
	PaperStatusArchived  PaperStatus = "archived"
)

func (s PaperStatus) Valid() bool {
	return s == PaperStatusDraft || s == PaperStatusFinalized || s == PaperStatusArchived
}

// NodeKind 排版节点类别（两层 AST 的第一层分类）。
type NodeKind string

const (
	NodeKindBlock     NodeKind = "block"     // 顶层块
	NodeKindModule    NodeKind = "module"    // 解析模块（其子节点由服务端派生）
	NodeKindReference NodeKind = "reference" // 引用（如答案条目）
)

// NodeType 排版节点类型。
type NodeType string

const (
	NodeTypeRichText        NodeType = "rich_text"        // 富文本段落/说明
	NodeTypeHeading         NodeType = "heading"          // 标题（如"一、选择题"）
	NodeTypeQuestion        NodeType = "question"         // 题目引用块
	NodeTypePageBreak       NodeType = "page_break"       // 分页符
	NodeTypeAnswerSpace     NodeType = "answer_space"     // 答题空白区
	NodeTypeQuestionDetails NodeType = "question_details" // 解析模块（module）
	NodeTypeAnswerItem      NodeType = "answer_item"      // 单题解析（reference）
)

// nodeKindOf 类型 → 类别。用于校验 kind 与 type 是否自洽。
var nodeKindOf = map[NodeType]NodeKind{
	NodeTypeRichText:        NodeKindBlock,
	NodeTypeHeading:         NodeKindBlock,
	NodeTypeQuestion:        NodeKindBlock,
	NodeTypePageBreak:       NodeKindBlock,
	NodeTypeAnswerSpace:     NodeKindBlock,
	NodeTypeQuestionDetails: NodeKindModule,
	NodeTypeAnswerItem:      NodeKindReference,
}

// KindOf 返回节点类型对应的类别。
func KindOf(t NodeType) (NodeKind, bool) {
	k, ok := nodeKindOf[t]
	return k, ok
}

// NodeTypes 全部合法节点类型。
var NodeTypes = []NodeType{
	NodeTypeRichText, NodeTypeHeading, NodeTypeQuestion, NodeTypePageBreak,
	NodeTypeAnswerSpace, NodeTypeQuestionDetails, NodeTypeAnswerItem,
}

// ImportTaskStatus 导入任务状态机：
//
//	pending → processing → completed
//	                     ↘ failed
//	pending → cancelled
type ImportTaskStatus string

const (
	ImportStatusPending    ImportTaskStatus = "pending"
	ImportStatusProcessing ImportTaskStatus = "processing"
	ImportStatusCompleted  ImportTaskStatus = "completed"
	ImportStatusFailed     ImportTaskStatus = "failed"
	ImportStatusCancelled  ImportTaskStatus = "cancelled"
)

// ImportSource 导入来源，用于区分同步审计记录与真正的后台任务。
type ImportSource string

const (
	ImportSourceManual   ImportSource = "manual" // 前端手工录入
	ImportSourceUpload   ImportSource = "upload" // 后台上传解析任务
	ImportSourcePaper    ImportSource = "paper"  // 整卷导入
	ImportSourceAIAssist ImportSource = "ai_assist"
)

// AIInterfaceType AI 供应商接口协议。
type AIInterfaceType string

const (
	AIInterfaceOpenAI AIInterfaceType = "openai" // OpenAI 兼容（含 DeepSeek/通义/智谱等）
	AIInterfaceGemini AIInterfaceType = "gemini"
)

// AIModelType 模型用途。
type AIModelType string

const (
	AIModelText      AIModelType = "text"
	AIModelVision    AIModelType = "vision"
	AIModelEmbedding AIModelType = "embedding"
)

// AgentRunStatus 助手单次运行状态。
type AgentRunStatus string

const (
	AgentRunRunning AgentRunStatus = "running"
	AgentRunDone    AgentRunStatus = "done"
	AgentRunFailed  AgentRunStatus = "failed"
)

// ChatRole 消息角色。
type ChatRole string

const (
	ChatRoleSystem    ChatRole = "system"
	ChatRoleUser      ChatRole = "user"
	ChatRoleAssistant ChatRole = "assistant"
	ChatRoleTool      ChatRole = "tool"
)

// SettingKey 系统设置的键。集中定义避免各处硬编码字符串。
const (
	SettingAIExtractPrompt  = "ai_extract_prompt"
	SettingAISolvePrompt    = "ai_solve_prompt"
	SettingRegistrationOpen = "registration_enabled"
	SettingReviewThreshold  = "default_review_threshold"

	// 当前生效的模型。存 model id 而不是模型名：模型名可能重复，
	// 而 id 能唯一定位到"哪个供应商的哪个模型"。
	SettingAITextModelID      = "ai_text_model_id"
	SettingAIVisionModelID    = "ai_vision_model_id"
	SettingAIEmbeddingModelID = "ai_embedding_model_id"
)

// SettingAIKeys 全部 AI 相关设置键，用于批量读取。
var SettingAIKeys = []string{
	SettingAITextModelID,
	SettingAIVisionModelID,
	SettingAIEmbeddingModelID,
}
