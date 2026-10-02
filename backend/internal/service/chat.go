package service

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/NanamiKyoka/questionmind/internal/ai"
	"github.com/NanamiKyoka/questionmind/internal/aiagent"
	"github.com/NanamiKyoka/questionmind/internal/importing"
	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
	"github.com/NanamiKyoka/questionmind/internal/repository"
	"github.com/NanamiKyoka/questionmind/internal/schema"
)

// chatContextLimit 送进模型的最近消息条数。
//
// 取 30 条而不是全部：更早的对话对当前问题帮助有限，
// 全量载入只会增加成本与延迟，还会把关键信息稀释掉。
const chatContextLimit = 30

// ChatService AI 助手用例。
type ChatService struct {
	repos    *Repos
	aiConfig *AIConfigService
	channel  *aiagent.Channel
	registry *aiagent.Registry
	budget   aiagent.Budget
	store    *repository.AgentRunRepo
}

// ChatDeps 聊天与 Agent 工具运行所需的业务服务。
//
// 用结构体而不是一串位置参数：工具集在扩充，每加一个能力就要给构造函数
// 多添一个参数，位置参数迟早会错位，而错位在这里只是"某个工具拿到 nil"。
type ChatDeps struct {
	Questions       *QuestionService
	KnowledgePoints *KnowledgePointService
	Tags            *TagService
	Papers          *PaperService
	Subjects        *SubjectService
	Stats           *StatsService
	Vectors         *VectorService
	Imports         *ImportService
}

// NewChatService 构造。
func NewChatService(
	repos *Repos,
	aiConfig *AIConfigService,
	deps ChatDeps,
	channel *aiagent.Channel,
	budget aiagent.Budget,
) *ChatService {
	return &ChatService{
		repos:    repos,
		aiConfig: aiConfig,
		channel:  channel,
		budget:   budget.Normalize(),
		store:    repository.NewAgentRunRepo(repos.Tx.DB()),
		registry: buildToolRegistry(toolDeps{
			questions:       deps.Questions,
			knowledgePoints: deps.KnowledgePoints,
			tags:            deps.Tags,
			papers:          deps.Papers,
			subjects:        deps.Subjects,
			stats:           deps.Stats,
			vectors:         deps.Vectors,
			imports:         deps.Imports,
		}),
	}
}

// Channel 暴露前端工具通道，供回传接口使用。
func (s *ChatService) Channel() *aiagent.Channel { return s.channel }

// Registry 暴露工具注册表，供自检与测试使用。
func (s *ChatService) Registry() *aiagent.Registry { return s.registry }

// ---- 会话 ----

func (s *ChatService) ListSessions(ctx context.Context, user *model.User, subjectID uint, limit, offset int) ([]schema.SessionResponse, int64, error) {
	list, total, err := s.repos.ChatSessions.ListByUser(ctx, user.ID, subjectID, limit, offset)
	if err != nil {
		return nil, 0, apperr.ErrInternal.WithCause(err)
	}
	out := make([]schema.SessionResponse, 0, len(list))
	for i := range list {
		out = append(out, schema.NewSessionResponse(&list[i]))
	}
	return out, total, nil
}

func (s *ChatService) CreateSession(ctx context.Context, user *model.User, req *schema.SessionCreateRequest) (*schema.SessionResponse, error) {
	if req.SubjectID != nil {
		// 建会话也要有该学科的访问权，否则会话会挂在一个用户看不到的学科上。
		if err := authorizeRead(user, *req.SubjectID); err != nil {
			return nil, err
		}
	}

	session := &model.ChatSession{
		UserID:    user.ID,
		SubjectID: req.SubjectID,
		Title:     strings.TrimSpace(req.Title),
		Scene:     string(aiagent.ParseScene(req.Scene)),
	}
	if session.Title == "" {
		session.Title = "新对话"
	}
	if err := s.repos.ChatSessions.Create(ctx, session); err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	resp := schema.NewSessionResponse(session)
	return &resp, nil
}

// GetSession 读取会话，并校验归属。
func (s *ChatService) GetSession(ctx context.Context, user *model.User, id uint) (*schema.SessionResponse, error) {
	session, err := s.ownedSession(ctx, user, id)
	if err != nil {
		return nil, err
	}
	resp := schema.NewSessionResponse(session)
	return &resp, nil
}

