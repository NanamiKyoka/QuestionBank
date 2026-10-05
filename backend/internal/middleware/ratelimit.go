package middleware

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
	"github.com/NanamiKyoka/questionmind/internal/pkg/response"
)

// 令牌桶参数。
//
// 登录是**唯一**不需要先证明身份就能触发 bcrypt 的入口，
// 而 hash.DummyVerify 只拉平了时序、挡不住无限次尝试。
// 这里按 "IP + 用户名" 两个维度各放一个桶：单个 IP 慢慢撞多个账号、
// 或单个账号被很多 IP 分摊着撞，两种打法都要挡住。
const (
	// rateLimitRefillEvery 满桶所需的静默时间。
	rateLimitRefillEvery = 10 * time.Second
	// rateLimitBurst 突发容量：正常人输错几次密码不该被拦。
	rateLimitBurst = 10
	// rateLimitMaxKeys 桶表上限，防止扫 IP 把内存吃光。
	rateLimitMaxKeys = 4096
	// rateLimitIdle 桶闲置多久后可被回收。
	rateLimitIdle = 10 * time.Minute
)

type bucket struct {
	tokens float64
	last   time.Time
}

// RateLimiter 进程内令牌桶。
//
// 不用 Redis：这是单二进制交付的部署形态，为一个登录限流引入外部依赖
// 得不偿失。多副本时各副本独立计数，总容忍度放宽到副本数倍——
// 对"挡住爆破"这个目标足够。
type RateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	now     func() time.Time
}

// NewRateLimiter 构造。now 为 nil 时用 time.Now，测试可注入假时钟。
func NewRateLimiter(now func() time.Time) *RateLimiter {
	if now == nil {
		now = time.Now
	}
	return &RateLimiter{buckets: make(map[string]*bucket), now: now}
}

// Allow 取一个令牌；桶空则拒绝。
//
// 补充是惰性的：不跑后台清扫 goroutine，而是在每次访问时按流逝时间
// 补足、顺手回收闲置键。低频入口这样足够，还省掉一个要正确关闭的任务。
func (l *RateLimiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	l.prune(now)

	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) >= rateLimitMaxKeys {
			// 表满了说明有人大面积扫 IP。这里不淘汰最旧的键——
			// 那等于送攻击者一个"打满就重置"的后门，直接按最坏情况拒绝。
			return false
		}
		b = &bucket{tokens: rateLimitBurst, last: now}
		l.buckets[key] = b
	}

	if missing := now.Sub(b.last); missing > 0 {
		b.tokens += float64(missing) / float64(rateLimitRefillEvery) * rateLimitBurst
		if b.tokens > rateLimitBurst {
			b.tokens = rateLimitBurst
		}
		b.last = now
	}

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// prune 回收长期不用的键。只在表接近上限时才扫，避免每次请求都遍历。
func (l *RateLimiter) prune(now time.Time) {
	if len(l.buckets) < rateLimitMaxKeys {
		return
	}
	for key, b := range l.buckets {
		if now.Sub(b.last) > rateLimitIdle {
			delete(l.buckets, key)
		}
	}
}

// limiterKeyAddr 取限流维度里的客户端地址。
//
// 刻意不用 c.ClientIP()：它会读 X-Forwarded-For，而本服务默认直连，
// 信任了就等于让攻击者用一个请求头绕开整套限流。
func limiterKeyAddr(c *gin.Context) string {
	host, _, err := net.SplitHostPort(c.Request.RemoteAddr)
	if err != nil {
		return c.Request.RemoteAddr
	}
	return host
}

// RateLimit 鉴权接口的限流：来源 IP 一次，请求体里的用户名再一次。
//
// 用户名从原始 body 里 peek 出来并把 body 放回去，handler 随后仍能正常
// 绑定——gin 的请求体是流，不放回去 handler 就会读到空。
func RateLimit(l *RateLimiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		addr := limiterKeyAddr(c)
		if !l.Allow("ip:" + addr) {
			respondTooMany(c, addr)
			return
		}

		username := peekUsername(c)
		if username != "" && !l.Allow("user:"+strings.ToLower(username)) {
			respondTooMany(c, username)
			return
		}
		c.Next()
	}
}

// peekUsername 读出 body 里的 username，然后把 body 复原。
//
// 必须复原：gin 的请求体是一条流，不放回去 handler 就会读到空，
// 表现为"登录接口突然说缺 username"。其余字段对限流无意义，不读。
// 解析失败（不是 JSON、读错、body 过大）一律返回空串——那时只剩
// IP 一维在起作用，仍然比不设防好。
func peekUsername(c *gin.Context) string {
	if c.Request == nil || c.Request.Body == nil {
		return ""
	}

	raw, err := io.ReadAll(c.Request.Body)
	_ = c.Request.Body.Close()
	// 无论读成功与否都要把 body 装回去，否则 handler 读到空。
	c.Request.Body = io.NopCloser(bytes.NewReader(raw))
	if err != nil || len(raw) == 0 || len(raw) > 1<<20 {
		return ""
	}

	var payload struct {
		Username string `json:"username"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.Username)
}

func respondTooMany(c *gin.Context, key string) {
	response.Fail(c, apperr.New(apperr.CodeTooManyRequests, "尝试过于频繁，请稍后再试").
		WithDetail("请等待约 %d 秒后重试（%s）", int(rateLimitRefillEvery.Seconds()), clip(key, 48)))
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
