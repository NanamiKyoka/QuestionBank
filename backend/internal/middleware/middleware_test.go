package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRedactQueryHidesSecrets(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"page=2", "page=2"},
		{"token=abcdef123", "token=%2A%2A%2A"},
		{"page=1&token=abcdef123&q=hello", "page=1&q=hello&token=%2A%2A%2A"},
		{"TOKEN=abcdef", "TOKEN=%2A%2A%2A"},
		{"access_token=x&api_key=y", "access_token=%2A%2A%2A&api_key=%2A%2A%2A"},
		// 解析不出来的串整段丢弃，绝不能原样漏出去。
		{"token=%zz", "[unreadable]"},
	}
	for _, tc := range cases {
		got := redactQuery(tc.in)
		if tc.in != "" && strings.Contains(got, "abcdef") {
			t.Fatalf("redactQuery(%q) = %q，泄露了 token", tc.in, got)
		}
		if tc.want != "" && got != tc.want {
			t.Fatalf("redactQuery(%q) = %q，期望 %q", tc.in, got, tc.want)
		}
	}
}

// 请求体超过上限时必须得到 413，而不是读进内存后才 500。
func TestBodyLimitRejectsOversizedRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(BodyLimit(16))
	r.POST("/", func(c *gin.Context) {
		_, err := c.GetRawData()
		if err != nil {
			FailBodyTooLarge(c, err)
			return
		}
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("a", 64)))
	req.Header.Set("Content-Type", "text/plain")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("超限应返回 413，实际 %d（body=%s）", w.Code, w.Body.String())
	}
}

// 路由级中间件要能放宽默认上限：上传路由全靠这条。
func TestBodyLimitCanBeOverriddenPerRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(BodyLimit(16))
	r.POST("/small", func(c *gin.Context) {
		if _, err := c.GetRawData(); err != nil {
			FailBodyTooLarge(c, err)
			return
		}
		c.Status(http.StatusOK)
	})
	r.POST("/large", bodyLimitOverride(1024), func(c *gin.Context) {
		if _, err := c.GetRawData(); err != nil {
			FailBodyTooLarge(c, err)
			return
		}
		c.Status(http.StatusOK)
	})

	payload := strings.Repeat("a", 64)
	for _, tc := range []struct {
		path string
		want int
	}{
		{"/small", http.StatusRequestEntityTooLarge},
		{"/large", http.StatusOK},
	} {
		req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(payload))
		req.Header.Set("Content-Type", "text/plain")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != tc.want {
			t.Fatalf("%s 应返回 %d，实际 %d", tc.path, tc.want, w.Code)
		}
	}
}

// bodyLimitOverride 测试用的路由级覆盖（生产代码在 router 包里）。
func bodyLimitOverride(n int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		SetBodyLimit(c, n)
		c.Next()
	}
}
