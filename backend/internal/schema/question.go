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
	// Status 仅管理员可指定；普通用户创建时一律为草稿。
	Status model.QuestionStatus `json:"status" binding:"omitempty"`
}

// QuestionUpdateRequest 更新题目。未传的字段保持不变。
type QuestionUpdateRequest struct {
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
		Source:            q.Source,
		CreatedBy:         q.CreatedBy,
		CreatedAt:         q.CreatedAt,
		UpdatedAt:         q.UpdatedAt,
	}
}
