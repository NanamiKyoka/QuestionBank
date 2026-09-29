package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/NanamiKyoka/questionmind/internal/middleware"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
	"github.com/NanamiKyoka/questionmind/internal/pkg/response"
	"github.com/NanamiKyoka/questionmind/internal/service"
)

// UploadHandler 文件上传接口。
type UploadHandler struct {
	uploads *service.UploadService
}

func NewUploadHandler(uploads *service.UploadService) *UploadHandler {
	return &UploadHandler{uploads: uploads}
}

// UploadImages POST /uploads/images
//
// multipart/form-data，字段名 files（可重复）或 file（单张）。
func (h *UploadHandler) UploadImages(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}

	form, err := c.MultipartForm()
	if err != nil {
		response.Fail(c, apperr.New(apperr.CodeInvalidParam, "表单解析失败").
			WithDetail("请以 multipart/form-data 提交，字段名为 files"))
		return
	}

	// 同时接受 files 与 file 两个字段名：单张上传时用 file 更自然，
	// 而多张上传用 files。兼容两者比强制前端记一个名字更省事。
	files := form.File["files"]
	if len(files) == 0 {
		files = form.File["file"]
	}
	if len(files) == 0 {
		response.Fail(c, apperr.New(apperr.CodeInvalidParam, "没有收到文件").
			WithDetail("字段名应为 files 或 file"))
		return
	}

	saved, err := h.uploads.UploadImages(c.Request.Context(), user, files)
	if err != nil {
		response.Fail(c, err)
		return
	}

	// 单张时同时给出顶层 url，前端不必再解一层数组。
	if len(saved) == 1 {
		response.Created(c, gin.H{
			"url":           saved[0].URL,
			"mime":          saved[0].Mime,
			"width":         saved[0].Width,
			"height":        saved[0].Height,
			"size":          saved[0].Size,
			"original_name": saved[0].OriginalName,
			"images":        saved,
		})
		return
	}
	response.Created(c, gin.H{"images": saved, "count": len(saved)})
}

// DeleteImage DELETE /uploads/images?url=/media/...
//
// 用查询参数而不是路径参数：媒体地址本身含斜杠，
// 放进路径段需要额外的编码约定，查询串更直接。
func (h *UploadHandler) DeleteImage(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	url := c.Query("url")
	if err := h.uploads.RemoveImage(c.Request.Context(), user, url); err != nil {
		response.Fail(c, err)
		return
	}
	response.NoContent(c)
}
