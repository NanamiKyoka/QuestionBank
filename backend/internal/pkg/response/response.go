package response

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
)

// Body 统一响应结构：成功 code=0，失败 code 为业务错误码。
type Body struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
	Data    any    `json:"data,omitempty"`
}

// PageData 分页负载。
type PageData struct {
	List     any   `json:"list"`
	Total    int64 `json:"total"`
	Page     int   `json:"page"`
	PageSize int   `json:"page_size"`
}

func OK(c *gin.Context, data any) {
	c.JSON(http.StatusOK, Body{Code: 0, Message: "ok", Data: data})
}

func Created(c *gin.Context, data any) {
	c.JSON(http.StatusCreated, Body{Code: 0, Message: "ok", Data: data})
}

// Accepted 表示"已受理，但还没做完"。
//
// 用于异步任务：请求成功不代表结果已经产生。
// 状态码必须与 OK 区分开——前端据此知道该去轮询而不是直接渲染结果。
func Accepted(c *gin.Context, data any) {
	c.JSON(http.StatusAccepted, Body{Code: 0, Message: "已受理", Data: data})
}

func NoContent(c *gin.Context) {
	c.JSON(http.StatusOK, Body{Code: 0, Message: "ok"})
}

func Page(c *gin.Context, list any, total int64, page, pageSize int) {
	OK(c, PageData{List: list, Total: total, Page: page, PageSize: pageSize})
}

// Fail 把任意 error 翻译为统一响应，是 handler 层唯一的错误出口。
func Fail(c *gin.Context, err error) {
	e := apperr.From(err)
	body := Body{Code: int(e.Code), Message: e.Message, Detail: e.Detail}
	// 5xx 不回传 detail，避免泄露内部实现细节。
	if e.HTTPStatus() >= 500 {
		body.Detail = ""
	}
	c.AbortWithStatusJSON(e.HTTPStatus(), body)
}

func FailWith(c *gin.Context, code apperr.Code, message string) {
	Fail(c, apperr.New(code, message))
}