func (s *ChatService) UpdateSession(ctx context.Context, user *model.User, id uint, req *schema.SessionUpdateRequest) (*schema.SessionResponse, error) {
	if _, err := s.ownedSession(ctx, user, id); err != nil {
		return nil, err
	}
	fields := map[string]any{}
	if req.Title != nil {
		title := strings.TrimSpace(*req.Title)
		if title == "" {
			return nil, apperr.New(apperr.CodeInvalidParam, "标题不能为空")
		}
		fields["title"] = title
	}
	if err := s.repos.ChatSessions.UpdateFields(ctx, id, fields); err != nil {
		return nil, apperr.From(err)
	}
	return s.GetSession(ctx, user, id)
}

func (s *ChatService) DeleteSession(ctx context.Context, user *model.User, id uint) error {
	if _, err := s.ownedSession(ctx, user, id); err != nil {
		return err
	}
	if err := s.repos.ChatSessions.SoftDelete(ctx, id); err != nil {
		return apperr.ErrInternal.WithCause(err)
	}
	return nil
}

func (s *ChatService) ListMessages(ctx context.Context, user *model.User, sessionID uint, limit int) ([]schema.MessageResponse, error) {
	if _, err := s.ownedSession(ctx, user, sessionID); err != nil {
		return nil, err
	}
	list, err := s.repos.ChatMessages.ListVisible(ctx, sessionID, limit)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	out := make([]schema.MessageResponse, 0, len(list))
	for i := range list {
		out = append(out, schema.NewMessageResponse(&list[i]))
	}
	return out, nil
}

// ownedSession 取会话并确认它属于当前用户。
//
// 会话是私人物品，他人的会话一律按"不存在"处理（返回 404 而不是 403），
// 避免通过错误码探测出"这个 id 存在"。
func (s *ChatService) ownedSession(ctx context.Context, user *model.User, id uint) (*model.ChatSession, error) {
	session, err := s.repos.ChatSessions.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if session.UserID != user.ID && !user.IsSuperuser {
		return nil, apperr.ErrNotFound
	}
	return session, nil
}

// ---- 发送消息 ----

// PreparedChat 一次已校验、待执行的对话请求。
//
// 之所以把「校验」与「执行」拆开：执行阶段一旦开始写 SSE 流，
// HTTP 状态码就已经发出去了，业务错误再也没法用普通 JSON 返回。
// 未配置模型、无权限、消息为空这类问题必须在写流**之前**解决，
// 前端才能走统一的错误处理（401 跳登录、403 弹提示……），
// 而不是从一堆 SSE 帧里刨错误信息。
type PreparedChat struct {
	Resolved *ResolvedModel
	Messages []ai.Message
	Scene    aiagent.Scene

	SessionID uint
	SubjectID uint
	RunID     string

	SystemPrompt string
	MaxTokens    int
}

