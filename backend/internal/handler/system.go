package handler

import (
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/NanamiKyoka/questionmind/internal/db"
	"github.com/NanamiKyoka/questionmind/internal/pkg/response"
	"github.com/NanamiKyoka/questionmind/internal/service"
	"github.com/NanamiKyoka/questionmind/internal/version"
)

// SystemHandler 系统级接口：健康检查、版本、初始化状态。
type SystemHandler struct {
	auth    *service.AuthService
	gdb     *gorm.DB
	appName string
}

func NewSystemHandler(auth *service.AuthService, gdb *gorm.DB, appName string) *SystemHandler {
	return &SystemHandler{auth: auth, gdb: gdb, appName: appName}
}

// Health GET /system/health
// 供容器编排、反向代理与运维探活使用。
func (h *SystemHandler) Health(c *gin.Context) {
	body := gin.H{"status": "ok", "database": "ok"}
	if err := db.Health(h.gdb); err != nil {
		body["status"] = "degraded"
		body["database"] = err.Error()
		c.JSON(503, body)
		return
	}
	response.OK(c, body)
}

// Version GET /system/version
// 无需鉴权：前端在登录页就要能探测后端版本与是否已完成初始化。
func (h *SystemHandler) Version(c *gin.Context) {
	response.OK(c, version.Get())
}

// SetupStatus GET /system/setup-status
func (h *SystemHandler) SetupStatus(c *gin.Context) {
	res, err := h.auth.SetupStatus(c.Request.Context())
	if err != nil {
		response.Fail(c, err)
		return
	}
	res.AppName = h.appName
	res.Version = version.Get().Version
	response.OK(c, res)
}
