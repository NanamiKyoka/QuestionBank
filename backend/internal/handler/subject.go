package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/NanamiKyoka/questionmind/internal/middleware"
	"github.com/NanamiKyoka/questionmind/internal/pkg/response"
	"github.com/NanamiKyoka/questionmind/internal/schema"
	"github.com/NanamiKyoka/questionmind/internal/service"
)

// SubjectHandler 学科与成员接口。
type SubjectHandler struct {
	subjects *service.SubjectService
}

func NewSubjectHandler(subjects *service.SubjectService) *SubjectHandler {
	return &SubjectHandler{subjects: subjects}
}

// List GET /subjects
func (h *SubjectHandler) List(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	res, err := h.subjects.List(c.Request.Context(), user)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// Create POST /subjects
func (h *SubjectHandler) Create(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	var req schema.SubjectCreateRequest
	if !bind(c, &req) {
		return
	}
	res, err := h.subjects.Create(c.Request.Context(), user, &req)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.Created(c, res)
}

// Get GET /subjects/:subject_id
func (h *SubjectHandler) Get(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "subject_id")
	if !ok {
		return
	}
	res, err := h.subjects.Get(c.Request.Context(), user, id)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// Update PUT /subjects/:subject_id
func (h *SubjectHandler) Update(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "subject_id")
	if !ok {
		return
	}
	var req schema.SubjectUpdateRequest
	if !bind(c, &req) {
		return
	}
	res, err := h.subjects.Update(c.Request.Context(), user, id, &req)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// Delete DELETE /subjects/:subject_id
func (h *SubjectHandler) Delete(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "subject_id")
	if !ok {
		return
	}
	if err := h.subjects.Delete(c.Request.Context(), user, id); err != nil {
		response.Fail(c, err)
		return
	}
	response.NoContent(c)
}

// ListMembers GET /subjects/:subject_id/members
func (h *SubjectHandler) ListMembers(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "subject_id")
	if !ok {
		return
	}
	res, err := h.subjects.ListMembers(c.Request.Context(), user, id)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// UpsertMember PUT /subjects/:subject_id/members/:user_id
func (h *SubjectHandler) UpsertMember(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	subjectID, ok := parseID(c, "subject_id")
	if !ok {
		return
	}
	var req schema.MemberUpsertRequest
	if !bind(c, &req) {
		return
	}
	// 路径里的 user_id 优先于请求体，避免两者不一致时产生歧义。
	if uid, ok := parseID(c, "user_id"); ok {
		req.UserID = uid
	}
	if err := h.subjects.UpsertMember(c.Request.Context(), user, subjectID, &req); err != nil {
		response.Fail(c, err)
		return
	}
	response.NoContent(c)
}

// RemoveMember DELETE /subjects/:subject_id/members/:user_id
func (h *SubjectHandler) RemoveMember(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	subjectID, ok := parseID(c, "subject_id")
	if !ok {
		return
	}
	userID, ok := parseID(c, "user_id")
	if !ok {
		return
	}
	if err := h.subjects.RemoveMember(c.Request.Context(), user, subjectID, userID); err != nil {
		response.Fail(c, err)
		return
	}
	response.NoContent(c)
}
