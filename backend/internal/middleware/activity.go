package middleware

import (
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/service"
)

// ActivityRecorder 审计记录接收方。
//
// 定义成中间件本地的窄接口，而不是直接依赖 service.ActivityService：
// 这样 middleware 与 service 互不依赖，避免出现循环引用。
type ActivityRecorder interface {
	Record(entry *model.ActivityLog)
}

// ActivityLogger 记录写操作的审计日志。
//
// 只记 POST / PUT / PATCH / DELETE —— 读操作数量大、价值低，
// 记下来只会把日志表撑爆，反而让真正有用的记录被淹没。
//
// 幂等接口（如重新索引）也会被记录，因为它们确实改变了状态。
func ActivityLogger(recorder ActivityRecorder, skipPrefixes []string) gin.HandlerFunc {
	return func(c *gin.Context) {
		started := time.Now()

		c.Next()

		method := c.Request.Method
		if method != "POST" && method != "PUT" && method != "PATCH" && method != "DELETE" {
			return
		}
		path := c.Request.URL.Path
		for _, prefix := range skipPrefixes {
			if strings.HasPrefix(path, prefix) {
				return
			}
		}

		// FullPath 返回路由**模式**（如 /api/v1/questions/:id），
		// 用它推断动作名比从实际路径里猜哪一段是 id 可靠得多。
		fullPath := c.FullPath()
		if fullPath == "" {
			return
		}
		action, resource := service.DeriveAction(fullPath, method)

		status := c.Writer.Status()
		entry := &model.ActivityLog{
			Action:     action,
			Resource:   resource,
			ResourceID: resourceIDOf(c, fullPath),
			Method:     method,
			Path:       path,
			IP:         c.ClientIP(),
			UserAgent:  c.Request.UserAgent(),
			StatusCode: status,
			DurationMs: int(time.Since(started).Milliseconds()),
			// 4xx 算"业务被拒绝"，不是系统故障。审计里区分这两类，
			// 排障时才不会把权限错误当成服务异常。
			Success: status < 400,
		}

		if user := CurrentUser(c); user != nil {
			uid := user.ID
			entry.UserID = &uid
			entry.Username = user.Username
		}

		if RequestIDOf(c) != "" {
			entry.Detail = model.MustJSONOf(map[string]any{
				"request_id": RequestIDOf(c),
			})
		}

		recorder.Record(entry)
	}
}

// resourceIDOf 从实际路径里提取资源 id。
//
// 路由模式里的 :id 是占位符，实际值在 URL.Path 里。做法是把模式按段拆开，
// 找到 :param 所在的下标，再取实际路径对应段的值。
// 这样 "/api/v1/questions/:id" 与 "/api/v1/questions/42" 能对上。
func resourceIDOf(c *gin.Context, fullPath string) string {
	patternParts := strings.Split(strings.Trim(fullPath, "/"), "/")
	actualParts := strings.Split(strings.Trim(c.Request.URL.Path, "/"), "/")

	for i, p := range patternParts {
		if !strings.HasPrefix(p, ":") {
			continue
		}
		if i < len(actualParts) {
			value := actualParts[i]
			// id 必须像数字，避免把 :subject_id 之类取到非数字值。
			if _, err := strconv.ParseUint(value, 10, 64); err == nil {
				return value
			}
		}
	}
	return ""
}
