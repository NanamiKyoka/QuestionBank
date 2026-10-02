package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/NanamiKyoka/questionmind/internal/middleware"
	"github.com/NanamiKyoka/questionmind/internal/pkg/response"
	"github.com/NanamiKyoka/questionmind/internal/schema"
	"github.com/NanamiKyoka/questionmind/internal/service"
)

// SolveHandler 题库内 AI 解题接口。
//
// 与 QuestionHandler 分开只是为了不去动它的构造签名：两者共享的路由组，
// 依赖的服务不同（这个要模型配置）。
type SolveHandler struct {
	solve *service.SolveService
}

func NewSolveHandler(solve *service.SolveService) *SolveHandler {
	return &SolveHandler{solve: solve}
}

// Solve POST /questions/:id/solve
//
// 单题粒度，批量由前端循环调用：每做完一道就已经落库，
// 中途关页只丢没做到的那几道（见 schema.SolveRequest 的说明）。
func (h *SolveHandler) Solve(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var req schema.SolveRequest
	if !bind(c, &req) {
		return
	}
	res, err := h.solve.Solve(c.Request.Context(), user, id, &req)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}
