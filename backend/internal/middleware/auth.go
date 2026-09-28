package middleware

import (
	"context"
	"errors"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
	"github.com/NanamiKyoka/questionmind/internal/pkg/jwtx"
	"github.com/NanamiKyoka/questionmind/internal/pkg/response"
)

const ctxUserKey = "qm.current_user"

// UserLoader 由 repository 实现，避免中间件直接依赖具体的数据访问实现
// （也避免了 middleware ↔ repository 的潜在循环依赖）。
type UserLoader interface {
	// GetWithMemberships 必须一并加载学科成员关系。
	// 权限判定是纯内存计算，若不带出成员关系就会退化成 N+1 查询。
	GetWithMemberships(ctx context.Context, id uint) (*model.User, error)
}

// Auth 解析 Authorization: Bearer <token>，成功则把用户放入上下文。
//
// 这里刻意**不**在解析失败时中断请求：
//   - 没有 token：匿名访问，交给 RequireAuth 决定是否放行；
//   - token 无效/过期：同样不中断，但记录原因，供 RequireAuth 给出精确提示。
//
// 这样 /questions（匿名可看公开题）与 /questions（需登录才能改）可以共用一条链。
func Auth(loader UserLoader, tokens *jwtx.Manager) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw := extractToken(c)
		if raw == "" {
			c.Next()
			return
		}

		userID, err := tokens.Parse(raw)
		if err != nil {
			// 过期与无效区分开：前端对"过期"应跳登录页，对"无效"可提示重登。
			if errors.Is(err, jwtx.ErrExpired) {
				c.Set(ctxAuthErrKey, apperr.ErrTokenExpired)
			} else {
				c.Set(ctxAuthErrKey, apperr.ErrUnauthorized)
			}
			c.Next()
			return
		}

		user, err := loader.GetWithMemberships(c.Request.Context(), userID)
		if err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				response.Fail(c, apperr.ErrInternal.WithCause(err))
				return
			}
			c.Set(ctxAuthErrKey, apperr.ErrUnauthorized)
			c.Next()
			return
		}

		// 被禁用的账号立即失效，即使它持有未过期的 token。
		if !user.IsActive {
			c.Set(ctxAuthErrKey, apperr.New(apperr.CodeAccountInactive, "账号已被禁用"))
			c.Next()
			return
		}

		c.Set(ctxUserKey, user)
		c.Next()
	}
}

const ctxAuthErrKey = "qm.auth_err"

// extractToken 从 Authorization 头或 cookie 中取出 token。
//
// 同时支持两种载体：前端 SPA 用 Header（无状态、天然免疫 CSRF），
// 而预览/下载这类"浏览器直接发起"的请求只带得上 cookie。
func extractToken(c *gin.Context) string {
	if h := c.GetHeader("Authorization"); h != "" {
		if after, ok := strings.CutPrefix(h, "Bearer "); ok {
			return strings.TrimSpace(after)
		}
	}
	if v, err := c.Cookie("token"); err == nil {
		return v
	}
	if q := c.Query("token"); q != "" {
		// 仅用于"新窗口打开导出文件"这类无法设置请求头的场景。
		return q
	}
	return ""
}

// CurrentUser 取出当前用户，未登录返回 nil。
func CurrentUser(c *gin.Context) *model.User {
	if v, ok := c.Get(ctxUserKey); ok {
		if u, ok := v.(*model.User); ok {
			return u
		}
	}
	return nil
}

// MustUser 取出当前用户；未登录时写出错误响应并返回 false。
// handler 中统一写成：
//
//	user, ok := middleware.MustUser(c)
//	if !ok { return }
func MustUser(c *gin.Context) (*model.User, bool) {
	u := CurrentUser(c)
	if u == nil {
		FailAuth(c)
		return nil, false
	}
	return u, true
}

// FailAuth 输出鉴权失败。优先使用 Auth 中间件记录的精确原因（如"已过期"）。
func FailAuth(c *gin.Context) {
	if v, ok := c.Get(ctxAuthErrKey); ok {
		if e, ok := v.(*apperr.Error); ok {
			response.Fail(c, e)
			return
		}
	}
	response.Fail(c, apperr.ErrUnauthorized)
}

// RequireAuth 强制登录。
func RequireAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		if CurrentUser(c) == nil {
			FailAuth(c)
			return
		}
		c.Next()
	}
}

// RequireSuperuser 强制全局管理员。
func RequireSuperuser() gin.HandlerFunc {
	return func(c *gin.Context) {
		u, ok := MustUser(c)
		if !ok {
			return
		}
		if !u.IsSuperuser {
			response.Fail(c, apperr.ErrForbidden)
			return
		}
		c.Next()
	}
}

// SubjectIDFunc 从请求中解析学科 ID。
// 各路由的取法不同（路径参数 / 查询参数 / 请求体），因此由调用方提供。
type SubjectIDFunc func(c *gin.Context) uint

// RequirePermission 学科级权限门禁。
//
// 这是"接口级"的第一道门；"数据级"的可见性（公开/私有）由 service 层负责。
// 两道门都有才完整：前者防越权操作，后者防越权查看。
func RequirePermission(perm model.Permission, subjectID SubjectIDFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		u, ok := MustUser(c)
		if !ok {
			return
		}

		var sid uint
		if subjectID != nil {
			sid = subjectID(c)
		}
		if sid == 0 && !u.IsSuperuser {
			// 无法确定学科就无从判权，按拒绝处理（fail-closed）。
			response.Fail(c, apperr.ErrForbidden.WithDetail("无法确定学科作用域"))
			return
		}

		if !u.Can(perm, sid) {
			response.Fail(c, apperr.ErrForbidden)
			return
		}
		c.Next()
	}
}

// SubjectIDFromPath 从路径参数 :subject_id 取学科 ID。
func SubjectIDFromPath(c *gin.Context) uint {
	return parseUint(c.Param("subject_id"))
}

// SubjectIDFromQuery 从查询参数 subject_id 取学科 ID。
func SubjectIDFromQuery(c *gin.Context) uint {
	return parseUint(c.Query("subject_id"))
}

func parseUint(s string) uint {
	if s == "" {
		return 0
	}
	var n uint64
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return 0
		}
		n = n*10 + uint64(ch-'0')
		if n > 1<<53 {
			return 0
		}
	}
	return uint(n)
}
