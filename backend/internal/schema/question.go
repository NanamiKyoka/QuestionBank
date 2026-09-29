package schema

import (
	"time"

	"github.com/NanamiKyoka/questionmind/internal/model"
)

// ---- 写入 ----

// OptionInput 选项输入。
type OptionInput struct {
	Label   string     `json:"label" binding:"required,max=8"`
	Content model.JSON `json:"content"`
}

// QuestionCreateRequest 新建题目。
type QuestionCreateRequest struct {
	// SubjectID 由路由路径提供，handler 在绑定后覆写，因此不参与请求体校验。
	SubjectID uint  `json:"subject_id"`
	ParentID  *uint `json:"parent_id"`

	QuestionType model.QuestionType `json:"question_type" binding:"required"`
	Difficulty   *int               `json:"difficulty" binding:"omitempty,min=1,max=5"`
	Visibility   model.Visibility   `json:"visibility" binding:"omitempty,oneof=public private"`

	Stem     model.JSON       `json:"stem" binding:"required"`
	Options  []OptionInput    `json:"options"`
	Answer   model.AnswerSpec `json:"answer"`
	Analysis model.JSON       `json:"analysis"`
	Solution model.JSON       `json:"solution"`
	Summary  model.JSON       `json:"summary"`

	TagIDs            []uint `json:"tag_ids"`
	KnowledgePointIDs []uint `json:"knowledge_point_ids"`

	Source string `json:"source" binding:"omitempty,max=255"`
	// ImportTaskID 溯源：这批题目来自哪一次导入。
	//
	// 由智能录题的入库流程填写，用来回答"这道题是哪次导入带进来的"。
	// ⚠️ 当前的落库动作由导入用例在入库之后统一回填
	//（repository.QuestionRepo.SetImportTask），因为题目必须先建出来才有主键；
	// 本字段属于接口契约，QuestionService.Create 直接采纳它之后即可删掉那次回填。
	ImportTaskID *uint `json:"import_task_id"`
	// Status 仅管理员可指定；普通用户创建时一律为草稿。
	Status model.QuestionStatus `json:"status" binding:"omitempty"`
}

// QuestionUpdateRequest 更新题目。未传的字段保持不变。
type QuestionUpdateRequest struct {
	// ParentID 改挂母题。
	//
	// 用 **uint 区分三种情形：nil（不动）、指向一题（改挂为子题）、
	// 指向 0（提升为独立题）。裸指针表达不出"解除挂载"，
	// 于是界面上就没法把一道子题从母题下摘出来。
	ParentID *uint `json:"parent_id"`

	QuestionType *model.QuestionType `json:"question_type"`
	Difficulty   *int                `json:"difficulty" binding:"omitempty,min=1,max=5"`
	Visibility   *model.Visibility   `json:"visibility" binding:"omitempty,oneof=public private"`

	Stem     model.JSON        `json:"stem"`
	Options  *[]OptionInput    `json:"options"`
	Answer   *model.AnswerSpec `json:"answer"`
	Analysis model.JSON        `json:"analysis"`
	Solution model.JSON        `json:"solution"`
	Summary  model.JSON        `json:"summary"`

	// 用指针区分"没传"与"传了空数组"：后者表示清空全部关联。
	TagIDs            *[]uint `json:"tag_ids"`
	KnowledgePointIDs *[]uint `json:"knowledge_point_ids"`

	Source *string `json:"source" binding:"omitempty,max=255"`

	// ChangeNote 变更说明，写入审核日志与操作时间线。
	ChangeNote string `json:"change_note" binding:"omitempty,max=255"`
}

// QuestionBatchDeleteRequest 批量删除。
type QuestionBatchDeleteRequest struct {
	IDs []uint `json:"ids" binding:"required,min=1,max=200"`
}

// QuestionBatchUpdateRequest 批量改来源。
type QuestionBatchUpdateRequest struct {
	IDs    []uint `json:"ids" binding:"required,min=1,max=200"`
	Source string `json:"source" binding:"required,max=255"`
}

// QuestionBatchReviewRequest 批量提交审核 / 批量通过 / 批量驳回。
//
// Action 与单题的 Submit / Review 保持同一套语义：
//
//	submit  草稿 → 待审（需要 edit_question）
//	approve 待审 → 通过一次，达标即发布（需要 review_question，学科阈值照常生效）
//	reject  待审 → 退回草稿并清零审核次数（需要 review_question）
type QuestionBatchReviewRequest struct {
	IDs    []uint `json:"ids" binding:"required,min=1,max=200"`
	Action string `json:"action" binding:"required,oneof=submit approve reject"`
	// Comment 驳回原因。批量驳回时整批共用同一条原因。
	Comment string `json:"comment" binding:"omitempty,max=512"`
}

// QuestionArchiveRequest 归档 / 取消归档。
type QuestionArchiveRequest struct {
	// Comment 归档理由，写入审核日志。取消归档时可留空。
	Comment string `json:"comment" binding:"omitempty,max=512"`
}

// ReviewRequest 审核。
type ReviewRequest struct {
	// Action: approve | reject
	Action  string `json:"action" binding:"required,oneof=approve reject"`
	Comment string `json:"comment" binding:"omitempty,max=512"`
}