// PrepareSend 校验请求并准备执行所需的全部上下文。
//
// 返回错误时调用方应当用普通 JSON 响应返回；只有成功之后，
// 调用方才可以切换到 SSE 模式。
func (s *ChatService) PrepareSend(
	ctx context.Context,
	user *model.User,
	sessionID uint,
	req *schema.SendMessageRequest,
) (*PreparedChat, error) {
	session, err := s.ownedSession(ctx, user, sessionID)
	if err != nil {
		return nil, err
	}

	content := strings.TrimSpace(req.Content)
	if content == "" && len(req.Images) == 0 {
		return nil, apperr.New(apperr.CodeInvalidParam, "消息内容不能为空")
	}
	if err := validateImages(req.Images); err != nil {
		return nil, err
	}

	// 解析模型：未配置时在这里就失败，走普通错误响应。
	resolved, err := s.aiConfig.Resolve(ctx, model.AIModelText)
	if err != nil {
		return nil, err
	}

	subjectID := uint(0)
	if session.SubjectID != nil {
		subjectID = *session.SubjectID
	}

	// 先落库用户消息：即使后面的模型调用失败，用户说的话也不会丢。
	userMsg := &model.ChatMessage{
		SessionID: sessionID,
		Role:      model.ChatRoleUser,
		Content:   content,
	}
	if len(req.Images) > 0 {
		userMsg.Attachments = model.MustJSONOf(req.Images)
	}
	if err := s.repos.ChatMessages.Create(ctx, userMsg); err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}

	scene := aiagent.ParseScene(req.Scene)
	if scene == aiagent.SceneUnscoped && session.Scene != "" {
		scene = aiagent.ParseScene(session.Scene)
	}

	// 首条消息用作会话标题，省得用户手动改。
	if session.MessageCount == 0 && strings.TrimSpace(session.Title) == "新对话" {
		title := content
		if title == "" {
			title = "图片提问"
		}
		_ = s.repos.ChatSessions.UpdateFields(ctx, sessionID, map[string]any{
			"title": truncateRunes(title, 30),
		})
	}
	if session.Scene == "" && scene != aiagent.SceneUnscoped {
		_ = s.repos.ChatSessions.UpdateFields(ctx, sessionID, map[string]any{"scene": string(scene)})
	}

	messages, err := s.buildContext(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	// 把本次的图片附到最后一条用户消息上（上下文里存的是历史，不含本次图片）。
	if len(req.Images) > 0 && len(messages) > 0 {
		messages[len(messages)-1].Images = req.Images
	}

	return &PreparedChat{
		Resolved:     resolved,
		Messages:     messages,
		Scene:        scene,
		SessionID:    sessionID,
		SubjectID:    subjectID,
		RunID:        newUUID(),
		SystemPrompt: s.systemPrompt(ctx, scene, subjectID),
		MaxTokens:    resolved.MaxTokens,
	}, nil
}

// Execute 执行一次对话并把事件交给 emit。
//
// 走到这一步通常只会出现运行期错误（模型报错、超时），
// 由调用方通过错误帧告知前端。
func (s *ChatService) Execute(
	ctx context.Context,
	user *model.User,
	prepared *PreparedChat,
	emit func(aiagent.Event) error,
) error {
	runtime := aiagent.NewRuntime(prepared.Resolved.Provider, s.registry, s.channel, s.budget).
		WithStore(runStoreAdapter{s.store}, &prepared.Resolved.ModelID, prepared.Resolved.ProviderName)

	persist := &messagePersister{
		repos:     s.repos,
		sessionID: prepared.SessionID,
		runID:     prepared.RunID,
	}

	runCtx, cancel := context.WithTimeout(ctx, s.budget.WallClock+30*time.Second)
	defer cancel()

	runErr := runtime.Run(runCtx, aiagent.RunRequest{
		RunID:        prepared.RunID,
		SessionID:    &prepared.SessionID,
		Model:        prepared.Resolved.ModelName,
		SystemPrompt: prepared.SystemPrompt,
		Messages:     prepared.Messages,
		Scene:        prepared.Scene,
		Call: aiagent.ToolCall{
			User:      user,
			SubjectID: prepared.SubjectID,
		},
		MaxTokens: prepared.MaxTokens,
	}, func(ev aiagent.Event) error {
		persist.handle(ev)
		return emit(ev)
	})

	persist.flush()

	// 更新会话活跃时间（+2 计入了本轮的用户与助手两条消息）。
	_ = s.repos.ChatSessions.Touch(ctx, prepared.SessionID, time.Now().UTC(), 2)

	return runErr
}

// SendMessage 是 PrepareSend + Execute 的便捷组合，供不需要区分
// 「校验」与「执行」两个阶段的调用方（如测试、后台任务）使用。
func (s *ChatService) SendMessage(
	ctx context.Context,
	user *model.User,
	sessionID uint,
	req *schema.SendMessageRequest,
	emit func(aiagent.Event) error,
) error {
	prepared, err := s.PrepareSend(ctx, user, sessionID, req)
	if err != nil {
		return err
	}
	return s.Execute(ctx, user, prepared, emit)
}

