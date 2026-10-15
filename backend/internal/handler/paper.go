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

// PaperHandler 组卷相关接口。
type PaperHandler struct {
	papers    *service.PaperService
	questions *service.QuestionService
}

func NewPaperHandler(papers *service.PaperService, questions *service.QuestionService) *PaperHandler {
	return &PaperHandler{papers: papers, questions: questions}
}

// ---- 目录 ----

// FolderTree GET /subjects/:subject_id/paper-folders?scope=
func (h *PaperHandler) FolderTree(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	subjectID, ok := parseID(c, "subject_id")
	if !ok {
		return
	}
	res, err := h.papers.FolderTree(c.Request.Context(), user, subjectID, model.PaperScope(c.Query("scope")))
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// CreateFolder POST /subjects/:subject_id/paper-folders
func (h *PaperHandler) CreateFolder(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	subjectID, ok := parseID(c, "subject_id")
	if !ok {
		return
	}
	var req schema.FolderCreateRequest
	if !bind(c, &req) {
		return
	}
	res, err := h.papers.CreateFolder(c.Request.Context(), user, subjectID, &req)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.Created(c, res)
}

// UpdateFolder PUT /paper-folders/:id
func (h *PaperHandler) UpdateFolder(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var req schema.FolderUpdateRequest
	if !bind(c, &req) {
		return
	}
	res, err := h.papers.UpdateFolder(c.Request.Context(), user, id, &req)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// DeleteFolder DELETE /paper-folders/:id
func (h *PaperHandler) DeleteFolder(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	if err := h.papers.DeleteFolder(c.Request.Context(), user, id); err != nil {
		response.Fail(c, err)
		return
	}
	response.NoContent(c)
}

// ---- 试卷 ----

type paperListQuery struct {
	Scope    string `form:"scope"`
	FolderID *uint  `form:"folder_id"`
	Unfiled  bool   `form:"unfiled"`
	Keyword  string `form:"keyword"`
	Status   string `form:"status"`
	// IncludeDeleted 是否包含已软删除的试卷（回收站）。
	IncludeDeleted bool `form:"include_deleted"`
	// OnlyDeleted 只返回已软删除的试卷（回收站）。
	OnlyDeleted bool `form:"only_deleted"`
}

// List GET /subjects/:subject_id/papers
func (h *PaperHandler) List(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	subjectID, ok := parseID(c, "subject_id")
	if !ok {
		return
	}
	var q paperListQuery
	if !bindQuery(c, &q) {
		return
	}
	page := pagination.Parse(c)

	res, total, err := h.papers.ListPapers(c.Request.Context(), user, subjectID, service.ListPapersQuery{
		Scope:          model.PaperScope(q.Scope),
		FolderID:       q.FolderID,
		Unfiled:        q.Unfiled,
		Keyword:        q.Keyword,
		Status:         model.PaperStatus(q.Status),
		IncludeDeleted: q.IncludeDeleted,
		OnlyDeleted:    q.OnlyDeleted,
	}, page.Limit(), page.Offset())
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.Page(c, res, total, page.Page, page.PageSize)
}

// Create POST /subjects/:subject_id/papers
func (h *PaperHandler) Create(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	subjectID, ok := parseID(c, "subject_id")
	if !ok {
		return
	}
	var req schema.PaperCreateRequest
	if !bind(c, &req) {
		return
	}
	res, err := h.papers.CreatePaper(c.Request.Context(), user, subjectID, &req)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.Created(c, res)
}

// Get GET /papers/:id
func (h *PaperHandler) Get(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	res, err := h.papers.GetPaper(c.Request.Context(), user, id)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// Update PUT /papers/:id
func (h *PaperHandler) Update(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var req schema.PaperUpdateRequest
	if !bind(c, &req) {
		return
	}
	res, err := h.papers.UpdatePaper(c.Request.Context(), user, id, &req)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// Delete DELETE /papers/:id
func (h *PaperHandler) Delete(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	if err := h.papers.DeletePaper(c.Request.Context(), user, id); err != nil {
		response.Fail(c, err)
		return
	}
	response.NoContent(c)
}

// Restore POST /papers/:id/restore
func (h *PaperHandler) Restore(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	if err := h.papers.RestorePaper(c.Request.Context(), user, id); err != nil {
		response.Fail(c, err)
		return
	}
	response.NoContent(c)
}

// Duplicate POST /papers/:id/duplicate
func (h *PaperHandler) Duplicate(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var req struct {
		Title string `json:"title" binding:"omitempty,max=200"`
	}
	if !bind(c, &req) {
		return
	}
	res, err := h.papers.DuplicatePaper(c.Request.Context(), user, id, req.Title)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.Created(c, res)
}

// ReplaceNodes PUT /papers/:id/nodes
func (h *PaperHandler) ReplaceNodes(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var req schema.ReplaceNodesRequest
	if !bind(c, &req) {
		return
	}
	res, err := h.papers.ReplaceNodes(c.Request.Context(), user, id, &req)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// PreviewPlan POST /subjects/:subject_id/paper-plans/preview
func (h *PaperHandler) PreviewPlan(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	subjectID, ok := parseID(c, "subject_id")
	if !ok {
		return
	}
	var req schema.PaperPlanRequest
	if !bind(c, &req) {
		return
	}
	res, err := h.questions.PlanQuestions(c.Request.Context(), user, subjectID, &req)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// QuestionRevisions GET /papers/:id/question-revisions
func (h *PaperHandler) QuestionRevisions(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	res, err := h.papers.CheckQuestionRevisions(c.Request.Context(), user, id)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// SyncQuestionNodes POST /papers/:id/question-nodes/sync
func (h *PaperHandler) SyncQuestionNodes(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var req struct {
		QuestionIDs []uint `json:"question_ids"`
	}
	if !bind(c, &req) {
		return
	}
	res, err := h.papers.SyncQuestionNodes(c.Request.Context(), user, id, req.QuestionIDs)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// ---- 版本 ----

// Finalize POST /papers/:id/versions
func (h *PaperHandler) Finalize(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var req schema.FinalizeRequest
	if !bind(c, &req) {
		return
	}
	res, err := h.papers.FinalizeVersion(c.Request.Context(), user, id, &req)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.Created(c, res)
}

// ListVersions GET /papers/:id/versions
func (h *PaperHandler) ListVersions(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	res, err := h.papers.ListVersions(c.Request.Context(), user, id)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// GetVersion GET /papers/:id/versions/:version_no
func (h *PaperHandler) GetVersion(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	versionNo, ok := parseID(c, "version_no")
	if !ok {
		return
	}
	res, err := h.papers.GetVersion(c.Request.Context(), user, id, int(versionNo))
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// ---- 时间线 ----

// ListEvents GET /papers/:id/events?before_id=&limit=
func (h *PaperHandler) ListEvents(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var q struct {
		BeforeID uint `form:"before_id"`
		Limit    int  `form:"limit"`
	}
	if !bindQuery(c, &q) {
		return
	}
	res, err := h.papers.ListEvents(c.Request.Context(), user, id, q.Limit, q.BeforeID)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}
