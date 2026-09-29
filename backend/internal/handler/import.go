package handler

import (
	"io"

	"github.com/gin-gonic/gin"

	"github.com/NanamiKyoka/questionmind/internal/middleware"
	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
	"github.com/NanamiKyoka/questionmind/internal/pkg/pagination"
	"github.com/NanamiKyoka/questionmind/internal/pkg/response"
	"github.com/NanamiKyoka/questionmind/internal/schema"
	"github.com/NanamiKyoka/questionmind/internal/service"
)

// maxUploadBytes 单个上传文件的上限。
// 与 importing.MaxDocFileBytes 对齐：这里多读的那一字节是用来判断"超了"的，
// 不能因为读满了就以为刚好没超。
const maxUploadBytes = 32 << 20 // 32MB

// ImportHandler 智能录题接口。
type ImportHandler struct {
	imports *service.ImportService
}

func NewImportHandler(imports *service.ImportService) *ImportHandler {
	return &ImportHandler{imports: imports}
}

// Upload POST /subjects/:subject_id/imports/upload
//
// 表单字段：file（必填，单个）、method（ai|template）、mode（extract|solve）。
// 响应体与 /extract 完全一致，因此前端两条路径共用同一套审核界面。
func (h *ImportHandler) Upload(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	subjectID, ok := parseID(c, "subject_id")
	if !ok {
		return
	}

	fh, err := c.FormFile("file")
	if err != nil {
		response.Fail(c, apperr.New(apperr.CodeInvalidParam, "请选择要导入的文件"))
		return
	}
	f, err := fh.Open()
	if err != nil {
		response.Fail(c, apperr.New(apperr.CodeInvalidParam, "无法读取上传的文件"))
		return
	}
	defer f.Close()

	// LimitReader 多读 1 字节，用来分辨"刚好 32MB"与"超过 32MB"。
	data, err := io.ReadAll(io.LimitReader(f, maxUploadBytes+1))
	if err != nil {
		response.Fail(c, apperr.New(apperr.CodeInvalidParam, "无法读取上传的文件"))
		return
	}
	if len(data) > maxUploadBytes {
		response.Fail(c, apperr.New(apperr.CodeInvalidParam,
			"文件过大").WithDetail("单个文件上限 %d MB", maxUploadBytes>>20))
		return
	}

	res, err := h.imports.Upload(c.Request.Context(), user, subjectID,
		fh.Filename, data, c.PostForm("method"), c.PostForm("mode"))
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// Extract POST /subjects/:subject_id/imports/extract
//
// 返回草稿而不是直接入库：模型识别题目总会有出入，
// 必须经过人工审核这一步，否则错误答案会直接污染题库。
func (h *ImportHandler) Extract(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	subjectID, ok := parseID(c, "subject_id")
	if !ok {
		return
	}
	var req schema.ExtractRequest
	if !bind(c, &req) {
		return
	}
	res, err := h.imports.Extract(c.Request.Context(), user, subjectID, &req)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// Commit POST /subjects/:subject_id/imports/commit
func (h *ImportHandler) Commit(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	subjectID, ok := parseID(c, "subject_id")
	if !ok {
		return
	}
	var req schema.CommitRequest
	if !bind(c, &req) {
		return
	}
	res, err := h.imports.Commit(c.Request.Context(), user, subjectID, &req)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// ListTasks GET /subjects/:subject_id/imports
func (h *ImportHandler) ListTasks(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	subjectID, ok := parseID(c, "subject_id")
	if !ok {
		return
	}
	var q struct {
		Status string `form:"status"`
		// UserID 仅超级管理员有效：普通用户传了也会被服务端忽略，
		// 固定按"只看我自己的"过滤。
		UserID uint `form:"user_id"`
	}
	if !bindQuery(c, &q) {
		return
	}
	page := pagination.Parse(c)

	res, total, err := h.imports.ListTasks(c.Request.Context(), user, subjectID,
		model.ImportTaskStatus(q.Status), q.UserID, page.Limit(), page.Offset())
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.Page(c, res, total, page.Page, page.PageSize)
}

// CancelTask POST /subjects/:subject_id/imports/:task_id/cancel
//
// 语义是**放弃**而不是中断：抽取同步跑在请求里，服务端没有可抢占的执行体。
// 它把任务标记为已放弃并移出队列，不会终止任何正在进行的模型调用。
func (h *ImportHandler) CancelTask(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	subjectID, taskID, ok := parseTaskIDs(c)
	if !ok {
		return
	}
	res, err := h.imports.CancelTask(c.Request.Context(), user, subjectID, taskID)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// RetryTask POST /subjects/:subject_id/imports/:task_id/retry
//
// 把任务复位成"等待处理"并清空上一轮的报错。
// 没有后台 worker 时它不会自动重跑抽取——要立刻重跑请回到智能录题页。
func (h *ImportHandler) RetryTask(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	subjectID, taskID, ok := parseTaskIDs(c)
	if !ok {
		return
	}
	res, err := h.imports.RetryTask(c.Request.Context(), user, subjectID, taskID)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// DeleteTask DELETE /subjects/:subject_id/imports/:task_id
//
// 只删导入记录与其附件，**不动题目**：题目是内容，删除走题库页的回收站。
func (h *ImportHandler) DeleteTask(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	subjectID, taskID, ok := parseTaskIDs(c)
	if !ok {
		return
	}
	if err := h.imports.DeleteTask(c.Request.Context(), user, subjectID, taskID); err != nil {
		response.Fail(c, err)
		return
	}
	response.NoContent(c)
}

// QueueStatus GET /subjects/:subject_id/imports/queue-status
//
// 返回各状态计数与活动任务的 FIFO 位置，并如实告知"当前没有 worker 在消费队列"。
func (h *ImportHandler) QueueStatus(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	subjectID, ok := parseID(c, "subject_id")
	if !ok {
		return
	}
	res, err := h.imports.QueueStatus(c.Request.Context(), user, subjectID)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// TaskQuestions GET /subjects/:subject_id/imports/:task_id/questions
//
// 这次导入产生的题目。历史页的"查看题目"就是它。
func (h *ImportHandler) TaskQuestions(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	subjectID, taskID, ok := parseTaskIDs(c)
	if !ok {
		return
	}
	page := pagination.Parse(c)

	res, total, err := h.imports.TaskQuestions(c.Request.Context(), user, subjectID,
		taskID, page.Limit(), page.Offset())
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.Page(c, res, total, page.Page, page.PageSize)
}

// parseTaskIDs 同时取出路径上的学科 id 与任务 id。
func parseTaskIDs(c *gin.Context) (subjectID, taskID uint, ok bool) {
	subjectID, ok = parseID(c, "subject_id")
	if !ok {
		return 0, 0, false
	}
	taskID, ok = parseID(c, "task_id")
	if !ok {
		return 0, 0, false
	}
	return subjectID, taskID, true
}
