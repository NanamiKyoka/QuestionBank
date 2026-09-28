package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/NanamiKyoka/questionmind/internal/middleware"
	"github.com/NanamiKyoka/questionmind/internal/pkg/response"
	"github.com/NanamiKyoka/questionmind/internal/service"
)

// StatsHandler 统计概览接口。
type StatsHandler struct {
	stats *service.StatsService
}

func NewStatsHandler(stats *service.StatsService) *StatsHandler {
	return &StatsHandler{stats: stats}
}

// Subject GET /subjects/:subject_id/stats
func (h *StatsHandler) Subject(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	subjectID, ok := parseID(c, "subject_id")
	if !ok {
		return
	}
	res, err := h.stats.Subject(c.Request.Context(), user, subjectID)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// Global GET /stats/overview
//
// 仅管理员：包含用户数与 AI 用量这类运营信息。
func (h *StatsHandler) Global(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	res, err := h.stats.Global(c.Request.Context(), user)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}
