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
// tag_ids 支持重复参数（?tag_ids=1&tag_ids=2），gin 会绑定为切片。
type questionListQuery struct {
	Keyword    string `form:"keyword"`
	Type       string `form:"type"`
	Status     string `form:"status"`
	Difficulty int    `form:"difficulty"`

	KnowledgePointID uint   `form:"knowledge_point_id"`
	IncludeSub       bool   `form:"include_sub_knowledge"`
	TagIDs           []uint `form:"tag_ids"`
	TagMatch         string `form:"tag_match"`

	CreatedBy      uint `form:"created_by"`
	RootOnly       bool `form:"root_only"`
	IncludeDeleted bool `form:"include_deleted"`
	// OnlyDeleted 只返回已软删除的题目（回收站）。与 IncludeDeleted 互斥。
	OnlyDeleted bool   `form:"only_deleted"`
	Sort        string `form:"sort"`
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
		Keyword:          q.Keyword,
		Type:             model.QuestionType(q.Type),
		Status:           model.QuestionStatus(q.Status),
		Difficulty:       q.Difficulty,
		KnowledgePointID: q.KnowledgePointID,
		IncludeSub:       q.IncludeSub,
		TagIDs:           q.TagIDs,
		TagMatch:         q.TagMatch,
		CreatedBy:        q.CreatedBy,
		RootOnly:         q.RootOnly,
		IncludeDeleted:   q.IncludeDeleted,
		OnlyDeleted:      q.OnlyDeleted,
		Sort:             model.QuestionSort(q.Sort),
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
