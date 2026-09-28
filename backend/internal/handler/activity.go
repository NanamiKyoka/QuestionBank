package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/NanamiKyoka/questionmind/internal/middleware"
	"github.com/NanamiKyoka/questionmind/internal/pkg/pagination"
	"github.com/NanamiKyoka/questionmind/internal/pkg/response"
	"github.com/NanamiKyoka/questionmind/internal/schema"
	"github.com/NanamiKyoka/questionmind/internal/service"
)

// ActivityHandler 操作审计接口。仅管理员。
type ActivityHandler struct {
	activities *service.ActivityService
}

func NewActivityHandler(activities *service.ActivityService) *ActivityHandler {
	return &ActivityHandler{activities: activities}
}

// List GET /activity-logs
func (h *ActivityHandler) List(c *gin.Context) {
	actor, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	var q schema.ActivityLogQuery
	if !bindQuery(c, &q) {
		return
	}
	page := pagination.Parse(c)

	res, total, err := h.activities.List(c.Request.Context(), actor, q, page.Limit(), page.Offset())
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.Page(c, res, total, page.Page, page.PageSize)
}
