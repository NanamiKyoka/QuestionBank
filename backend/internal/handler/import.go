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

// ImportHandler 智能录题接口。
type ImportHandler struct {
	imports *service.ImportService
}

func NewImportHandler(imports *service.ImportService) *ImportHandler {
	return &ImportHandler{imports: imports}
}

// Extract POST /subjects/:subject_id/imports/extract
//
// 返回草稿而不是直接入库：模型识别题目总会有出入，
// 必须经过人工审核这一步，否则错误答案会直接污染题库。
func (h *ImportHandler) Extract(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	subjectID, ok := parseID(c, "subject_id")
	if !ok {
		return
	}
	var req schema.ExtractRequest
	if !bind(c, &req) {
		return
	}
	res, err := h.imports.Extract(c.Request.Context(), user, subjectID, &req)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// Commit POST /subjects/:subject_id/imports/commit
func (h *ImportHandler) Commit(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	subjectID, ok := parseID(c, "subject_id")
	if !ok {
		return
	}
	var req schema.CommitRequest
	if !bind(c, &req) {
		return
	}
	res, err := h.imports.Commit(c.Request.Context(), user, subjectID, &req)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// ListTasks GET /subjects/:subject_id/imports
func (h *ImportHandler) ListTasks(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	subjectID, ok := parseID(c, "subject_id")
	if !ok {
		return
	}
	var q struct {
		Status string `form:"status"`
	}
	if !bindQuery(c, &q) {
		return
	}
	page := pagination.Parse(c)

	res, total, err := h.imports.ListTasks(c.Request.Context(), user, subjectID,
		model.ImportTaskStatus(q.Status), page.Limit(), page.Offset())
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.Page(c, res, total, page.Page, page.PageSize)
}
