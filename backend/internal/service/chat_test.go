package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NanamiKyoka/questionmind/internal/aiagent"
	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/schema"
)

// fakeChatModel 模拟一个按脚本回应的模型端点。
//
// 用真实的 HTTP + SSE 而不是替换 Provider：这样连"请求形状、
// 流式分片、工具调用聚合"都一起验证到了。
func fakeChatModel(t *testing.T, turns []string) *httptest.Server {
	t.Helper()
	var call int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(atomic.AddInt32(&call, 1)) - 1
		w.Header().Set("Content-Type", "text/event-stream")
		if n >= len(turns) {
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		fmt.Fprint(w, turns[n])
	}))
	t.Cleanup(srv.Close)
	return srv
}

// textTurn 生成一段纯文本回应的 SSE。
func textTurn(text string) string {
	payload := strings.ReplaceAll(text, `"`, `\"`)
	return fmt.Sprintf("data: {\"choices\":[{\"delta\":{\"content\":\"%s\"}}]}\n\n"+
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"+
		"data: [DONE]\n\n", payload)
}

// toolTurn 生成一段发起工具调用的 SSE。
func toolTurn(id, name, args string) string {
	escaped := strings.ReplaceAll(args, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return fmt.Sprintf(
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"%s\",\"type\":\"function\","+
			"\"function\":{\"name\":\"%s\",\"arguments\":\"%s\"}}]}}]}\n\n"+
			"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n"+
			"data: [DONE]\n\n", id, name, escaped)
}

// chatFixture 搭好一套可用的对话环境。
type chatFixture struct {
	repos    *Repos
	svc      *ChatService
	subject  *model.Subject
	user     *model.User
	question *model.Question
}

func newChatFixture(t *testing.T, turns []string) *chatFixture {
	t.Helper()
	repos := newTestRepos(t)
	ctx := context.Background()

	admin := adminUser()
	subject := &model.Subject{Name: "数学", ReviewThreshold: 1, CreatedBy: admin.ID}
	if err := repos.Subjects.Create(ctx, subject); err != nil {
		t.Fatalf("建学科失败: %v", err)
	}
	if err := repos.Subjects.AddMember(ctx, subject.ID, admin.ID, model.SubjectRoleManager); err != nil {
		t.Fatalf("加成员失败: %v", err)
	}

	// 放一道题，供工具检索命中。
	question := &model.Question{
		SubjectID:       subject.ID,
		QuestionType:    model.QuestionTypeSingleChoice,
		Difficulty:      2,
		Status:          model.QuestionStatusPublished,
		Visibility:      model.VisibilityPublic,
		Stem:            mustJSONT(t, map[string]any{"type": "doc"}),
		Answer:          mustJSONT(t, model.AnswerSpec{Kind: "single", Correct: []string{}}),
		StemText:        "已知函数 f(x)=x^2，则 f(2) 的值为",
		ContentVersion:  1,
		ContentRevision: 1,
		CreatedBy:       admin.ID,
		UpdatedBy:       admin.ID,
	}
	if err := repos.Questions.Create(ctx, question); err != nil {
		t.Fatalf("建题失败: %v", err)
	}

	// 配置 AI 指向假端点。
	aiSvc := newAIService(t, repos)
	provider, err := aiSvc.CreateProvider(ctx, admin, &schema.ProviderCreateRequest{
		Name: "Fake", InterfaceType: "openai",
		BaseURL: fakeChatModel(t, turns).URL,
		APIKey:  "sk-test",
	})
	if err != nil {
		t.Fatalf("建供应商失败: %v", err)
	}
	if _, err := aiSvc.CreateModel(ctx, admin, &schema.ModelCreateRequest{
		ProviderID: provider.ID, Name: "fake", ModelType: string(model.AIModelText),
	}); err != nil {
		t.Fatalf("建模型失败: %v", err)
	}

	return &chatFixture{
		repos:    repos,
		svc:      newChatServiceForTest(t, repos, aiSvc),
		subject:  subject,
		user:     admin,
		question: question,
	}
}

func newChatServiceForTest(t *testing.T, repos *Repos, aiSvc *AIConfigService) *ChatService {
	t.Helper()
	return NewChatService(repos, aiSvc, ChatDeps{
		Questions:       NewQuestionService(repos),
		KnowledgePoints: NewKnowledgePointService(repos),
		Tags:            NewTagService(repos),
		Papers:          NewPaperService(repos),
		Subjects:        NewSubjectService(repos),
		Stats:           NewStatsService(repos),
	},
		aiagent.NewChannel(200*time.Millisecond),
		aiagent.Budget{MaxTurns: 4, MaxToolCalls: 5, WallClock: 10 * time.Second},
	)
}

func mustJSONT(t *testing.T, v any) model.JSON {
	t.Helper()
	j, err := model.JSONOf(v)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	return j
}

// ---- 会话管理 ----

func TestChatSessionLifecycle(t *testing.T) {
	fx := newChatFixture(t, []string{textTurn("你好")})
	ctx := context.Background()

	created, err := fx.svc.CreateSession(ctx, fx.user, &schema.SessionCreateRequest{
		SubjectID: &fx.subject.ID,
		Scene:     string(aiagent.SceneQuestionLibrary),
	})
	if err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	if created.Title != "新对话" {
		t.Fatalf("默认标题应为「新对话」，实际 %q", created.Title)
	}

	// 他人不应看到这个会话——返回 404 而不是 403，避免探测出 id 存在。
	other := &model.User{Base: model.Base{ID: 99}, Username: "other", IsActive: true}
	if _, err := fx.svc.GetSession(ctx, other, created.ID); err == nil {
		t.Fatal("他人不应能读取该会话")
	}

	title := "数学答疑"
	updated, err := fx.svc.UpdateSession(ctx, fx.user, created.ID, &schema.SessionUpdateRequest{Title: &title})
	if err != nil {
		t.Fatalf("改标题失败: %v", err)
	}
	if updated.Title != title {
		t.Fatalf("标题未更新: %q", updated.Title)
	}

	if err := fx.svc.DeleteSession(ctx, fx.user, created.ID); err != nil {
		t.Fatalf("删会话失败: %v", err)
	}
	if _, err := fx.svc.GetSession(ctx, fx.user, created.ID); err == nil {
		t.Fatal("删除后不应还能读到")
	}
}

func TestChatSessionRequiresSubjectAccess(t *testing.T) {
	fx := newChatFixture(t, []string{textTurn("x")})
	outsider := &model.User{Base: model.Base{ID: 50}, Username: "outsider", IsActive: true}
	if _, err := fx.svc.CreateSession(context.Background(), outsider, &schema.SessionCreateRequest{
		SubjectID: &fx.subject.ID,
	}); err == nil {
		t.Fatal("非成员不应能在该学科下建会话")
	}
}

// ---- 发送消息 ----

func TestChatSendMessageSimpleAnswer(t *testing.T) {
	fx := newChatFixture(t, []string{textTurn("你好，有什么可以帮你？")})
	ctx := context.Background()

	session, err := fx.svc.CreateSession(ctx, fx.user, &schema.SessionCreateRequest{
		SubjectID: &fx.subject.ID,
	})
	if err != nil {
		t.Fatalf("建会话失败: %v", err)
	}

	var text strings.Builder
	var gotDone bool
	err = fx.svc.SendMessage(ctx, fx.user, session.ID, &schema.SendMessageRequest{
		Content: "你好",
	}, func(ev aiagent.Event) error {
		switch e := ev.(type) {
		case *aiagent.TextDelta:
			text.WriteString(e.Text)
		case *aiagent.RunFinished:
			gotDone = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	if text.String() != "你好，有什么可以帮你？" {
		t.Fatalf("文本流错误: %q", text.String())
	}
	if !gotDone {
		t.Fatal("应发出结束事件")
	}

	// 消息应落库：用户一条 + 助手一条。
	messages, err := fx.svc.ListMessages(ctx, fx.user, session.ID, 50)
	if err != nil {
		t.Fatalf("读消息失败: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("应保存 2 条可见消息，实际 %d", len(messages))
	}
	if messages[0].Role != model.ChatRoleUser || messages[0].Content != "你好" {
		t.Fatalf("第一条应为用户消息: %+v", messages[0])
	}
	if messages[1].Role != model.ChatRoleAssistant || messages[1].Content != "你好，有什么可以帮你？" {
		t.Fatalf("第二条应为助手回答: %+v", messages[1])
	}

	// 首条消息应自动成为会话标题。
	got, _ := fx.svc.GetSession(ctx, fx.user, session.ID)
	if got.Title == "新对话" || !strings.Contains(got.Title, "你好") {
		t.Fatalf("首条消息应作为标题，实际 %q", got.Title)
	}
}

// 完整链路：模型调用工具 → 服务端执行 → 结果回喂 → 模型给出最终回答。
func TestChatSendMessageWithServerTool(t *testing.T) {
	fx := newChatFixture(t, []string{
		toolTurn("call_1", "search_questions", `{"keyword":"函数"}`),
		textTurn("我找到了一道相关题目。"),
	})
	ctx := context.Background()

	session, _ := fx.svc.CreateSession(ctx, fx.user, &schema.SessionCreateRequest{
		SubjectID: &fx.subject.ID,
		Scene:     string(aiagent.SceneQuestionLibrary),
	})

	var (
		sawToolCall   bool
		sawToolResult bool
		toolOutput    string
	)
	err := fx.svc.SendMessage(ctx, fx.user, session.ID, &schema.SendMessageRequest{
		Content: "帮我找一道关于函数的题",
		Scene:   string(aiagent.SceneQuestionLibrary),
	}, func(ev aiagent.Event) error {
		switch e := ev.(type) {
		case *aiagent.ToolCallStarted:
			if e.Name == "search_questions" {
				sawToolCall = true
			}
		case *aiagent.ToolCallFinished:
			sawToolResult = true
			toolOutput = e.Content
		}
		return nil
	})
	if err != nil {
		t.Fatalf("发送失败: %v", err)
	}

	if !sawToolCall || !sawToolResult {
		t.Fatalf("应发出工具开始与结束事件: call=%v result=%v", sawToolCall, sawToolResult)
	}
	// 工具真的查到了库里的题。
	if !strings.Contains(toolOutput, "已知函数") {
		t.Fatalf("工具应检索到题库中的题目，实际 %q", toolOutput)
	}

	// 可见消息只剩用户与助手两条：工具消息应被过滤掉。
	visible, _ := fx.svc.ListMessages(ctx, fx.user, session.ID, 50)
	for _, m := range visible {
		if m.Role == model.ChatRoleTool {
			t.Fatalf("工具消息不应出现在可见列表里: %+v", m)
		}
	}
	if len(visible) != 2 {
		t.Fatalf("应保存 2 条可见消息，实际 %d", len(visible))
	}

	// 但工具消息必须真的落库——多轮工具调用的上下文需要它。
	all, err := fx.repos.ChatMessages.RecentForContext(ctx, session.ID, 50)
	if err != nil {
		t.Fatalf("读上下文失败: %v", err)
	}
	var toolRows int
	for _, m := range all {
		if m.Role == model.ChatRoleTool {
			toolRows++
		}
	}
	if toolRows != 1 {
		t.Fatalf("应保存 1 条工具消息，实际 %d", toolRows)
	}
}

// 模型失败时，用户消息仍应保留，避免"白打一段话"。
func TestChatKeepsUserMessageOnModelFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"error":{"message":"boom"}}`)
	}))
	t.Cleanup(srv.Close)

	repos := newTestRepos(t)
	ctx := context.Background()
	admin := adminUser()
	subject := &model.Subject{Name: "数学", ReviewThreshold: 1, CreatedBy: admin.ID}
	repos.Subjects.Create(ctx, subject)
	repos.Subjects.AddMember(ctx, subject.ID, admin.ID, model.SubjectRoleManager)

	aiSvc := newAIService(t, repos)
	provider, _ := aiSvc.CreateProvider(ctx, admin, &schema.ProviderCreateRequest{
		Name: "Bad", InterfaceType: "openai", BaseURL: srv.URL, APIKey: "k",
	})
	aiSvc.CreateModel(ctx, admin, &schema.ModelCreateRequest{
		ProviderID: provider.ID, Name: "m", ModelType: string(model.AIModelText),
	})

	svc := newChatServiceForTest(t, repos, aiSvc)
	session, _ := svc.CreateSession(ctx, admin, &schema.SessionCreateRequest{SubjectID: &subject.ID})

	// 模型调用失败仍应把错误帧发出去（这里只验证事件层面）。
	var sawFailed bool
	_ = svc.SendMessage(ctx, admin, session.ID, &schema.SendMessageRequest{Content: "在吗"},
		func(ev aiagent.Event) error {
			if _, ok := ev.(*aiagent.RunFailed); ok {
				sawFailed = true
			}
			return nil
		})
	if !sawFailed {
		t.Fatal("模型失败应发出失败事件")
	}

	// 关键：用户消息不能因为模型失败而丢失。
	messages, _ := svc.ListMessages(ctx, admin, session.ID, 50)
	if len(messages) != 1 || messages[0].Content != "在吗" {
		t.Fatalf("用户消息应保留，实际 %+v", messages)
	}
}

func TestChatRejectsEmptyMessage(t *testing.T) {
	fx := newChatFixture(t, []string{textTurn("x")})
	ctx := context.Background()
	session, _ := fx.svc.CreateSession(ctx, fx.user, &schema.SessionCreateRequest{SubjectID: &fx.subject.ID})

	for _, req := range []*schema.SendMessageRequest{
		{Content: ""},
		{Content: "   "},
	} {
		if err := fx.svc.SendMessage(ctx, fx.user, session.ID, req, func(aiagent.Event) error { return nil }); err == nil {
			t.Fatalf("空消息应被拒绝: %+v", req)
		}
	}
}

// 未配置模型时，错误必须发生在开始写流之前（否则前端拿到的是半个流）。
func TestChatWithoutModelConfigured(t *testing.T) {
	repos := newTestRepos(t)
	ctx := context.Background()

	admin := adminUser()
	subject := &model.Subject{Name: "数学", ReviewThreshold: 1, CreatedBy: admin.ID}
	repos.Subjects.Create(ctx, subject)
	repos.Subjects.AddMember(ctx, subject.ID, admin.ID, model.SubjectRoleManager)

	aiSvc := newAIService(t, repos)
	svc := newChatServiceForTest(t, repos, aiSvc)
	session, _ := svc.CreateSession(ctx, admin, &schema.SessionCreateRequest{SubjectID: &subject.ID})

	err := svc.SendMessage(ctx, admin, session.ID, &schema.SendMessageRequest{Content: "你好"},
		func(aiagent.Event) error { return nil })
	if err == nil {
		t.Fatal("未配置模型应报错")
	}
	if !strings.Contains(err.Error(), "文本模型") {
		t.Fatalf("错误信息应指明缺少文本模型: %v", err)
	}
	// 失败在写流之前，因此不应有任何消息落库。
	messages, _ := svc.ListMessages(ctx, admin, session.ID, 50)
	if len(messages) != 0 {
		t.Fatalf("未开始运行就不应留下消息，实际 %d 条", len(messages))
	}
}

// ---- 工具注册表 ----

func TestChatToolRegistryIsWired(t *testing.T) {
	fx := newChatFixture(t, []string{textTurn("x")})
	names := fx.svc.Registry().Names()
	want := []string{
		"add_questions_to_paper", "archive_question", "create_paper", "create_question",
		"delete_question", "edit_paper", "get_paper", "get_question", "get_subject_stats",
		"list_knowledge_points", "list_papers", "list_review_queue", "list_subjects",
		"list_tags", "propose_questions", "read_paper_outline", "restore_question",
		"search_questions", "semantic_search_knowledge_points", "set_question_knowledge_points",
		"set_question_tags", "submit_question", "update_question",
	}
	if len(names) != len(want) {
		t.Fatalf("工具数量不符，期望 %d 个，实际 %d 个: %v", len(want), len(names), names)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("工具名不符，期望 %v，实际 %v", want, names)
		}
	}
}

// 组卷场景下应能看到客户端工具；题库场景下不应看到。
func TestChatToolScenes(t *testing.T) {
	fx := newChatFixture(t, []string{textTurn("x")})
	reg := fx.svc.Registry()

	editorSchemas := reg.Schemas(aiagent.SceneCompositionEditor, true)
	var hasClient bool
	for _, s := range editorSchemas {
		if s.Name == "edit_paper" {
			hasClient = true
		}
	}
	if !hasClient {
		t.Fatal("组卷场景应暴露 edit_paper")
	}

	for _, s := range reg.Schemas(aiagent.SceneQuestionLibrary, true) {
		if s.Name == "edit_paper" {
			t.Fatal("题库场景不应暴露 edit_paper")
		}
	}
}
