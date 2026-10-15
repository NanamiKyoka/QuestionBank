package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/NanamiKyoka/questionmind/internal/middleware"
	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/pagination"
	"github.com/NanamiKyoka/questionmind/internal/pkg/response"
	"github.com/NanamiKyoka/questionmind/internal/schema"
	"github.com/NanamiKyoka/questionmind/internal/service"
)

// QuestionHandler 题库接口。
type QuestionHandler struct {
	questions *service.QuestionService
}

func NewQuestionHandler(questions *service.QuestionService) *QuestionHandler {
	return &QuestionHandler{questions: questions}
}

// questionListQuery 列表查询参数。
// knowledge_point_ids / tag_ids 支持重复参数（?knowledge_point_ids=1&knowledge_point_ids=2），gin 会绑定为切片。
type questionListQuery struct {
	Keyword    string `form:"keyword"`
	Type       string `form:"type"`
	Status     string `form:"status"`
	Difficulty int    `form:"difficulty"`

	// KnowledgePointIDs 多选。KnowledgePointID 是它的单值别名，
	// 两个都留着是为了不破坏已有的调用方（导出、AI 工具、参考实现照搬的脚本）。
	KnowledgePointIDs []uint `form:"knowledge_point_ids"`
	KnowledgePointID  uint   `form:"knowledge_point_id"`
	IncludeSub        bool   `form:"include_sub_knowledge"`
	TagIDs            []uint `form:"tag_ids"`
	TagMatch          string `form:"tag_match"`

	CreatedBy uint   `form:"created_by"`
	Source    string `form:"source"`
	// ReviewCount 用指针：0 是"还没被审过"，与"不过滤"必须区分开。
	ReviewCount *int `form:"review_count"`
	// ReviewerID 被此人审核过。
	ReviewerID     uint `form:"reviewer_id"`
	RootOnly       bool `form:"root_only"`
	IncludeDeleted bool `form:"include_deleted"`
	// OnlyDeleted 只返回已软删除的题目（回收站）。与 IncludeDeleted 互斥。
	OnlyDeleted bool   `form:"only_deleted"`
	Sort        string `form:"sort"`
}

// knowledgePointIDs 合并多选参数与单值别名。
func (q questionListQuery) knowledgePointIDs() []uint {
	if len(q.KnowledgePointIDs) > 0 {
		return q.KnowledgePointIDs
	}
	if q.KnowledgePointID > 0 {
		return []uint{q.KnowledgePointID}
	}
	return nil
}

// List GET /subjects/:subject_id/questions
func (h *QuestionHandler) List(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	subjectID, ok := parseID(c, "subject_id")
	if !ok {
		return
	}

	var q questionListQuery
	if !bindQuery(c, &q) {
		return
	}
	page := pagination.Parse(c)

	res, total, err := h.questions.List(c.Request.Context(), user, subjectID, service.ListQuery{
		Keyword:           q.Keyword,
		Type:              model.QuestionType(q.Type),
		Status:            model.QuestionStatus(q.Status),
		Difficulty:        q.Difficulty,
		KnowledgePointIDs: q.knowledgePointIDs(),
		IncludeSub:        q.IncludeSub,
		TagIDs:            q.TagIDs,
		TagMatch:          q.TagMatch,
		CreatedBy:         q.CreatedBy,
		Source:            q.Source,
		ReviewCount:       q.ReviewCount,
		ReviewerID:        q.ReviewerID,
		RootOnly:          q.RootOnly,
		IncludeDeleted:    q.IncludeDeleted,
		OnlyDeleted:       q.OnlyDeleted,
		Sort:              model.QuestionSort(q.Sort),
	}, page.Limit(), page.Offset())
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.Page(c, res, total, page.Page, page.PageSize)
}

// Create POST /subjects/:subject_id/questions
func (h *QuestionHandler) Create(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	subjectID, ok := parseID(c, "subject_id")
	if !ok {
		return
	}
	var req schema.QuestionCreateRequest
	if !bind(c, &req) {
		return
	}
	// 路径中的学科为准，避免请求体里的 subject_id 与路径不一致时产生歧义。
	req.SubjectID = subjectID

	res, err := h.questions.Create(c.Request.Context(), user, &req)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.Created(c, res)
}

