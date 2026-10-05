// Package middleware 提供 HTTP 中间件：请求 ID、panic 恢复、访问日志、跨域、
// 鉴权与权限门禁。
//
// 中间件的顺序很重要，router 中的注册顺序即为执行顺序：
//
//	RequestID → Recovery → AccessLog → CORS → Auth → RequireAuth → 业务处理
//
// 其中 Auth 只负责"有 token 就解析出用户"，不做拦截；
// 是否需要登录由各路由组自己决定（如 /health、/auth/login 就不需要）。
package middleware

import (
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
	"github.com/NanamiKyoka/questionmind/internal/pkg/response"
)

// HeaderRequestID 请求 ID 的响应头名称。
const HeaderRequestID = "X-Request-Id"

const ctxRequestIDKey = "qm.request_id"

// RequestID 为每个请求生成唯一 ID，便于把一次请求的多条日志串起来。
// 若上游（如 nginx）已经传了，则沿用。
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		rid := c.GetHeader(HeaderRequestID)
		if rid == "" {
			rid = uuid.NewString()
		}
		c.Set(ctxRequestIDKey, rid)
		c.Header(HeaderRequestID, rid)
		c.Next()
	}
}

// RequestIDOf 取出当前请求 ID。
func RequestIDOf(c *gin.Context) string {
	if v, ok := c.Get(ctxRequestIDKey); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// Recovery 捕获 panic，返回统一的 500 响应而不是断开连接。
//
// 与 gin.Recovery() 的区别：输出结构化的 JSON 错误体，前端无需特殊处理。
func Recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("请求处理发生 panic",
					"request_id", RequestIDOf(c),
					"path", c.Request.URL.Path,
					"panic", r,
				)
				response.Fail(c, apperr.ErrInternal)
			}
		}()
		c.Next()
	}
}

// AccessLog 记录访问日志。
//
// 只记录必要字段；请求体与响应体一律不记，避免把密码、API Key 写进日志。
// query 里的 `token=` 必须脱敏：导出/预览这类"浏览器直接打开"的请求
// 会把访问令牌拼在 URL 上，原样落盘等于把 7 天有效的账号接管权
// 交给任何能读日志的人（运维、共享机器、被轮转归档的副本）。
func AccessLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		query := redactQuery(c.Request.URL.RawQuery)

		c.Next()

		elapsed := time.Since(start)
		status := c.Writer.Status()

		attrs := []any{
			"request_id", RequestIDOf(c),
			"method", c.Request.Method,
			"path", path,
			"status", status,
			"duration_ms", elapsed.Milliseconds(),
			"ip", c.ClientIP(),
			"size", c.Writer.Size(),
		}
		if query != "" {
			attrs = append(attrs, "query", query)
		}
		if len(c.Errors) > 0 {
			attrs = append(attrs, "errors", c.Errors.String())
		}

		switch {
		case status >= 500:
			slog.Error("请求失败", attrs...)
		case status >= 400:
			slog.Warn("请求异常", attrs...)
		case elapsed > 500*time.Millisecond:
			slog.Warn("慢请求", attrs...)
		default:
			slog.Info("请求完成", attrs...)
		}
	}
}

// redactedQuery 参数名列表：值绝不允许出现在日志里。
var redactedQueryParams = map[string]bool{
	"token":        true,
	"access_token": true,
	"api_key":      true,
	"apikey":       true,
	"password":     true,
}

// redactQuery 把敏感参数的值替换成 ***，其余原样保留。
//
// 逐个解析而不是正则替换：query 里可能有转义过的 `%26token%3D`，
// 正则会误伤正常的路径参数，而 url.ParseQuery 保证只动真正的键值对。
// 解析失败时宁可整段丢弃，也不能把原始串漏出去。
func redactQuery(raw string) string {
	if raw == "" {
		return ""
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return "[unreadable]"
	}
	changed := false
	for key := range values {
		if redactedQueryParams[strings.ToLower(key)] {
			values.Set(key, "***")
			changed = true
		}
	}
	if !changed {
		return raw
	}
	return values.Encode()
}

// CORS 跨域支持。
//
// 生产部署下前端与后端同源（前端静态资源由同一服务或 nginx 提供），
// 因此默认不开放跨域。只有在显式配置了允许来源时才启用，
// 且必须精确匹配——不使用通配符 *，因为接口带凭证。
func CORS(allowOrigins []string) gin.HandlerFunc {
	allowed := make(map[string]bool, len(allowOrigins))
	for _, o := range allowOrigins {
		allowed[strings.TrimRight(o, "/")] = true
	}

	return func(c *gin.Context) {
		origin := strings.TrimRight(c.GetHeader("Origin"), "/")
		if origin != "" && allowed[origin] {
			h := c.Writer.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Credentials", "true")
			h.Set("Access-Control-Allow-Methods", "GET,POST,PUT,PATCH,DELETE,OPTIONS")
			h.Set("Access-Control-Allow-Headers", "Content-Type,Authorization,"+HeaderRequestID)
			h.Set("Access-Control-Expose-Headers", HeaderRequestID)
			h.Set("Access-Control-Max-Age", "86400")
			// 响应的 Vary 头必不可少：否则中间缓存可能把 A 站的响应喂给 B 站。
			h.Add("Vary", "Origin")
		}

		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// NoCache 禁止缓存接口响应（鉴权接口与实时数据必须禁用缓存）。
func NoCache() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store, no-cache, must-revalidate")
		c.Header("Pragma", "no-cache")
		c.Next()
	}
}