// ---- 读取 ----

// TagBrief 题目标签的简要信息。
type TagBrief struct {
	ID    uint   `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color"`
}

// KnowledgePointBrief 题目知识点的简要信息。
type KnowledgePointBrief struct {
	ID   uint   `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
}

// QuestionBrief 列表项。
//
// 刻意不返回 stem 的富文本、选项与答案：列表一页可能有几十条，
// 带上这些字段会让响应体膨胀几十倍，而列表页只需要展示题干纯文本。
// 这是从参考实现"列表查询放大"问题中吸取的教训。
type QuestionBrief struct {
	ID                uint                 `json:"id"`
	SubjectID         uint                 `json:"subject_id"`
	ParentID          *uint                `json:"parent_id"`
	QuestionType      model.QuestionType   `json:"question_type"`
	QuestionTypeLabel string               `json:"question_type_label"`
	Difficulty        int                  `json:"difficulty"`
	Status            model.QuestionStatus `json:"status"`
	StatusLabel       string               `json:"status_label"`
	Visibility        model.Visibility     `json:"visibility"`

	// StemText 题干纯文本，列表展示与搜索命中高亮使用。
	StemText string `json:"stem_text"`

	Tags            []TagBrief            `json:"tags"`
	KnowledgePoints []KnowledgePointBrief `json:"knowledge_points"`

	ChildCount int `json:"child_count"`

	// ReviewCount 已通过的审核次数。列表页要靠它显示「还差几次复核」，
	// 否则审核者只能逐题点开详情才能知道离学科阈值还差多少。
	ReviewCount int `json:"review_count"`

	Source    string    `json:"source"`
	CreatedBy uint      `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// QuestionDetail 详情，包含完整内容。
type QuestionDetail struct {
	QuestionBrief

	Stem     model.JSON       `json:"stem"`
	Options  []model.Option   `json:"options"`
	Answer   model.AnswerSpec `json:"answer"`
	Analysis model.JSON       `json:"analysis"`
	Solution model.JSON       `json:"solution"`
	Summary  model.JSON       `json:"summary"`

	ContentVersion  int   `json:"content_version"`
	ContentRevision int64 `json:"content_revision"`
	ReviewCount     int   `json:"review_count"`

	// Children 子题（母子题结构）。只带简要信息，避免无限递归。
	Children []QuestionBrief `json:"children"`

	// Permissions 当前用户对该题的操作权限，前端据此控制按钮。
	Permissions QuestionPermissions `json:"permissions"`
}

// QuestionPermissions 前端按钮显隐依据。
type QuestionPermissions struct {
	CanEdit   bool `json:"can_edit"`
	CanDelete bool `json:"can_delete"`
	CanReview bool `json:"can_review"`
}

// ReviewLogResponse 审核日志。
type ReviewLogResponse struct {
	ID         uint                 `json:"id"`
	Action     string               `json:"action"`
	FromStatus model.QuestionStatus `json:"from_status"`
	ToStatus   model.QuestionStatus `json:"to_status"`
	Comment    string               `json:"comment"`
	ReviewerID uint                 `json:"reviewer_id"`
	Reviewer   string               `json:"reviewer"`
	CreatedAt  time.Time            `json:"created_at"`
}

// NewTagBriefs 转换标签简要信息。
func NewTagBriefs(tags []model.Tag) []TagBrief {
	out := make([]TagBrief, 0, len(tags))
	for i := range tags {
		out = append(out, TagBrief{ID: tags[i].ID, Name: tags[i].Name, Color: tags[i].Color})
	}
	return out
}

// NewKnowledgePointBriefs 转换知识点简要信息。
func NewKnowledgePointBriefs(kps []model.KnowledgePoint) []KnowledgePointBrief {
	out := make([]KnowledgePointBrief, 0, len(kps))
	for i := range kps {
		out = append(out, KnowledgePointBrief{ID: kps[i].ID, Name: kps[i].Name, Path: kps[i].Path})
	}
	return out
}

// NewQuestionBrief 从模型构造列表项。tags/kps 由调用方批量装载后传入。
func NewQuestionBrief(q *model.Question, tags []model.Tag, kps []model.KnowledgePoint, childCount int) QuestionBrief {
	return QuestionBrief{
		ID:                q.ID,
		SubjectID:         q.SubjectID,
		ParentID:          q.ParentID,
		QuestionType:      q.QuestionType,
		QuestionTypeLabel: q.QuestionType.Label(),
		Difficulty:        q.Difficulty,
		Status:            q.Status,
		StatusLabel:       q.Status.Label(),
		Visibility:        q.Visibility,
		StemText:          q.StemText,
		Tags:              NewTagBriefs(tags),
		KnowledgePoints:   NewKnowledgePointBriefs(kps),
		ChildCount:        childCount,
		ReviewCount:       q.ReviewCount,
		Source:            q.Source,
		CreatedBy:         q.CreatedBy,
		CreatedAt:         q.CreatedAt,
		UpdatedAt:         q.UpdatedAt,
	}
}