// Get GET /questions/:id
func (h *QuestionHandler) Get(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	res, err := h.questions.Get(c.Request.Context(), user, id)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// BatchRead POST /questions/batch-read
func (h *QuestionHandler) BatchRead(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	var req schema.QuestionBatchReadRequest
	if !bind(c, &req) {
		return
	}
	res, err := h.questions.BatchGet(c.Request.Context(), user, req.IDs)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// Update PUT /questions/:id
func (h *QuestionHandler) Update(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var req schema.QuestionUpdateRequest
	if !bind(c, &req) {
		return
	}
	res, err := h.questions.Update(c.Request.Context(), user, id, &req)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// BatchDelete POST /questions/batch-delete
func (h *QuestionHandler) BatchDelete(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	var req schema.QuestionBatchDeleteRequest
	if !bind(c, &req) {
		return
	}
	deleted, skipped, err := h.questions.Delete(c.Request.Context(), user, req.IDs)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, gin.H{
		"deleted_count": len(deleted),
		"skipped_count": len(skipped),
		"skipped_ids":   skipped,
	})
}

// Restore POST /questions/:id/restore
func (h *QuestionHandler) Restore(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	if err := h.questions.Restore(c.Request.Context(), user, id); err != nil {
		response.Fail(c, err)
		return
	}
	response.NoContent(c)
}

// Submit POST /questions/:id/submit
func (h *QuestionHandler) Submit(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	res, err := h.questions.Submit(c.Request.Context(), user, id)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// Review POST /questions/:id/review
func (h *QuestionHandler) Review(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var req schema.ReviewRequest
	if !bind(c, &req) {
		return
	}
	res, err := h.questions.Review(c.Request.Context(), user, id, &req)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// ReviewLogs GET /questions/:id/review-logs
func (h *QuestionHandler) ReviewLogs(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	res, err := h.questions.ReviewLogs(c.Request.Context(), user, id, 50)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// BatchUpdateSource POST /questions/batch-source
func (h *QuestionHandler) BatchUpdateSource(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	var req schema.QuestionBatchUpdateRequest
	if !bind(c, &req) {
		return
	}
	updated, skipped, err := h.questions.BatchUpdateSource(c.Request.Context(), user, req.IDs, req.Source)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, gin.H{
		"updated_count": updated,
		"skipped_ids":   skipped,
	})
}

// BatchReview POST /questions/batch-review
func (h *QuestionHandler) BatchReview(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	var req schema.QuestionBatchReviewRequest
	if !bind(c, &req) {
		return
	}
	handled, skipped, err := h.questions.BatchReview(c.Request.Context(), user, req.IDs, req.Action, req.Comment)
	if err != nil {
		response.Fail(c, err)
		return
	}
	// 批量接口的常态是"部分成功"：混进来的已发布题、别人学科的题都会被跳过。
	// 因此如实回报两个数量，而不是编一个成功/失败二选一的结论。
	response.OK(c, gin.H{
		"handled_count": len(handled),
		"skipped_count": len(skipped),
		"skipped_ids":   skipped,
	})
}

// Archive POST /questions/:id/archive
func (h *QuestionHandler) Archive(c *gin.Context) {
	h.setArchived(c, true)
}

// Unarchive POST /questions/:id/unarchive
func (h *QuestionHandler) Unarchive(c *gin.Context) {
	h.setArchived(c, false)
}

func (h *QuestionHandler) setArchived(c *gin.Context, archived bool) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	// 允许空 body：归档不是必须写理由的操作，强制一个请求体会让
	// "一键下架"这种最高频的调用多出一段无意义的样板。
	var req schema.QuestionArchiveRequest
	if c.Request.ContentLength > 0 {
		if !bind(c, &req) {
			return
		}
	}
	res, err := h.questions.SetArchived(c.Request.Context(), user, id, archived, req.Comment)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// Stats GET /subjects/:subject_id/questions/stats
func (h *QuestionHandler) Stats(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	subjectID, ok := parseID(c, "subject_id")
	if !ok {
		return
	}
	res, err := h.questions.Stats(c.Request.Context(), user, subjectID)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}
