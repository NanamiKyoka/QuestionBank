package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/NanamiKyoka/questionmind/internal/model"
)

// 导入任务的五条新路由必须能同时注册在一棵路由树上。
//
// 为什么要专门测它：`queue-status` 是静态段，`/:task_id/cancel` 是通配段，
// 两者出现在同一层。gin 的路由树对"静态段与通配段并存"有历史限制，
// 一旦冲突，panic 发生在**进程启动时**而不是请求时——
// 也就是说，等到有人访问到才发现的话，服务根本起不来。
// 这个测试把那条失败提前到 CI。
func TestImportTaskRoutesRegister(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	h := NewImportHandler(nil)

	imports := r.Group("/api/v1/subjects/:subject_id/imports")
	{
		imports.POST("/extract", h.Extract)
		imports.POST("/commit", h.Commit)
		imports.GET("", h.ListTasks)

		imports.POST("/:task_id/cancel", h.CancelTask)
		imports.POST("/:task_id/retry", h.RetryTask)
		imports.DELETE("/:task_id", h.DeleteTask)
		imports.GET("/queue-status", h.QueueStatus)
		imports.GET("/:task_id/questions", h.TaskQuestions)
	}

	// 注册成功还不够：静态段与通配段并存时，
	// gin 可能把 /queue-status 解析到 :task_id 上（值为 "queue-status"），
	// 那会让队列面板在 parseID 上直接 400。
	const prefix = "/api/v1/subjects/:subject_id/imports"
	routes := map[string]bool{
		prefix:                         false,
		prefix + "/extract":            false,
		prefix + "/commit":             false,
		prefix + "/queue-status":       false,
		prefix + "/:task_id/cancel":    false,
		prefix + "/:task_id/retry":     false,
		prefix + "/:task_id":           false,
		prefix + "/:task_id/questions": false,
	}
	for _, ri := range r.Routes() {
		if _, ok := routes[ri.Path]; ok {
			routes[ri.Path] = true
		}
	}
	for path, found := range routes {
		if !found {
			t.Fatalf("路由未注册成功: %s", path)
		}
	}
}

// queue-status 必须命中静态路由，而不是被 :task_id 吃掉。
//
// 注入一个已登录用户，让请求真正走到 handler 里：
// 命中静态路由 → 进入 QueueStatus → service 为 nil 而 panic（被 recovery 兜成 500）；
// 被 :task_id 吃掉 → 先在 parseID("task_id") 上因为 "queue-status" 不是数字而返回 400。
// 两种结果截然不同，足以区分路由到底打到了谁。
func TestQueueStatusIsNotSwallowedByTaskWildcard(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(gin.Recovery())
	// ctxUserKey 在 middleware 包内未导出，但它的值是稳定的字符串常量，
	// 这里直接按同样的键注入一个用户。
	r.Use(func(c *gin.Context) {
		c.Set("qm.current_user", &model.User{Base: model.Base{ID: 1}, IsSuperuser: true, IsActive: true})
		c.Next()
	})

	h := NewImportHandler(nil)
	imports := r.Group("/api/v1/subjects/:subject_id/imports")
	{
		imports.GET("/:task_id/questions", h.TaskQuestions)
		imports.GET("/queue-status", h.QueueStatus)
	}

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/subjects/1/imports/queue-status", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("queue-status 应命中静态路由，实际响应码 %d（400 说明被 :task_id 吞掉了）", w.Code)
	}
}
