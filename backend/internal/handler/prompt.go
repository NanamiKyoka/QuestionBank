package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/NanamiKyoka/questionmind/internal/middleware"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
	"github.com/NanamiKyoka/questionmind/internal/pkg/response"
	"github.com/NanamiKyoka/questionmind/internal/schema"
	"github.com/NanamiKyoka/questionmind/internal/service"
)

// PromptHandler 提示词模板接口。
//
// 鉴权：整组仅全局管理员，与 /ai 组一致。提示词决定的是全系统送给
// 模型的话术——一个普通用户改了它，等于改了所有人的智能录题结果，
// 因此这里不做"按学科授权"这种半吊子设计。路由侧应挂
// RequireAuth + RequireSuperuser，service 内部还有一道 authorizeSuperuser。
type PromptHandler struct {
	prompts *service.PromptService
}

func NewPromptHandler(prompts *service.PromptService) *PromptHandler {
	return &PromptHandler{prompts: prompts}
}

// List GET /prompts
func (h *PromptHandler) List(c *gin.Context) {
	actor, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	res, err := h.prompts.List(c.Request.Context(), actor)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// Create POST /prompts
func (h *PromptHandler) Create(c *gin.Context) {
	actor, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	var req schema.PromptCreateRequest
	if !bind(c, &req) {
		return
	}
	res, err := h.prompts.Create(c.Request.Context(), actor, &req)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.Created(c, res)
}

// Update PUT /prompts/:id
func (h *PromptHandler) Update(c *gin.Context) {
	actor, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var req schema.PromptUpdateRequest
	if !bind(c, &req) {
		return
	}
	res, err := h.prompts.Update(c.Request.Context(), actor, id, &req)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// Delete DELETE /prompts/:id
//
// 删掉的是这一条模板，不是"禁用它"：删完之后该作用域自动回退到
// 下一级来源（学科覆盖 → 全局 → 内置默认）。
func (h *PromptHandler) Delete(c *gin.Context) {
	actor, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	if err := h.prompts.Delete(c.Request.Context(), actor, id); err != nil {
		response.Fail(c, err)
		return
	}
	response.NoContent(c)
}

// SubjectPrompts GET /subjects/:sid/prompts
func (h *PromptHandler) SubjectPrompts(c *gin.Context) {
	actor, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	subjectID, ok := subjectParam(c)
	if !ok {
		return
	}
	res, err := h.prompts.SubjectPrompts(c.Request.Context(), actor, subjectID)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// SaveSubjectPrompts PUT /subjects/:sid/prompts
func (h *PromptHandler) SaveSubjectPrompts(c *gin.Context) {
	actor, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	subjectID, ok := subjectParam(c)
	if !ok {
		return
	}
	var req schema.SubjectPromptsUpdateRequest
	if !bind(c, &req) {
		return
	}
	res, err := h.prompts.SaveSubjectPrompts(c.Request.Context(), actor, subjectID, &req)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// subjectParam 取学科 id。
//
// 仓库里 /subjects 下的参数名历来是 :subject_id，而本组接口的契约写的是
// :sid。两个名字都收：参数名不一致的症状是"路径明明对、接口却报缺少
// 参数"，排查起来纯属浪费——而这种不一致只需要在这里多写两行就能抹平。
func subjectParam(c *gin.Context) (uint, bool) {
	raw := c.Param("subject_id")
	if raw == "" {
		raw = c.Param("sid")
	}
	if raw == "" {
		response.Fail(c, apperr.New(apperr.CodeInvalidParam, "缺少路径参数 subject_id"))
		return 0, false
	}
	n := uint(0)
	for _, ch := range raw {
		if ch < '0' || ch > '9' {
			response.Fail(c, apperr.New(apperr.CodeInvalidParam, "路径参数 subject_id 必须是数字"))
			return 0, false
		}
		n = n*10 + uint(ch-'0')
	}
	if n == 0 {
		response.Fail(c, apperr.New(apperr.CodeInvalidParam, "路径参数 subject_id 不合法"))
		return 0, false
	}
	return n, true
}
