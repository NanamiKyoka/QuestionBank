package handler

import (
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/NanamiKyoka/questionmind/internal/middleware"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
	"github.com/NanamiKyoka/questionmind/internal/pkg/response"
	"github.com/NanamiKyoka/questionmind/internal/service"
)

// SettingsHandler 系统设置接口。
//
// 鉴权说明：路由侧应挂 RequireAuth + RequireSuperuser（与 /ai 组一致），
// service 内部还有一道 authorizeSuperuser。这里仍然调 MustUser 拿当前用户，
// 因为 updated_by 必须落到具体的人身上。
type SettingsHandler struct {
	settings *service.SettingsService
}

func NewSettingsHandler(settings *service.SettingsService) *SettingsHandler {
	return &SettingsHandler{settings: settings}
}

// updateSettingRequest PUT /settings/:key 的请求体。
//
// 键放在路径里而不是请求体：资源就是"这一项设置"，
// 放路径能让审计日志（service/activity.go:174 的 DeriveAction）记成
// system_setting.update，键名则留在路径参数里可读。
type updateSettingRequest struct {
	Value string `json:"value" binding:"max=65535"`
}

// List GET /settings
func (h *SettingsHandler) List(c *gin.Context) {
	actor, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	items, err := h.settings.List(c.Request.Context(), actor)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, items)
}

// Update PUT /settings/:key
func (h *SettingsHandler) Update(c *gin.Context) {
	actor, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	key := strings.TrimSpace(c.Param("key"))
	if key == "" {
		response.Fail(c, apperr.New(apperr.CodeInvalidParam, "缺少设置项名称"))
		return
	}
	var req updateSettingRequest
	if !bind(c, &req) {
		return
	}
	item, err := h.settings.Update(c.Request.Context(), actor, key, req.Value)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, item)
}
