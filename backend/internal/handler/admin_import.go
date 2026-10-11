package handler

import (
	"io"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/NanamiKyoka/questionmind/internal/middleware"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
	"github.com/NanamiKyoka/questionmind/internal/pkg/response"
	"github.com/NanamiKyoka/questionmind/internal/service"
)

const maxAdminImportBytes = 5 << 20

type AdminImportHandler struct {
	imports *service.AdminImportService
}

func NewAdminImportHandler(imports *service.AdminImportService) *AdminImportHandler {
	return &AdminImportHandler{imports: imports}
}

func (h *AdminImportHandler) UserTemplate(c *gin.Context) {
	if _, ok := middleware.MustUser(c); !ok {
		return
	}
	data, err := h.imports.UserTemplate()
	if err != nil {
		response.Fail(c, apperr.ErrInternal.WithCause(err))
		return
	}
	writeXLSX(c, "users_template.xlsx", data)
}

func (h *AdminImportHandler) ImportUsers(c *gin.Context) {
	actor, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	data, err := readAdminImportFile(c)
	if err != nil {
		response.Fail(c, err)
		return
	}
	res, err := h.imports.ImportUsers(c.Request.Context(), actor, data)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

func (h *AdminImportHandler) TagTemplate(c *gin.Context) {
	if _, ok := middleware.MustUser(c); !ok {
		return
	}
	data, err := h.imports.TagTemplate()
	if err != nil {
		response.Fail(c, apperr.ErrInternal.WithCause(err))
		return
	}
	writeXLSX(c, "tags_template.xlsx", data)
}

func (h *AdminImportHandler) ImportTags(c *gin.Context) {
	actor, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	subjectID, ok := parseID(c, "subject_id")
	if !ok {
		return
	}
	data, err := readAdminImportFile(c)
	if err != nil {
		response.Fail(c, err)
		return
	}
	res, err := h.imports.ImportTags(c.Request.Context(), actor, subjectID, data)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

func (h *AdminImportHandler) KnowledgePointTemplate(c *gin.Context) {
	if _, ok := middleware.MustUser(c); !ok {
		return
	}
	data, err := h.imports.KnowledgePointTemplate()
	if err != nil {
		response.Fail(c, apperr.ErrInternal.WithCause(err))
		return
	}
	writeXLSX(c, "knowledge_points_template.xlsx", data)
}

func (h *AdminImportHandler) KnowledgePointPreflight(c *gin.Context) {
	actor, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	subjectID, ok := parseID(c, "subject_id")
	if !ok {
		return
	}
	res, err := h.imports.KnowledgePointPreflight(c.Request.Context(), actor, subjectID)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

func (h *AdminImportHandler) ImportKnowledgePoints(c *gin.Context) {
	actor, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	subjectID, ok := parseID(c, "subject_id")
	if !ok {
		return
	}
	data, err := readAdminImportFile(c)
	if err != nil {
		response.Fail(c, err)
		return
	}
	mode := strings.TrimSpace(c.PostForm("mode"))
	if mode == "" {
		mode = "incremental"
	}
	res, err := h.imports.ImportKnowledgePoints(c.Request.Context(), actor, subjectID, mode, data)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

func readAdminImportFile(c *gin.Context) ([]byte, error) {
	file, err := c.FormFile("file")
	if err != nil {
		return nil, apperr.New(apperr.CodeInvalidParam, "请选择要导入的 .xlsx 文件")
	}
	if !strings.EqualFold(filepath.Ext(file.Filename), ".xlsx") {
		return nil, apperr.New(apperr.CodeInvalidParam, "仅支持 .xlsx 格式")
	}
	if file.Size > maxAdminImportBytes {
		return nil, apperr.New(apperr.CodeInvalidParam, "文件过大").WithDetail("文件上限 5 MB")
	}
	reader, err := file.Open()
	if err != nil {
		return nil, apperr.New(apperr.CodeInvalidParam, "无法读取上传文件")
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, maxAdminImportBytes+1))
	if err != nil {
		return nil, apperr.New(apperr.CodeInvalidParam, "无法读取上传文件")
	}
	if len(data) > maxAdminImportBytes {
		return nil, apperr.New(apperr.CodeInvalidParam, "文件过大").WithDetail("文件上限 5 MB")
	}
	return data, nil
}

func writeXLSX(c *gin.Context, filename string, data []byte) {
	c.Header("Content-Disposition", `attachment; filename="`+filename+`"`)
	c.Data(200, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", data)
}
