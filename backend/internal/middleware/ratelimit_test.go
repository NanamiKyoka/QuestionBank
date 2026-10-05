package middleware

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// 突发额度用尽后必须拒绝，静默一段时间后按比例回填。
func TestRateLimiterBurstThenRefill(t *testing.T) {
	gin.SetMode(gin.TestMode)
	clock := time.Now()
	l := NewRateLimiter(func() time.Time { return clock })

	for i := 0; i < rateLimitBurst; i++ {
		if !l.Allow("ip:1.2.3.4") {
			t.Fatalf("第 %d 次仍在突发额度内，应当放行", i+1)
		}
	}
	if l.Allow("ip:1.2.3.4") {
		t.Fatalf("突发额度已用尽，第 %d 次应当拒绝", rateLimitBurst+1)
	}

	// 静默半个回填周期，只该回填一半额度。
	clock = clock.Add(rateLimitRefillEvery / 2)
	allowed := 0
	for i := 0; i < rateLimitBurst; i++ {
		if l.Allow("ip:1.2.3.4") {
			allowed++
		}
	}
	if allowed == 0 || allowed >= rateLimitBurst {
		t.Fatalf("半周期后应回填部分额度，实际放行 %d 次", allowed)
	}

	// 长时间静默后回满，但不会超过突发容量。
	clock = clock.Add(10 * rateLimitRefillEvery)
	for i := 0; i < rateLimitBurst; i++ {
		if !l.Allow("ip:1.2.3.4") {
			t.Fatalf("长时间静默后第 %d 次应当放行", i+1)
		}
	}
	if l.Allow("ip:1.2.3.4") {
		t.Fatal("回填不应超过突发容量")
	}
}

// 两个维度互不影响：一个 IP 换用户名、一个用户名换 IP，都得各记各的账。
func TestRateLimiterTracksDimensionsSeparately(t *testing.T) {
	gin.SetMode(gin.TestMode)
	l := NewRateLimiter(nil)

	for i := 0; i < rateLimitBurst; i++ {
		if !l.Allow("ip:9.9.9.9") {
			t.Fatalf("第 %d 次 IP 维度应放行", i+1)
		}
	}
	if l.Allow("ip:9.9.9.9") {
		t.Fatal("该 IP 额度已尽，应拒绝")
	}
	if !l.Allow("user:victim") {
		t.Fatal("换用户名后账号维度应是满的，应放行")
	}
	for i := 0; i < rateLimitBurst; i++ {
		l.Allow("user:victim")
	}
	if l.Allow("user:victim") {
		t.Fatal("该账号额度已尽，应拒绝")
	}
	if !l.Allow("ip:8.8.8.8") {
		t.Fatal("换 IP 后来源维度应是满的，应放行")
	}
}

// 桶表被打满时按最坏情况拒绝，不能因为淘汰旧键送攻击者重置窗口。
func TestRateLimiterRejectsWhenTableFull(t *testing.T) {
	gin.SetMode(gin.TestMode)
	clock := time.Now()
	l := NewRateLimiter(func() time.Time { return clock })

	for i := 0; i < rateLimitMaxKeys; i++ {
		if !l.Allow(fmt.Sprintf("ip:10.0.%d.%d", i/256, i%256)) {
			t.Fatalf("建表阶段第 %d 次应放行", i+1)
		}
	}
	if l.Allow("ip:192.0.2.1") {
		t.Fatal("桶表已满且没有闲置键，新键应被拒绝")
	}

	// 闲置够久的键要能被回收，否则表一旦打满就再也进不来新键。
	clock = clock.Add(rateLimitIdle + time.Minute)
	if !l.Allow("ip:192.0.2.1") {
		t.Fatal("闲置键回收后新键应放行")
	}
}

// username 要能被读出来，而且读完必须把 body 复原，否则 handler 读到空。
func TestPeekUsernameRestoresBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := `{"username":"Alice","password":"s3cret"}`

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(body))

	if got := peekUsername(c); got != "Alice" {
		t.Fatalf("peekUsername = %q，期望 Alice", got)
	}
	raw, err := io.ReadAll(c.Request.Body)
	if err != nil {
		t.Fatalf("读回 body 出错: %v", err)
	}
	if string(raw) != body {
		t.Fatalf("body 未复原，实际 %q", raw)
	}
}

// 非 JSON / 超大 body 一律退回空串，由 IP 单维度兜底，不能 panic。
func TestPeekUsernameFallsBackOnGarbage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, body := range []string{
		`not json`,
		`{"username":` + strings.Repeat("a", 2<<20),
	} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(body))
		if got := peekUsername(c); got != "" {
			t.Fatalf("异常 body 应退回空串，实际 %q", got)
		}
		if c.Request.Body == nil {
			t.Fatal("body 被读空后必须装回去")
		}
	}
}

// 限流命中时要返回 429 且**中止**链路，绝不能一边报 429 一边让登录跑完。
func TestRateLimitMiddlewareStopsChain(t *testing.T) {
	gin.SetMode(gin.TestMode)
	l := NewRateLimiter(nil)

	handlerRan := false
	r := gin.New()
	r.POST("/login", RateLimit(l), func(c *gin.Context) {
		handlerRan = true
		c.Status(http.StatusOK)
	})

	payload := `{"username":"alice","password":"x"}`
	for i := 0; i < rateLimitBurst; i++ {
		handlerRan = false
		req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(payload))
		req.RemoteAddr = "203.0.113.7:5000"
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK || !handlerRan {
			t.Fatalf("第 %d 次应放行，实际 code=%d handlerRan=%v", i+1, w.Code, handlerRan)
		}
	}

	handlerRan = false
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(payload))
	req.RemoteAddr = "203.0.113.7:5000"
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("额度用尽应返回 429，实际 %d", w.Code)
	}
	if handlerRan {
		t.Fatal("限流命中后登录 handler 不该执行")
	}
}
