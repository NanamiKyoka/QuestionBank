// Package handler 是 HTTP 传输层：绑定参数、调用 service、翻译结果。
//
// 这一层刻意保持"薄"：不写业务逻辑，不碰数据库。
// 判断标准是——handler 里出现 if 判断业务规则，就说明放错地方了。
package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/NanamiKyoka/questionmind/internal/middleware"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
	"github.com/NanamiKyoka/questionmind/internal/pkg/response"
)

// bind 绑定并校验 JSON 请求体。
//
// 校验失败时把 validator 的具体原因放进 detail：
// 前端可以直接展示"password 长度至少 8 位"这种精确提示，
// 而不是笼统的"参数错误"。
func bind(c *gin.Context, req any) bool {
	if err := c.ShouldBindJSON(req); err != nil {
		if middleware.FailBodyTooLarge(c, err) {
			return false
		}
		response.Fail(c, apperr.New(apperr.CodeBindingFailed, "请求参数不合法").
			WithDetail("%v", err))
		return false
	}
	return true
}

// bindQuery 绑定查询参数（不做 required 校验，交给业务层）。
func bindQuery(c *gin.Context, req any) bool {
	if err := c.ShouldBindQuery(req); err != nil {
		response.Fail(c, apperr.New(apperr.CodeBindingFailed, "查询参数不合法").
			WithDetail("%v", err))
		return false
	}
	return true
}

// parseID 解析路径中的 :id。
func parseID(c *gin.Context, name string) (uint, bool) {
	raw := c.Param(name)
	if raw == "" {
		response.Fail(c, apperr.New(apperr.CodeInvalidParam, "缺少路径参数 "+name))
		return 0, false
	}
	var n uint64
	for _, ch := range raw {
		if ch < '0' || ch > '9' {
			response.Fail(c, apperr.New(apperr.CodeInvalidParam, "路径参数 "+name+" 必须是数字"))
			return 0, false
		}
		n = n*10 + uint64(ch-'0')
	}
	if n == 0 {
		response.Fail(c, apperr.New(apperr.CodeInvalidParam, "路径参数 "+name+" 不合法"))
		return 0, false
	}
	return uint(n), true
}
