package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/NanamiKyoka/questionmind/internal/aiagent"
	"github.com/NanamiKyoka/questionmind/internal/middleware"
	"github.com/NanamiKyoka/questionmind/internal/pkg/pagination"
	"github.com/NanamiKyoka/questionmind/internal/pkg/response"
	"github.com/NanamiKyoka/questionmind/internal/schema"
	"github.com/NanamiKyoka/questionmind/internal/service"
)

// ChatHandler AI 助手接口。
type ChatHandler struct {
	chat *service.ChatService
}

func NewChatHandler(chat *service.ChatService) *ChatHandler { return &ChatHandler{chat: chat} }

// ---- 会话 ----

// ListSessions GET /chat/sessions
func (h *ChatHandler) ListSessions(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	page := pagination.Parse(c)
	var q struct {
		SubjectID uint `form:"subject_id"`
	}
	if !bindQuery(c, &q) {
		return
	}

	res, total, err := h.chat.ListSessions(c.Request.Context(), user, q.SubjectID, page.Limit(), page.Offset())
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.Page(c, res, total, page.Page, page.PageSize)
}

// CreateSession POST /chat/sessions
func (h *ChatHandler) CreateSession(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	var req schema.SessionCreateRequest
	if !bind(c, &req) {
		return
	}
	res, err := h.chat.CreateSession(c.Request.Context(), user, &req)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.Created(c, res)
}

// GetSession GET /chat/sessions/:id
func (h *ChatHandler) GetSession(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	res, err := h.chat.GetSession(c.Request.Context(), user, id)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// UpdateSession PATCH /chat/sessions/:id
func (h *ChatHandler) UpdateSession(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var req schema.SessionUpdateRequest
	if !bind(c, &req) {
		return
	}
	res, err := h.chat.UpdateSession(c.Request.Context(), user, id, &req)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// DeleteSession DELETE /chat/sessions/:id
func (h *ChatHandler) DeleteSession(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	if err := h.chat.DeleteSession(c.Request.Context(), user, id); err != nil {
		response.Fail(c, err)
		return
	}
	response.NoContent(c)
}

// ListMessages GET /chat/sessions/:id/messages
func (h *ChatHandler) ListMessages(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var q struct {
		Limit int `form:"limit"`
	}
	if !bindQuery(c, &q) {
		return
	}
	res, err := h.chat.ListMessages(c.Request.Context(), user, id, q.Limit)
	if err != nil {
		response.Fail(c, err)
		return
	}
	response.OK(c, res)
}

// SendMessage POST /chat/sessions/:id/messages
//
// 分两阶段：先用 PrepareSend 做校验，**失败时走普通 JSON 错误响应**
// （此时还没写流，状态码可以正常设置）；只有校验通过才切到 SSE 模式。
// 这样"未配置模型""会话不存在"这类问题前端能走统一的错误处理，
// 不必从 SSE 帧里刨错误信息。
func (h *ChatHandler) SendMessage(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	sessionID, ok := parseID(c, "id")
	if !ok {
		return
	}
	var req schema.SendMessageRequest
	if !bind(c, &req) {
		return
	}

	prepared, err := h.chat.PrepareSend(c.Request.Context(), user, sessionID, &req)
	if err != nil {
		response.Fail(c, err)
		return
	}

	// 校验通过，切到 SSE 模式。
	for k, v := range aiagent.SSEHeaders() {
		c.Header(k, v)
	}
	c.Status(http.StatusOK)

	flusher, canFlush := c.Writer.(http.Flusher)

	// sawDone 记录是否已经写出过终止帧。
	//
	// 运行时的 RunFailed 事件本身就会翻译成 error + done 两帧；
	// 如果 handler 在返错时再补一对，前端会看到两条错误、两个 done。
	// 终止帧只允许出现一次——前端拿它判断"流结束了"。
	var sawDone bool

	// writeRaw 写原始帧（用于运行之外的收尾帧）。
	writeRaw := func(frames ...aiagent.SSEFrame) error {
		for _, f := range frames {
			// 客户端断开时 ctx 会被取消，这里第一时间感知并停止生成，
			// 不必等模型把整段话说完。
			if err := c.Request.Context().Err(); err != nil {
				return err
			}
			if _, err := c.Writer.WriteString(f.Render()); err != nil {
				return err
			}
			if f.Event == aiagent.EventDone {
				sawDone = true
			}
		}
		if canFlush {
			flusher.Flush()
		}
		return nil
	}

	writeFrame := func(ev aiagent.Event) error {
		frames := aiagent.ToFrames(ev)
		if len(frames) == 0 {
			return nil
		}
		return writeRaw(frames...)
	}

	if err := h.chat.Execute(c.Request.Context(), user, prepared, writeFrame); err != nil {
		// 流已经开始，只能用帧报错。已经收尾过就不再重复补 done。
		if !sawDone {
			_ = writeRaw(
				aiagent.ErrorFrame(aiagent.HumanizeError(err)),
				aiagent.DoneFrame(prepared.RunID, "failed"),
			)
		}
	}
}

// ResolveClientTool POST /chat/runs/:run_id/tool-result
//
// 前端执行完客户端工具后回传结果，唤醒正在等待的后端协程。
func (h *ChatHandler) ResolveClientTool(c *gin.Context) {
	user, ok := middleware.MustUser(c)
	if !ok {
		return
	}
	runID := c.Param("run_id")
	if runID == "" {
		response.FailWith(c, 40001, "缺少 run_id")
		return
	}
	var req schema.ClientToolResultRequest
	if !bind(c, &req) {
		return
	}
	if err := h.chat.ResolveClientTool(c.Request.Context(), user, runID, &req); err != nil {
		response.Fail(c, err)
		return
	}
	response.NoContent(c)
}

// humanizeChatError 已并入 aiagent.HumanizeError —— 见该函数的注释。
