package handler

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/NanamiKyoka/questionmind/internal/middleware"
	"github.com/NanamiKyoka/questionmind/internal/pkg/response"
	"github.com/NanamiKyoka/questionmind/internal/schema"
	"github.com/NanamiKyoka/questionmind/internal/service"
)

// TagHandler 标签与标签分类接口。
type TagHandler struct {
	tags *service.TagService
}

func NewTagHandler(tags *service.TagService) *TagHandler { return &TagHandler{tags: tags} }

// ---- 分类 ----

// ListCategories GET /subjects/:subject_id/tag-categories
func (h *TagHandler) ListCategories(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	subjectID, ok := parseID(c, "subject_id")
	if !ok {
		return
	}
	res, err := h.tags.ListCategories(c.Request.Context(), user, subjectID)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// CreateCategory POST /subjects/:subject_id/tag-categories
func (h *TagHandler) CreateCategory(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	subjectID, ok := parseID(c, "subject_id")
	if !ok {
		return
	}
	var req schema.TagCategoryCreateRequest
	if !bind(c, &req) {
		return
	}
	res, err := h.tags.CreateCategory(c.Request.Context(), user, subjectID, &req)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.Created(c, res)
}

// UpdateCategory PUT /tag-categories/:id
func (h *TagHandler) UpdateCategory(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var req schema.TagCategoryUpdateRequest
	if !bind(c, &req) {
		return
	}
	res, err := h.tags.UpdateCategory(c.Request.Context(), user, id, &req)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// DeleteCategory DELETE /tag-categories/:id
func (h *TagHandler) DeleteCategory(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	if err := h.tags.DeleteCategory(c.Request.Context(), user, id); err != nil {
		response.Fail(c, err)
		return
	}
	response.NoContent(c)
}

// ---- 标签 ----

// List GET /subjects/:subject_id/tags?category_id=
func (h *TagHandler) List(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	subjectID, ok := parseID(c, "subject_id")
	if !ok {
		return
	}

	var categoryID *uint
	if raw := c.Query("category_id"); raw != "" {
		n, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			response.FailWith(c, 40001, "category_id 必须是数字")
			return
		}
		v := uint(n)
		categoryID = &v
	}

	res, err := h.tags.List(c.Request.Context(), user, subjectID, categoryID)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// Create POST /subjects/:subject_id/tags
func (h *TagHandler) Create(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	subjectID, ok := parseID(c, "subject_id")
	if !ok {
		return
	}
	var req schema.TagCreateRequest
	if !bind(c, &req) {
		return
	}
	res, err := h.tags.Create(c.Request.Context(), user, subjectID, &req)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.Created(c, res)
}

// Update PUT /tags/:id
func (h *TagHandler) Update(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var req schema.TagUpdateRequest
	if !bind(c, &req) {
		return
	}
	res, err := h.tags.Update(c.Request.Context(), user, id, &req)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// Delete DELETE /tags/:id
func (h *TagHandler) Delete(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	if err := h.tags.Delete(c.Request.Context(), user, id); err != nil {
		response.Fail(c, err)
		return
	}
	response.NoContent(c)
}