// buildContext 把历史消息转换成模型消息序列。
//
// 包含 role=tool 的记录：多轮工具调用的上下文必须完整，
// 否则模型会看到"助手说要用工具、却没有工具结果"的断链。
func (s *ChatService) buildContext(ctx context.Context, sessionID uint) ([]ai.Message, error) {
	history, err := s.repos.ChatMessages.RecentForContext(ctx, sessionID, chatContextLimit)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}

	out := make([]ai.Message, 0, len(history))
	for i := range history {
		m := &history[i]
		msg := ai.Message{
			Role:       ai.Role(m.Role),
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
		}
		if !m.ToolCalls.IsEmpty() {
			var records []aiagent.ToolCallRecord
			if err := m.ToolCalls.Into(&records); err == nil {
				for _, r := range records {
					msg.ToolCalls = append(msg.ToolCalls, ai.ToolCall{
						ID: r.ID, Name: r.Name, Arguments: r.Arguments,
					})
				}
			}
		}
		out = append(out, msg)
	}
	return out, nil
}

// systemPrompt 助手的人设与行为约束。
//
// 走 importing 的解析链：学科覆盖 → 全局模板 → 内置默认（importing/prompt.go）。
// 没配模板时拿到的正文与改造前的硬编码版本逐字一致，因此"接上配置"这件事
// 本身不会改变任何一次对话的行为。
//
// 解析链内部已经保证不会失败：读不到模板、模板渲染为空，一律回落到内置默认。
// 这里因此没有 error 返回——一轮对话不该因为一段提示词发不出去。
func (s *ChatService) systemPrompt(ctx context.Context, scene aiagent.Scene, subjectID uint) string {
	// 三个键必须先占位再决定填不填值：渲染规则是"取值为空就删掉整行"，
	// 而键缺失会被当成未知占位符原样留在提示词里。少了这一步，
	// 没有学科的会话就会把 {{subject_name}} 原样发给模型。
	vars := importing.Vars{
		"subject_name":        "",
		"subject_description": "",
		"scene_hint":          sceneHint(scene),
	}

	// 学科名与说明是可选的：会话可以没有学科（跨学科提问），
	// 因此这里刻意吞掉读取错误，只在没有学科上下文时少两个占位符的取值。
	var subjectIDPtr *uint
	if subjectID > 0 {
		subjectIDPtr = &subjectID
		if subject, err := s.repos.Subjects.Get(ctx, subjectID); err == nil {
			vars["subject_name"] = subject.Name
			vars["subject_description"] = importing.DescribeSubject(subject.Description)
		} else {
			slog.Warn("读取学科信息失败，助手提示词将缺少学科上下文",
				"subject_id", subjectID, "error", err)
		}
	}

	return importing.ResolveSystemPrompt(ctx, importing.PromptKeyAssistant, subjectIDPtr, vars).Text
}

// sceneHint 页面场景的一段提示。
//
// 单独拆出来是因为它属于**请求**而不是提示词模板：场景由客户端上报、
// 每一轮都可能不同，不该被管理员写死在模板里。可配置模板通过
// {{scene_hint}} 拿到它，因此管理员依然能决定这段话出现在什么位置。
func sceneHint(scene aiagent.Scene) string {
	switch scene {
	case aiagent.SceneCompositionEditor:
		return "当前用户在组卷编辑器里。用户的卷子可能有未保存的改动，" +
			"要改动试卷结构时请使用客户端工具，这样改动会体现在用户眼前的编辑器里。"
	case aiagent.SceneImportReview:
		return "当前用户在审核导入的题目。请帮助核对题干、选项与答案是否合理。"
	case aiagent.SceneQuestionLibrary:
		return "当前用户在题库页面。请帮助检索、筛选和理解题目。"
	}
	return ""
}

// ResolveClientTool 处理前端的工具结果回传。
func (s *ChatService) ResolveClientTool(ctx context.Context, user *model.User, runID string, req *schema.ClientToolResultRequest) error {
	if s.channel == nil || !s.channel.Enabled() {
		return apperr.New(apperr.CodeUnprocessable, "当前部署不支持客户端工具")
	}

	ok := true
	if req.OK != nil {
		ok = *req.OK
	}
	payload := map[string]any{
		"ok":      ok,
		"content": req.Content,
	}
	if req.Data != nil {
		payload["data"] = req.Data
	}

	if !s.channel.Resolve(req.Ticket, runID, payload) {
		// 票据不存在、已兑现或不属于该 run。常见原因是超时后前端才回传——
		// 这不算错误，但要明确告知，免得前端反复重试。
		return apperr.New(apperr.CodeNotFound,
			"该工具请求已结束或已超时，结果未被采纳")
	}
	return nil
}

