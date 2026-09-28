package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/NanamiKyoka/questionmind/internal/middleware"
	"github.com/NanamiKyoka/questionmind/internal/pkg/pagination"
	"github.com/NanamiKyoka/questionmind/internal/pkg/response"
	"github.com/NanamiKyoka/questionmind/internal/schema"
	"github.com/NanamiKyoka/questionmind/internal/service"
)

// UserHandler 用户管理与权限查询接口。
type UserHandler struct {
	users *service.UserService
}

func NewUserHandler(users *service.UserService) *UserHandler { return &UserHandler{users: users} }

// Permissions GET /auth/me/permissions
//
// 前端把它当作权限判定的唯一真相源，因此任何登录用户都要能调。
func (h *UserHandler) Permissions(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	res, err := h.users.Permissions(c.Request.Context(), user.ID)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// List GET /users
func (h *UserHandler) List(c *gin.Context) {
	actor, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	var q schema.UserListQuery
	if !bindQuery(c, &q) {
		return
	}
	page := pagination.Parse(c)

	res, total, err := h.users.List(c.Request.Context(), actor, q, page.Limit(), page.Offset())
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.Page(c, res, total, page.Page, page.PageSize)
}

// Get GET /users/:id
func (h *UserHandler) Get(c *gin.Context) {
	actor, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	res, err := h.users.Get(c.Request.Context(), actor, id)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// Create POST /users
func (h *UserHandler) Create(c *gin.Context) {
	actor, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	var req schema.UserCreateRequest
	if !bind(c, &req) {
		return
	}
	res, err := h.users.Create(c.Request.Context(), actor, &req)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.Created(c, res)
}

// Update PUT /users/:id
func (h *UserHandler) Update(c *gin.Context) {
	actor, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var req schema.UserAdminUpdateRequest
	if !bind(c, &req) {
		return
	}
	res, err := h.users.Update(c.Request.Context(), actor, id, &req)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// Delete DELETE /users/:id
func (h *UserHandler) Delete(c *gin.Context) {
	actor, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	if err := h.users.Delete(c.Request.Context(), actor, id); err != nil {
		response.Fail(c, err)
		return
	}
	response.NoContent(c)
}

// ResetPassword POST /users/:id/password
func (h *UserHandler) ResetPassword(c *gin.Context) {
	actor, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var req schema.ResetPasswordRequest
	if !bind(c, &req) {
		return
	}
	if err := h.users.ResetPassword(c.Request.Context(), actor, id, &req); err != nil {
		response.Fail(c, err)
		return
	}
	response.NoContent(c)
}
