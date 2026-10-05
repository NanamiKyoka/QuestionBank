package handler

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/NanamiKyoka/questionmind/internal/middleware"
	"github.com/NanamiKyoka/questionmind/internal/pkg/response"
	"github.com/NanamiKyoka/questionmind/internal/schema"
	"github.com/NanamiKyoka/questionmind/internal/service"
)

// AuthHandler 认证与账号相关接口。
type AuthHandler struct {
	auth *service.AuthService
	// tokenTTL 用于设置 cookie 的 Max-Age，与 JWT 有效期保持一致。
	tokenTTL time.Duration
	// secureCookie 生产环境（HTTPS）下应开启：cookie 只经加密通道传输，
	// 且设为 HttpOnly 防止 XSS 窃取。本地 HTTP 开发环境必须关闭，
	// 否则浏览器根本不会保存该 cookie。
	secureCookie bool
}

// NewAuthHandler 构造。
func NewAuthHandler(auth *service.AuthService, tokenTTL time.Duration, secureCookie bool) *AuthHandler {
	return &AuthHandler{auth: auth, tokenTTL: tokenTTL, secureCookie: secureCookie}
}

// SetupStatus GET /system/setup-status
// 公开接口：前端在任何时候都要能问"系统装好了吗"。
func (h *AuthHandler) SetupStatus(c *gin.Context) {
	res, err := h.auth.SetupStatus(c.Request.Context())
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// Setup POST /auth/setup
// 创建第一个管理员。系统已初始化时返回 403。
func (h *AuthHandler) Setup(c *gin.Context) {
	var req schema.SetupRequest
	if !bind(c, &req) {
		return
	}
	res, err := h.auth.Setup(c.Request.Context(), &req)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.Created(c, res)
}

// Login POST /auth/login
func (h *AuthHandler) Login(c *gin.Context) {
	var req schema.LoginRequest
	if !bind(c, &req) {
		return
	}
	res, err := h.auth.Login(c.Request.Context(), &req)
	if err != nil {
		response.Fail(c, err)
		return
	}

	// 同时下发 cookie：预览/下载这类"浏览器直接打开"的请求无法设置请求头，
	// 只能靠 cookie 携带凭证。HttpOnly/Secure 由部署环境决定（见结构体注释）。
	//
	// SameSite=Lax 显式写出来：跨站 POST 不带凭证，而顶部导航（在新窗口
	// 打开导出文件）仍然带——Lax 正是这两种行为的分界，不该靠浏览器默认值。
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie("token", res.AccessToken, int(h.tokenTTL.Seconds()), "/", "", h.secureCookie, h.secureCookie)

	response.OK(c, res)
}

// Logout POST /auth/logout
// 无状态 JWT 的"登出"就是清掉 cookie；客户端还需自行丢弃 token。
func (h *AuthHandler) Logout(c *gin.Context) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie("token", "", -1, "/", "", h.secureCookie, h.secureCookie)
	response.NoContent(c)
}

// Me GET /auth/me
func (h *AuthHandler) Me(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	res, err := h.auth.Me(c.Request.Context(), user.ID)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// UpdateMe PATCH /auth/me
func (h *AuthHandler) UpdateMe(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	var req schema.UpdateMeRequest
	if !bind(c, &req) {
		return
	}
	res, err := h.auth.UpdateMe(c.Request.Context(), user.ID, &req)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// ChangePassword POST /auth/me/password
func (h *AuthHandler) ChangePassword(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	var req schema.ChangePasswordRequest
	if !bind(c, &req) {
		return
	}
	if err := h.auth.ChangePassword(c.Request.Context(), user.ID, &req); err != nil {
		response.Fail(c, err)
		return
	}
	response.NoContent(c)
}

// UpdateLastSubject POST /auth/me/last-subject
func (h *AuthHandler) UpdateLastSubject(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	var req schema.UpdateLastSubjectRequest
	if !bind(c, &req) {
		return
	}
	if err := h.auth.UpdateLastSubject(c.Request.Context(), user.ID, req.SubjectID); err != nil {
		response.Fail(c, err)
		return
	}
	response.NoContent(c)
}
