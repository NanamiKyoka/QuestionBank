package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/NanamiKyoka/questionmind/internal/middleware"
	"github.com/NanamiKyoka/questionmind/internal/pkg/response"
	"github.com/NanamiKyoka/questionmind/internal/schema"
	"github.com/NanamiKyoka/questionmind/internal/service"
)

// KnowledgePointHandler 知识点接口。
type KnowledgePointHandler struct {
	kps *service.KnowledgePointService
}

func NewKnowledgePointHandler(kps *service.KnowledgePointService) *KnowledgePointHandler {
	return &KnowledgePointHandler{kps: kps}
}

// Tree GET /subjects/:subject_id/knowledge-points
func (h *KnowledgePointHandler) Tree(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	subjectID, ok := parseID(c, "subject_id")
	if !ok {
		return
	}
	res, err := h.kps.Tree(c.Request.Context(), user, subjectID)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// Create POST /subjects/:subject_id/knowledge-points
func (h *KnowledgePointHandler) Create(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	subjectID, ok := parseID(c, "subject_id")
	if !ok {
		return
	}
	var req schema.KnowledgePointCreateRequest
	if !bind(c, &req) {
		return
	}
	res, err := h.kps.Create(c.Request.Context(), user, subjectID, &req)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.Created(c, res)
}

// Update PUT /knowledge-points/:id
func (h *KnowledgePointHandler) Update(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var req schema.KnowledgePointUpdateRequest
	if !bind(c, &req) {
		return
	}
	res, err := h.kps.Update(c.Request.Context(), user, id, &req)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// Move PATCH /knowledge-points/:id/move
func (h *KnowledgePointHandler) Move(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var req schema.KnowledgePointMoveRequest
	if !bind(c, &req) {
		return
	}
	res, err := h.kps.Move(c.Request.Context(), user, id, &req)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// Delete DELETE /knowledge-points/:id
func (h *KnowledgePointHandler) Delete(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	if err := h.kps.Delete(c.Request.Context(), user, id); err != nil {
		response.Fail(c, err)
		return
	}
	response.NoContent(c)
}
