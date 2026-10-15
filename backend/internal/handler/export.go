package handler

import (
	"fmt"
	"net/url"

	"github.com/gin-gonic/gin"

	"github.com/NanamiKyoka/questionmind/internal/middleware"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
	"github.com/NanamiKyoka/questionmind/internal/pkg/response"
	"github.com/NanamiKyoka/questionmind/internal/service"
)

// ExportHandler 组卷导出接口。
type ExportHandler struct {
	exports *service.ExportService
}

func NewExportHandler(exports *service.ExportService) *ExportHandler {
	return &ExportHandler{exports: exports}
}

// ExportPaper GET /papers/:id/export?format=docx|latex
func (h *ExportHandler) ExportPaper(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	format, ok := parseFormat(c)
	if !ok {
		return
	}
	content, ok := parseContent(c)
	if !ok {
		return
	}

	res, err := h.exports.ExportPaper(c.Request.Context(), user, id, format, content)
	if err != nil {
		response.Fail(c, err)
		return
	}
	writeDownload(c, res)
}

// ExportVersion GET /papers/:id/versions/:version_no/export?format=docx|latex
func (h *ExportHandler) ExportVersion(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	versionNo, ok := parseID(c, "version_no")
	if !ok {
		return
	}
	format, ok := parseFormat(c)
	if !ok {
		return
	}
	content, ok := parseContent(c)
	if !ok {
		return
	}

	res, err := h.exports.ExportVersion(c.Request.Context(), user, id, int(versionNo), format, content)
	if err != nil {
		response.Fail(c, err)
		return
	}
	writeDownload(c, res)
}

func parseFormat(c *gin.Context) (service.ExportFormat, bool) {
	raw := c.Query("format")
	if raw == "" {
		raw = string(service.FormatDocx)
	}
	format := service.ExportFormat(raw)
	if !format.Valid() {
		response.Fail(c, apperr.New(apperr.CodeInvalidParam,
			"format 仅支持 docx 或 latex").WithDetail("format=%s", raw))
		return "", false
	}
	return format, true
}

// writeDownload 输出二进制附件。
//
// 文件名同时给出 ASCII 回退与 RFC 5987 的 UTF-8 形式：
// 中文标题直接放进 Content-Disposition 会被部分浏览器截断或乱码。
func parseContent(c *gin.Context) (service.ExportContent, bool) {
	raw := c.Query("content")
	if raw == "" {
		raw = string(service.ContentAll)
	}
	content := service.ExportContent(raw)
	if !content.Valid() {
		response.Fail(c, apperr.New(apperr.CodeInvalidParam,
			"content 仅支持 all、questions 或 answers").WithDetail("content=%s", raw))
		return "", false
	}
	return content, true
}

func writeDownload(c *gin.Context, res *service.ExportResult) {
	ascii := "paper." + extensionOf(res.Filename)
	utf8Name := url.PathEscape(res.Filename)

	c.Header("Content-Type", res.ContentType)
	c.Header("Content-Disposition",
		fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, ascii, utf8Name))
	c.Header("Content-Length", fmt.Sprintf("%d", len(res.Data)))
	c.Header("Cache-Control", "no-store")
	c.Data(200, res.ContentType, res.Data)
}

func extensionOf(name string) string {
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '.' {
			return name[i+1:]
		}
	}
	return "bin"
}
