package middleware

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
	"github.com/NanamiKyoka/questionmind/internal/pkg/response"
)

// ctxBodyLimitKey 请求体上限的上下文键。
//
// 上限**必须**延迟到读的时候才生效：中间件的执行顺序是全局的，
// 而"这条路由允许多大"是路由级信息，注册顺序上永远排在全局中间件之后。
// 让上限在 Read 时才向上下文取值，路由中间件就能在 handler 真正读体
// 之前改掉它，不需要把路由表和限制表维护成两份。
const ctxBodyLimitKey = "qm.body_limit"

// BodyLimit 默认请求体上限。
//
// 没有上限的请求体是免费的内存放大器：`/auth/login` 不需要认证，
// 一个几 GB 的 POST 足以把进程推到 OOM——而它连密码都不用猜对。
// 8 MB 覆盖所有 JSON 端点（最大的是 200000 字的抽取内容），
// 上传类路由会在自己的组里调高。
func BodyLimit(n int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set(ctxBodyLimitKey, n)
		if c.Request != nil && c.Request.Body != nil {
			c.Request.Body = &cappedBody{rc: c.Request.Body, c: c}
		}
		c.Next()
	}
}

// SetBodyLimit 由路由级中间件调用，覆盖本条路由的上限。
func SetBodyLimit(c *gin.Context, n int64) {
	c.Set(ctxBodyLimitKey, n)
}

// ErrBodyTooLarge 请求体超过当前路由的上限。
//
// 与 net/http 的 *http.MaxBytesError 同形，调用方用 errors.As 即可
// 同时识别两者，不必关心上限是在哪一层设的。
var ErrBodyTooLarge = errors.New("middleware: 请求体超过上限")

// cappedBody 读到上限就报错的请求体。
//
// 上限在**每次 Read 时**从上下文取：见 ctxBodyLimitKey 的注释。
type cappedBody struct {
	rc       io.ReadCloser
	c        *gin.Context
	consumed int64
	err      error
}

func (b *cappedBody) Read(p []byte) (int, error) {
	if b.err != nil {
		return 0, b.err
	}
	n, err := b.rc.Read(p)
	b.consumed += int64(n)
	if limit, ok := b.c.Get(ctxBodyLimitKey); ok {
		if max, isInt := limit.(int64); isInt && b.consumed > max {
			b.err = ErrBodyTooLarge
			return n, b.err
		}
	}
	if err != nil {
		return n, err
	}
	return n, nil
}

func (b *cappedBody) Close() error { return b.rc.Close() }

// FailBodyTooLarge 把超限翻成统一错误信封。
//
// 走 413 而不是 500：这是客户端该改的东西（换个更小的文件），
// 报成服务端错误会让用户去刷新页面重试同一个请求。
func FailBodyTooLarge(c *gin.Context, err error) bool {
	if err == nil {
		return false
	}
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge), errors.Is(err, ErrBodyTooLarge):
	case strings.Contains(err.Error(), "request body too large"),
		strings.Contains(err.Error(), ErrBodyTooLarge.Error()):
		// mime/multipart 在 ReadForm 内部不保证保留错误链，
		// 只好退一步按文本认——认错方向是安全的（多认 = 提示更清楚）。
	default:
		return false
	}
	response.Fail(c, apperr.New(apperr.CodePayloadTooLarge, "请求体过大").
		WithDetail("请减小文件体积，或拆成多批提交"))
	return true
}
