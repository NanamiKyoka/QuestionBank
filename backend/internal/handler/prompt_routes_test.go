package handler

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// 提示词这组路由必须能与既有路由共存在同一棵路由树上。
//
// 为什么要专门测它：/subjects 下已经有一整组 :subject_id 路由，
// 而本组的学科子资源也要挂上去。gin 的路由树对"同一位置出现不同名字的
// 通配段"是直接 panic 的（gin@v1.12.0 tree.go:230），而且 panic 发生在
// **进程启动时**——等到有人访问才发现的话，服务根本起不来。
// 这个测试把那条失败提前到 CI。
func TestPromptRoutesRegister(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	h := NewPromptHandler(nil)

	// 既有路由先注册，模拟真实装配顺序。
	subjects := r.Group("/api/v1/subjects")
	subjects.GET("/:subject_id", func(c *gin.Context) {})
	subjects.GET("/:subject_id/questions", func(c *gin.Context) {})

	prompts := r.Group("/api/v1/prompts")
	{
		prompts.GET("", h.List)
		prompts.POST("", h.Create)
		prompts.PUT("/:id", h.Update)
		prompts.DELETE("/:id", h.Delete)
	}

	subjectGroup := r.Group("/api/v1/subjects/:subject_id")
	{
		subjectGroup.GET("/prompts", h.SubjectPrompts)
		subjectGroup.PUT("/prompts", h.SaveSubjectPrompts)
	}

	want := []string{
		"/api/v1/prompts",
		"/api/v1/prompts/:id",
		"/api/v1/subjects/:subject_id/prompts",
	}
	got := make(map[string]bool, len(r.Routes()))
	for _, ri := range r.Routes() {
		got[ri.Path] = true
	}
	for _, path := range want {
		if !got[path] {
			t.Fatalf("路由未注册成功: %s", path)
		}
	}
}

// 学科 id 的路径参数名：契约里写的是 :sid，仓库里的既有约定是 :subject_id，
// 而 gin 不允许同一位置出现两个不同名字的通配段。handler 侧两个名字都收，
// 这条用例守住这个兼容性。
func TestSubjectParamAcceptsBothNames(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, param := range []string{"subject_id", "sid"} {
		r := gin.New()
		got := uint(0)
		ok := false
		name := param
		r.GET("/api/v1/subjects/:"+name, func(c *gin.Context) {
			got, ok = subjectParam(c)
			if !ok {
				return
			}
			c.Status(http.StatusOK)
		})

		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/subjects/42", nil)
		r.ServeHTTP(w, req)

		if !ok || got != 42 {
			t.Fatalf("参数名 :%s 应解析出 42，实际 %d（ok=%v）", name, got, ok)
		}
	}

	// 非数字必须被挡在 service 之前：把 "abc" 当成 0 去查库，
	// 得到的是一条"学科不存在"，那会把参数错误说成资源错误。
	r := gin.New()
	r.GET("/api/v1/subjects/:subject_id", func(c *gin.Context) {
		if _, ok := subjectParam(c); ok {
			c.Status(http.StatusOK)
		}
	})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/subjects/abc", nil)
	r.ServeHTTP(w, req)
	if w.Code == http.StatusOK {
		t.Fatal("非数字的学科 id 必须被拒绝")
	}
}

// 路由的鉴权中间件由 router 组装，这里只验证 handler 自己也会问身份：
// 缺用户时必须在碰 service 之前就返回 401，而不是 panic。
func TestPromptHandlersRequireUser(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	h := NewPromptHandler(nil)
	prompts := r.Group("/api/v1/prompts")
	{
		prompts.GET("", h.List)
		prompts.POST("", h.Create)
	}
	subjects := r.Group("/api/v1/subjects/:subject_id")
	{
		subjects.GET("/prompts", h.SubjectPrompts)
		subjects.PUT("/prompts", h.SaveSubjectPrompts)
	}

	for _, tc := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/v1/prompts", ""},
		{http.MethodPost, "/api/v1/prompts", `{"key":"import_extract","system_prompt":"x"}`},
		{http.MethodGet, "/api/v1/subjects/1/prompts", ""},
		{http.MethodPut, "/api/v1/subjects/1/prompts", `{"prompts":[{"key":"import_extract","system_prompt":"x"}]}`},
	} {
		w := httptest.NewRecorder()
		var body io.Reader
		if tc.body != "" {
			body = strings.NewReader(tc.body)
		}
		req, _ := http.NewRequest(tc.method, tc.path, body)
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s 未登录时应返回 401，实际 %d", tc.method, tc.path, w.Code)
		}
	}
}
