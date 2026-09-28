package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/NanamiKyoka/questionmind/internal/middleware"
	"github.com/NanamiKyoka/questionmind/internal/pkg/response"
	"github.com/NanamiKyoka/questionmind/internal/service"
)

// VectorHandler 知识点向量索引与语义检索接口。
type VectorHandler struct {
	vectors *service.VectorService
}

func NewVectorHandler(vectors *service.VectorService) *VectorHandler {
	return &VectorHandler{vectors: vectors}
}

// Status GET /subjects/:subject_id/knowledge-points/vector-status
func (h *VectorHandler) Status(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	subjectID, ok := parseID(c, "subject_id")
	if !ok {
		return
	}
	res, err := h.vectors.Status(c.Request.Context(), user, subjectID)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// Reindex POST /subjects/:subject_id/knowledge-points/reindex
func (h *VectorHandler) Reindex(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	subjectID, ok := parseID(c, "subject_id")
	if !ok {
		return
	}
	var req struct {
		// Force 为真时全部重算，用于换了向量模型之后。
		Force bool `json:"force"`
	}
	if !bind(c, &req) {
		return
	}
	res, err := h.vectors.Reindex(c.Request.Context(), user, subjectID, req.Force)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// Search POST /subjects/:subject_id/knowledge-points/search
func (h *VectorHandler) Search(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	subjectID, ok := parseID(c, "subject_id")
	if !ok {
		return
	}
	var req struct {
		Query string `json:"query" binding:"required,max=2000"`
		TopK  int    `json:"top_k" binding:"omitempty,min=1,max=50"`
	}
	if !bind(c, &req) {
		return
	}
	res, err := h.vectors.Search(c.Request.Context(), user, subjectID, req.Query, req.TopK)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}
