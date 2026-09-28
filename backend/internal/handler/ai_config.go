package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/NanamiKyoka/questionmind/internal/ai"
	"github.com/NanamiKyoka/questionmind/internal/middleware"
	"github.com/NanamiKyoka/questionmind/internal/pkg/response"
	"github.com/NanamiKyoka/questionmind/internal/schema"
	"github.com/NanamiKyoka/questionmind/internal/service"
)

// AIConfigHandler AI 供应商与模型配置接口。全部为管理员专属。
type AIConfigHandler struct {
	cfg *service.AIConfigService
}

func NewAIConfigHandler(cfg *service.AIConfigService) *AIConfigHandler {
	return &AIConfigHandler{cfg: cfg}
}

// ListProviders GET /ai/providers
func (h *AIConfigHandler) ListProviders(c *gin.Context) {
	actor, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	res, err := h.cfg.ListProviders(c.Request.Context(), actor)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// CreateProvider POST /ai/providers
func (h *AIConfigHandler) CreateProvider(c *gin.Context) {
	actor, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	var req schema.ProviderCreateRequest
	if !bind(c, &req) {
		return
	}
	res, err := h.cfg.CreateProvider(c.Request.Context(), actor, &req)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.Created(c, res)
}

// UpdateProvider PUT /ai/providers/:id
func (h *AIConfigHandler) UpdateProvider(c *gin.Context) {
	actor, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var req schema.ProviderUpdateRequest
	if !bind(c, &req) {
		return
	}
	res, err := h.cfg.UpdateProvider(c.Request.Context(), actor, id, &req)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// DeleteProvider DELETE /ai/providers/:id
func (h *AIConfigHandler) DeleteProvider(c *gin.Context) {
	actor, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	if err := h.cfg.DeleteProvider(c.Request.Context(), actor, id); err != nil {
		response.Fail(c, err)
		return
	}
	response.NoContent(c)
}

// TestProvider POST /ai/providers/:id/test
func (h *AIConfigHandler) TestProvider(c *gin.Context) {
	actor, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var req schema.ProviderTestRequest
	if !bind(c, &req) {
		return
	}
	res, err := h.cfg.TestProvider(c.Request.Context(), actor, id, &req)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// CreateModel POST /ai/models
func (h *AIConfigHandler) CreateModel(c *gin.Context) {
	actor, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	var req schema.ModelCreateRequest
	if !bind(c, &req) {
		return
	}
	res, err := h.cfg.CreateModel(c.Request.Context(), actor, &req)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.Created(c, res)
}

// UpdateModel PUT /ai/models/:id
func (h *AIConfigHandler) UpdateModel(c *gin.Context) {
	actor, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var req schema.ModelUpdateRequest
	if !bind(c, &req) {
		return
	}
	res, err := h.cfg.UpdateModel(c.Request.Context(), actor, id, &req)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// DeleteModel DELETE /ai/models/:id
func (h *AIConfigHandler) DeleteModel(c *gin.Context) {
	actor, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	if err := h.cfg.DeleteModel(c.Request.Context(), actor, id); err != nil {
		response.Fail(c, err)
		return
	}
	response.NoContent(c)
}

// GetActiveConfig GET /ai/active-config
func (h *AIConfigHandler) GetActiveConfig(c *gin.Context) {
	actor, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	res, err := h.cfg.GetActiveConfig(c.Request.Context(), actor)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// SetActiveConfig PUT /ai/active-config
func (h *AIConfigHandler) SetActiveConfig(c *gin.Context) {
	actor, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	var req schema.SetActiveConfigRequest
	if !bind(c, &req) {
		return
	}
	res, err := h.cfg.SetActiveConfig(c.Request.Context(), actor, &req)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// InterfaceTypes GET /ai/interface-types
//
// 前端下拉框的数据源。返回后端真正支持的协议，
// 避免界面提供了选项而后端并不支持。
func (h *AIConfigHandler) InterfaceTypes(c *gin.Context) {
	response.OK(c, gin.H{"types": ai.InterfaceTypes()})
}
