package aiagent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"time"
)

// ClientChannel 前端工具的等待/回传通道。
//
// 为什么用进程内注册表而不是"写一行 + 前端轮询"：
// 等待方是同一个进程里正在跑的 SSE 生成器，不是游离的后台任务。
// 用数据库行加轮询只会平添延迟和写放大，而且 run 结束后那行还得清理。
// 前端回传走的是同一个进程的 HTTP 请求，直接唤醒等待中的 goroutine 即可。
//
// ⚠️ 前提是**单进程**：若部署成多副本，前端回传可能落到另一个实例上，
// 通道就会静默失效（表现为前端工具永远超时）。因此这里检测副本数，
// 多副本时直接不向前端暴露客户端工具——宁可少一个功能，
// 也不要让用户遇到"点了没反应、等 30 秒超时"的诡异现象。
const defaultWaitTimeout = 30 * time.Second

// ErrChannelDisabled 多副本部署下客户端工具不可用。
var ErrChannelDisabled = errors.New("aiagent: 客户端工具在多副本部署下不可用")

type pending struct {
	runID  string
	done   chan struct{}
	once   sync.Once
	mu     sync.Mutex
	value  map[string]any
	closed bool
}

func (p *pending) resolve(v map[string]any) bool {
	set := false
	p.once.Do(func() {
		p.mu.Lock()
		p.value = v
		p.mu.Unlock()
		close(p.done)
		set = true
	})
	return set
}

func (p *pending) result() map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.value
}

// Channel 管理待回传的前端工具调用。
type Channel struct {
	mu      sync.Mutex
	pending map[string]*pending

	timeout time.Duration
	enabled bool
}

// NewChannel 构造。timeout 为 0 时使用默认值。
func NewChannel(timeout time.Duration) *Channel {
	if timeout <= 0 {
		timeout = defaultWaitTimeout
	}
	return &Channel{
		pending: make(map[string]*pending),
		timeout: timeout,
		enabled: detectSingleProcess(),
	}
}

// detectSingleProcess 判断当前是否单进程部署。
//
// 只认 WEB_CONCURRENCY / QM_REPLICAS 这两个约定变量。
// 默认（未设置）视为单进程——这正是单二进制交付的默认形态。
func detectSingleProcess() bool {
	for _, key := range []string{"WEB_CONCURRENCY", "QM_REPLICAS"} {
		raw := os.Getenv(key)
		if raw == "" {
			continue
		}
		n, err := strconv.Atoi(raw)
		if err != nil {
			continue
		}
		if n > 1 {
			slog.Warn("检测到多副本部署，已停用前端工具通道",
				"env", key, "replicas", n)
			return false
		}
	}
	return true
}

// Enabled 前端工具是否可用。
func (c *Channel) Enabled() bool { return c.enabled }

// Open 登记一次等待并返回一次性票据。
func (c *Channel) Open(runID string) string {
	ticket := newTicket()
	p := &pending{runID: runID, done: make(chan struct{})}

	c.mu.Lock()
	c.pending[ticket] = p
	c.mu.Unlock()
	return ticket
}

// Resolve 前端回传结果。
//
// 刻意**不**在这里摘掉条目：回传可能早于等待方进入 Wait
// （生成器 yield 出事件后，要等消费者再驱动一次才会开始等待），
// 提前摘掉会让那次回传丢失、白等一个超时。
// 条目统一由 Wait 的 defer 回收，一次性语义靠 once 保证。
func (c *Channel) Resolve(ticket, runID string, payload map[string]any) bool {
	c.mu.Lock()
	p, ok := c.pending[ticket]
	c.mu.Unlock()
	if !ok {
		return false
	}
	// 票据必须属于同一个 run：防止串话或伪造。
	if p.runID != runID {
		return false
	}
	return p.resolve(payload)
}

// Wait 等待前端回传。返回 (结果, 是否成功)。
//
// 超时返回 false 而不是阻塞：调用方据此把"前端没响应"
// 转成给模型看的错误结果，让模型改用文字说明，而不是把整个 run 挂死。
func (c *Channel) Wait(ctx context.Context, ticket string) (map[string]any, bool) {
	c.mu.Lock()
	p, ok := c.pending[ticket]
	c.mu.Unlock()
	if !ok {
		return nil, false
	}
	defer func() {
		c.mu.Lock()
		delete(c.pending, ticket)
		c.mu.Unlock()
	}()

	timer := time.NewTimer(c.timeout)
	defer timer.Stop()

	select {
	case <-p.done:
		return p.result(), true
	case <-timer.C:
		slog.Warn("前端工具等待超时", "ticket", ticket, "run_id", p.runID)
		return nil, false
	case <-ctx.Done():
		return nil, false
	}
}

// PendingCount 当前等待中的调用数，供健康检查与测试使用。
func (c *Channel) PendingCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.pending)
}

// newTicket 生成一次性票据。
//
// 用 crypto/rand 而不是自增序号：票据会随 SSE 下发到浏览器，
// 可预测的话就可能被第三方伪造回传（虽然还要过 run_id 校验，
// 但多一层不可预测性没有坏处）。
func newTicket() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// 极罕见：系统随机源不可用。退化为纳秒时间戳，
		// 至少不会与同一 run 内的其它票据相撞。
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