// ---- 落库适配 ----

// runStoreAdapter 把 repository 的入参类型适配成 aiagent 的接口。
//
// 这样 repository 不必依赖 aiagent 包，aiagent 也不必知道表结构。
type runStoreAdapter struct {
	repo *repository.AgentRunRepo
}

func (a runStoreAdapter) StartRun(ctx context.Context, run *aiagent.RunRecord) error {
	return a.repo.StartRun(ctx, &repository.AgentRunRecord{
		ID:           run.ID,
		SessionID:    run.SessionID,
		UserID:       run.UserID,
		SubjectID:    run.SubjectID,
		Surface:      run.Surface,
		ModelID:      run.ModelID,
		ModelName:    run.ModelName,
		ProviderName: run.ProviderName,
	})
}

func (a runStoreAdapter) AddStep(ctx context.Context, step *aiagent.StepRecord) error {
	return a.repo.AddStep(ctx, &repository.AgentStepRecord{
		RunID:     step.RunID,
		Idx:       step.Idx,
		StepType:  step.StepType,
		ToolName:  step.ToolName,
		Args:      step.Args,
		Result:    step.Result,
		LatencyMs: step.LatencyMs,
	})
}

func (a runStoreAdapter) FinishRun(ctx context.Context, runID string, status model.AgentRunStatus, stopReason, errMsg string, usage ai.Usage) error {
	return a.repo.FinishRun(ctx, runID, status, stopReason, errMsg, usage)
}

// messagePersister 把运行事件落成会话消息。
//
// 落库策略：
//   - AssistantTurn → 一条 assistant 消息（带工具调用记录）；
//     没有文字时也存，因为多轮工具上下文需要它才能连贯。
//   - ToolCallFinished → 一条 role=tool 消息，回填工具结果。
//   - RunFinished → 最终回答（只含最后一轮文字，不含中间轮）。
type messagePersister struct {
	repos     *Repos
	sessionID uint
	runID     string

	pendingTools map[string]*model.ChatMessage
	finalText    string
}

func (p *messagePersister) handle(ev aiagent.Event) {
	switch e := ev.(type) {
	case *aiagent.AssistantTurn:
		msg := &model.ChatMessage{
			SessionID:  p.sessionID,
			Role:       model.ChatRoleAssistant,
			Content:    e.Text,
			AgentRunID: &p.runID,
		}
		if len(e.ToolCalls) > 0 {
			msg.ToolCalls = model.MustJSONOf(e.ToolCalls)
		}
		_ = p.repos.ChatMessages.Create(context.Background(), msg)

	case *aiagent.ToolCallFinished:
		if p.pendingTools == nil {
			p.pendingTools = map[string]*model.ChatMessage{}
		}
		msg := &model.ChatMessage{
			SessionID:  p.sessionID,
			Role:       model.ChatRoleTool,
			Content:    e.Content,
			ToolCallID: e.ToolCallID,
			AgentRunID: &p.runID,
		}
		p.pendingTools[e.ToolCallID] = msg

	case *aiagent.RunFinished:
		p.finalText = e.Text

	case *aiagent.Usage:
		// 用量在 finish 时统一写入 agent_runs，这里不处理。
	}
}

// flush 把累积的工具结果与最终回答写库。
//
// 单独一步而不是在事件回调里直接写：事件回调位于 SSE 推送路径上，
// 每次写库都会阻塞下一个 token 的推送。集中到末尾写，
// 用户看到的流式体验不受数据库延迟影响。
func (p *messagePersister) flush() {
	for _, msg := range p.pendingTools {
		_ = p.repos.ChatMessages.Create(context.Background(), msg)
	}
	p.pendingTools = nil

	if strings.TrimSpace(p.finalText) == "" {
		return
	}
	msg := &model.ChatMessage{
		SessionID:  p.sessionID,
		Role:       model.ChatRoleAssistant,
		Content:    p.finalText,
		AgentRunID: &p.runID,
	}
	_ = p.repos.ChatMessages.Create(context.Background(), msg)
}
