package aiagent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NanamiKyoka/questionmind/internal/ai"
	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
)

// ---- 测试替身 ----

type fakeTurn struct {
	text      string
	toolCalls []ai.ToolCall
	usage     *ai.Usage
	err       error
	// delay 模拟一次慢调用：墙钟预算这类"真实时间才推进"的限制，
	// 用零耗时的替身根本测不出来（时钟分辨率会让断言变成碰运气）。
	delay time.Duration
}

type fakeProvider struct {
	mu    sync.Mutex
	turns []fakeTurn
	idx   int
	// seen 记录每次调用收到的工具定义，便于断言场景过滤。
	seen [][]ai.ToolSpec
}

func (f *fakeProvider) Name() string { return "fake" }

func (f *fakeProvider) ChatStream(_ context.Context, req ai.ChatRequest, emit func(ai.Chunk) error) error {
	f.mu.Lock()
	f.seen = append(f.seen, req.Tools)
	var turn fakeTurn
	if f.idx < len(f.turns) {
		turn = f.turns[f.idx]
	}
	f.idx++
	f.mu.Unlock()

	if turn.err != nil {
		return turn.err
	}
	if turn.delay > 0 {
		time.Sleep(turn.delay)
	}
	// 逐字发出，模拟真实的流式分片。
	for _, r := range turn.text {
		if err := emit(ai.Chunk{Text: string(r)}); err != nil {
			return err
		}
	}
	for i := range turn.toolCalls {
		if err := emit(ai.Chunk{ToolCall: &turn.toolCalls[i]}); err != nil {
			return err
		}
	}
	if turn.usage != nil {
		if err := emit(ai.Chunk{Usage: turn.usage}); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeProvider) ExtractQuestions(context.Context, ai.ExtractRequest) ([]ai.ExtractedQuestion, error) {
	return nil, nil
}

func (f *fakeProvider) Embed(context.Context, string, []string) ([][]float32, error) {
	return nil, nil
}

func (f *fakeProvider) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.seen)
}

func testUser() *model.User {
	return &model.User{Base: model.Base{ID: 1}, Username: "tester", IsSuperuser: true, IsActive: true}
}

// ---- Channel ----

func TestChannelRoundTrip(t *testing.T) {
	ch := NewChannel(time.Second)
	ticket := ch.Open("run-1")

	go func() {
		time.Sleep(10 * time.Millisecond)
		if !ch.Resolve(ticket, "run-1", map[string]any{"ok": true, "content": "做好了"}) {
			t.Error("回传应成功")
		}
	}()

	payload, ok := ch.Wait(context.Background(), ticket)
	if !ok {
		t.Fatal("应等到回传")
	}
	if payload["content"] != "做好了" {
		t.Fatalf("回传内容错误: %v", payload)
	}
	if ch.PendingCount() != 0 {
		t.Fatal("等待结束后应清理条目，否则会永久泄漏")
	}
}

// 回传可能早于等待方进入 Wait（生成器 yield 事件后需要消费者再驱动一次），
// 这种时序必须能正确处理，否则那次回传会丢失并白等一个超时。
func TestChannelResolveBeforeWait(t *testing.T) {
	ch := NewChannel(time.Second)
	ticket := ch.Open("run-1")

	if !ch.Resolve(ticket, "run-1", map[string]any{"content": "提前回传"}) {
		t.Fatal("提前回传应成功")
	}

	payload, ok := ch.Wait(context.Background(), ticket)
	if !ok {
		t.Fatal("提前回传的结果应仍能被取到")
	}
	if payload["content"] != "提前回传" {
		t.Fatalf("内容错误: %v", payload)
	}
}

// 票据必须属于同一个 run，防止串话或伪造。
func TestChannelRejectsMismatchedRunID(t *testing.T) {
	ch := NewChannel(time.Second)
	ticket := ch.Open("run-1")

	if ch.Resolve(ticket, "run-2", map[string]any{"content": "x"}) {
		t.Fatal("不同 run 的回传应被拒绝")
	}
	if ch.Resolve(ticket, "run-1", map[string]any{"ok": true}) != true {
		t.Fatal("正确的 run 应被接受")
	}
}

func TestChannelRejectsUnknownTicket(t *testing.T) {
	ch := NewChannel(time.Second)
	if ch.Resolve("不存在的票据", "run-1", nil) {
		t.Fatal("未知票据应被拒绝")
	}
}

// 一次性语义：第二次回传不应覆盖第一次的结果。
func TestChannelResolveIsOnce(t *testing.T) {
	ch := NewChannel(time.Second)
	ticket := ch.Open("run-1")

	if !ch.Resolve(ticket, "run-1", map[string]any{"content": "第一次"}) {
		t.Fatal("首次回传应成功")
	}
	if ch.Resolve(ticket, "run-1", map[string]any{"content": "第二次"}) {
		t.Fatal("重复回传应被拒绝")
	}

	payload, ok := ch.Wait(context.Background(), ticket)
	if !ok || payload["content"] != "第一次" {
		t.Fatalf("应保留首次结果，实际 %v", payload)
	}
}

func TestChannelTimeout(t *testing.T) {
	ch := NewChannel(30 * time.Millisecond)
	ticket := ch.Open("run-1")

	started := time.Now()
	if _, ok := ch.Wait(context.Background(), ticket); ok {
		t.Fatal("超时应返回 false")
	}
	if time.Since(started) > time.Second {
		t.Fatal("超时应按时返回，不能挂死")
	}
	// 超时后条目必须清理，否则长时间运行会持续泄漏。
	if ch.PendingCount() != 0 {
		t.Fatal("超时后应清理条目")
	}
}

func TestChannelContextCancel(t *testing.T) {
	ch := NewChannel(time.Minute)
	ticket := ch.Open("run-1")

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	if _, ok := ch.Wait(ctx, ticket); ok {
		t.Fatal("取消后应返回 false")
	}
	if ch.PendingCount() != 0 {
		t.Fatal("取消后应清理条目")
	}
}

func TestChannelTicketsAreUnique(t *testing.T) {
	ch := NewChannel(time.Second)
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		tk := ch.Open("run")
		if seen[tk] {
			t.Fatal("票据不应重复")
		}
		seen[tk] = true
	}
}

func TestChannelDisabledOnMultipleReplicas(t *testing.T) {
	t.Setenv("QM_REPLICAS", "3")
	ch := NewChannel(time.Second)
	if ch.Enabled() {
		t.Fatal("多副本部署下客户端工具应被停用")
	}

	t.Setenv("QM_REPLICAS", "1")
	if !NewChannel(time.Second).Enabled() {
		t.Fatal("单副本应启用")
	}
}

// ---- Registry ----

func TestRegistryRejectsDuplicateAndBrokenTools(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(&Tool{Name: "a", Executor: ExecutorServer, Handler: func(context.Context, ToolCall) (ToolResult, error) {
		return ToolResult{}, nil
	}}); err != nil {
		t.Fatalf("首次注册应成功: %v", err)
	}
	if err := r.Register(&Tool{Name: "a", Executor: ExecutorServer, Handler: func(context.Context, ToolCall) (ToolResult, error) {
		return ToolResult{}, nil
	}}); err == nil {
		t.Fatal("重名应报错")
	}
	if err := r.Register(&Tool{Name: "b", Executor: ExecutorServer}); err == nil {
		t.Fatal("服务端工具缺少 Handler 应报错")
	}
	if err := r.Register(&Tool{Executor: ExecutorServer}); err == nil {
		t.Fatal("无名工具应报错")
	}
}

func TestRegistrySceneFiltering(t *testing.T) {
	r := NewRegistry()
	r.MustRegister(&Tool{Name: "always", Executor: ExecutorServer,
		Handler: func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{}, nil }})
	r.MustRegister(&Tool{Name: "editor_only", Executor: ExecutorServer, Scenes: []Scene{SceneCompositionEditor},
		Handler: func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{}, nil }})

	// unscoped（页面没声明场景）时全部可用，与引入场景机制前的行为一致。
	if got := len(r.Schemas(SceneUnscoped, true)); got != 2 {
		t.Fatalf("unscoped 应暴露全部工具，实际 %d", got)
	}
	if got := len(r.Schemas(SceneQuestionLibrary, true)); got != 1 {
		t.Fatalf("题库场景应只暴露通用工具，实际 %d", got)
	}
	if got := len(r.Schemas(SceneCompositionEditor, true)); got != 2 {
		t.Fatalf("组卷场景应暴露两个工具，实际 %d", got)
	}
}

// 通道停用时不应暴露客户端工具：暴露了只会让用户白等一轮超时。
func TestRegistryHidesClientToolsWhenDisabled(t *testing.T) {
	r := NewRegistry()
	r.MustRegister(&Tool{Name: "server", Executor: ExecutorServer,
		Handler: func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{}, nil }})
	r.MustRegister(&Tool{Name: "client", Executor: ExecutorClient})

	if got := len(r.Schemas(SceneUnscoped, false)); got != 1 {
		t.Fatalf("通道停用时应只暴露服务端工具，实际 %d", got)
	}
	if got := len(r.Schemas(SceneUnscoped, true)); got != 2 {
		t.Fatalf("通道启用时应暴露全部工具，实际 %d", got)
	}
}

// ---- Runtime ----

func collectRun(t *testing.T, rt *Runtime, req RunRequest) ([]Event, error) {
	t.Helper()
	var events []Event
	err := rt.Run(context.Background(), req, func(ev Event) error {
		events = append(events, ev)
		return nil
	})
	return events, err
}

func baseRequest() RunRequest {
	return RunRequest{
		RunID: "run-1",
		Model: "fake-model",
		Call:  ToolCall{User: testUser(), SubjectID: 1},
	}
}

func TestRuntimeSimpleAnswer(t *testing.T) {
	prov := &fakeProvider{turns: []fakeTurn{{text: "你好"}}}
	rt := NewRuntime(prov, NewRegistry(), NewChannel(time.Second), Budget{})

	events, err := collectRun(t, rt, baseRequest())
	if err != nil {
		t.Fatalf("运行失败: %v", err)
	}

	var text strings.Builder
	var finished *RunFinished
	for _, ev := range events {
		switch e := ev.(type) {
		case *TextDelta:
			text.WriteString(e.Text)
		case *RunFinished:
			finished = e
		}
	}
	if text.String() != "你好" {
		t.Fatalf("文本应逐字累加为「你好」，实际 %q", text.String())
	}
	if finished == nil {
		t.Fatal("应发出结束事件")
	}
	if finished.StopReason != StopCompleted {
		t.Fatalf("结束原因应为 completed，实际 %s", finished.StopReason)
	}
	if prov.calls() != 1 {
		t.Fatalf("无工具调用时应只请求一次，实际 %d", prov.calls())
	}
}

func TestRuntimeExecutesServerTool(t *testing.T) {
	var received ToolCall
	reg := NewRegistry()
	reg.MustRegister(&Tool{
		Name: "search_questions", Executor: ExecutorServer,
		Parameters: map[string]any{"type": "object"},
		Handler: func(_ context.Context, call ToolCall) (ToolResult, error) {
			received = call
			return TextResult("找到 2 道题"), nil
		},
	})

	prov := &fakeProvider{turns: []fakeTurn{
		{toolCalls: []ai.ToolCall{{ID: "c1", Name: "search_questions", Arguments: `{"keyword":"函数","limit":5}`}}},
		{text: "已为你找到 2 道题。"},
	}}
	rt := NewRuntime(prov, reg, NewChannel(time.Second), Budget{})

	events, err := collectRun(t, rt, baseRequest())
	if err != nil {
		t.Fatalf("运行失败: %v", err)
	}

	// 参数应被解析为 map 交给工具。
	if received.ArgString("keyword") != "函数" {
		t.Fatalf("工具收到的关键字错误: %v", received.Arguments)
	}
	if received.ArgInt("limit") != 5 {
		t.Fatalf("工具收到的 limit 错误: %v", received.Arguments)
	}
	if received.User == nil || received.SubjectID != 1 {
		t.Fatal("工具应拿到用户与学科作用域以做鉴权")
	}

	var started, finished bool
	for _, ev := range events {
		switch ev.(type) {
		case *ToolCallStarted:
			started = true
		case *ToolCallFinished:
			finished = true
		}
	}
	if !started || !finished {
		t.Fatalf("应发出工具开始与结束事件: started=%v finished=%v", started, finished)
	}
	if prov.calls() != 2 {
		t.Fatalf("带工具调用时应请求两轮，实际 %d", prov.calls())
	}
}

// 工具报错不应中断 run，而应把错误回喂给模型让它自我纠正。
func TestRuntimeToolErrorIsFedBackToModel(t *testing.T) {
	reg := NewRegistry()
	reg.MustRegister(&Tool{Name: "boom", Executor: ExecutorServer,
		Handler: func(context.Context, ToolCall) (ToolResult, error) {
			return ToolResult{}, apperr.ErrForbidden
		}})

	prov := &fakeProvider{turns: []fakeTurn{
		{toolCalls: []ai.ToolCall{{ID: "c1", Name: "boom", Arguments: "{}"}}},
		{text: "我没有权限执行该操作。"},
	}}
	rt := NewRuntime(prov, reg, NewChannel(time.Second), Budget{})

	events, err := collectRun(t, rt, baseRequest())
	if err != nil {
		t.Fatalf("工具失败不应让运行失败: %v", err)
	}

	var content string
	for _, ev := range events {
		if e, ok := ev.(*ToolCallFinished); ok {
			content = e.Content
		}
	}
	if !strings.Contains(content, "权限") {
		t.Fatalf("应把领域错误文案回喂给模型，实际 %q", content)
	}
	if prov.calls() != 2 {
		t.Fatal("模型应有机会在收到错误后继续")
	}
}

// 模型幻觉出不存在的工具时，回喂错误而不是崩溃。
func TestRuntimeUnknownTool(t *testing.T) {
	prov := &fakeProvider{turns: []fakeTurn{
		{toolCalls: []ai.ToolCall{{ID: "c1", Name: "不存在的工具", Arguments: "{}"}}},
		{text: "我用其它方式回答。"},
	}}
	rt := NewRuntime(prov, NewRegistry(), NewChannel(time.Second), Budget{})

	events, err := collectRun(t, rt, baseRequest())
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	var content string
	for _, ev := range events {
		if e, ok := ev.(*ToolCallFinished); ok {
			content = e.Content
		}
	}
	if !strings.Contains(content, "不存在") {
		t.Fatalf("应提示工具不存在，实际 %q", content)
	}
}

func TestRuntimeInvalidToolArguments(t *testing.T) {
	reg := NewRegistry()
	reg.MustRegister(&Tool{Name: "t", Executor: ExecutorServer,
		Handler: func(context.Context, ToolCall) (ToolResult, error) { return TextResult("ok"), nil }})

	prov := &fakeProvider{turns: []fakeTurn{
		{toolCalls: []ai.ToolCall{{ID: "c1", Name: "t", Arguments: `{这不是 JSON}`}}},
		{text: "参数有问题。"},
	}}
	rt := NewRuntime(prov, reg, NewChannel(time.Second), Budget{})

	events, err := collectRun(t, rt, baseRequest())
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	var content string
	for _, ev := range events {
		if e, ok := ev.(*ToolCallFinished); ok {
			content = e.Content
		}
	}
	if !strings.Contains(content, "JSON") {
		t.Fatalf("应提示参数不是合法 JSON，实际 %q", content)
	}
}

// 空参数字符串（无参工具常见）应按空对象处理。
func TestRuntimeEmptyArguments(t *testing.T) {
	var got map[string]any
	reg := NewRegistry()
	reg.MustRegister(&Tool{Name: "t", Executor: ExecutorServer,
		Handler: func(_ context.Context, call ToolCall) (ToolResult, error) {
			got = call.Arguments
			return TextResult("ok"), nil
		}})

	prov := &fakeProvider{turns: []fakeTurn{
		{toolCalls: []ai.ToolCall{{ID: "c1", Name: "t", Arguments: ""}}},
		{text: "完成"},
	}}
	rt := NewRuntime(prov, reg, NewChannel(time.Second), Budget{})
	if _, err := collectRun(t, rt, baseRequest()); err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("空参数应解析为空对象，实际 %#v", got)
	}
}

// 客户端工具：推给前端 → 等回传 → 结果回喂模型。
func TestRuntimeClientToolRoundTrip(t *testing.T) {
	ch := NewChannel(2 * time.Second)

	reg := NewRegistry()
	reg.MustRegister(&Tool{
		Name: "edit_paper", Executor: ExecutorClient,
		Prepare: func(_ context.Context, call ToolCall) (map[string]any, error) {
			// 预处理应做归一化：这里模拟"把 Markdown 转成结构化操作"。
			out := map[string]any{}
			for k, v := range call.Arguments {
				out[k] = v
			}
			out["prepared"] = true
			return out, nil
		},
	})

	prov := &fakeProvider{turns: []fakeTurn{
		{toolCalls: []ai.ToolCall{{ID: "c1", Name: "edit_paper", Arguments: `{"op":"add"}`}}},
		{text: "已帮你加好了。"},
	}}
	rt := NewRuntime(prov, reg, ch, Budget{})

	ticketCh := make(chan string, 1)
	done := make(chan error, 1)

	go func() {
		done <- rt.Run(context.Background(), baseRequest(), func(ev Event) error {
			if e, ok := ev.(*ClientToolRequested); ok {
				if e.RunID != "run-1" || e.ToolCallID != "c1" {
					t.Errorf("事件字段错误: %+v", e)
				}
				// 预处理结果应一并下发。
				if args, ok := e.Arguments.(map[string]any); !ok || args["prepared"] != true {
					t.Errorf("应下发预处理后的参数: %#v", e.Arguments)
				}
				ticketCh <- e.Ticket
			}
			return nil
		})
	}()

	ticket := <-ticketCh
	if ticket == "" {
		t.Fatal("应收到票据")
	}
	// 模拟前端执行完回传。
	if !ch.Resolve(ticket, "run-1", map[string]any{"ok": true, "content": "已在编辑器中插入 2 道题"}) {
		t.Fatal("回传应成功")
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("运行失败: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("运行应在回传后继续，而不是挂死")
	}
	if prov.calls() != 2 {
		t.Fatalf("回传后应继续下一轮，实际请求 %d 次", prov.calls())
	}
}

// 客户端工具没有响应时，run 不能挂死：应回喂超时说明并让模型改用文字。
func TestRuntimeClientToolTimeoutKeepsRunAlive(t *testing.T) {
	ch := NewChannel(30 * time.Millisecond)

	reg := NewRegistry()
	reg.MustRegister(&Tool{Name: "edit_paper", Executor: ExecutorClient})

	prov := &fakeProvider{turns: []fakeTurn{
		{toolCalls: []ai.ToolCall{{ID: "c1", Name: "edit_paper", Arguments: "{}"}}},
		{text: "看起来你离开了页面，我改用文字说明。"},
	}}
	rt := NewRuntime(prov, reg, ch, Budget{})

	events, err := collectRun(t, rt, baseRequest())
	if err != nil {
		t.Fatalf("超时不应让运行失败: %v", err)
	}

	var content string
	for _, ev := range events {
		if e, ok := ev.(*ToolCallFinished); ok {
			content = e.Content
		}
	}
	if !strings.Contains(content, "超时") {
		t.Fatalf("应把超时转成给模型的说明，实际 %q", content)
	}
	if prov.calls() != 2 {
		t.Fatal("模型应有机会继续")
	}
}

// 预处理失败时不应开票：否则前端收不到请求，白等一轮超时。
func TestRuntimeClientToolPrepareFailure(t *testing.T) {
	ch := NewChannel(time.Second)

	reg := NewRegistry()
	reg.MustRegister(&Tool{
		Name: "edit_paper", Executor: ExecutorClient,
		Prepare: func(context.Context, ToolCall) (map[string]any, error) {
			return nil, apperr.New(apperr.CodeInvalidParam, "缺少 op 参数")
		},
	})

	prov := &fakeProvider{turns: []fakeTurn{
		{toolCalls: []ai.ToolCall{{ID: "c1", Name: "edit_paper", Arguments: "{}"}}},
		{text: "参数不对，我重新试。"},
	}}
	rt := NewRuntime(prov, reg, ch, Budget{})

	events, err := collectRun(t, rt, baseRequest())
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}

	for _, ev := range events {
		if _, ok := ev.(*ClientToolRequested); ok {
			t.Fatal("预处理失败时不应向前端开票")
		}
	}
	var content string
	for _, ev := range events {
		if e, ok := ev.(*ToolCallFinished); ok {
			content = e.Content
		}
	}
	if !strings.Contains(content, "缺少 op 参数") {
		t.Fatalf("应把预处理失败原因回喂模型，实际 %q", content)
	}
}

// 通道停用时，客户端工具应直接拒绝而不是静默卡住。
func TestRuntimeClientToolWhenChannelDisabled(t *testing.T) {
	t.Setenv("QM_REPLICAS", "2")
	ch := NewChannel(time.Second)

	reg := NewRegistry()
	reg.MustRegister(&Tool{Name: "edit_paper", Executor: ExecutorClient})

	prov := &fakeProvider{turns: []fakeTurn{
		{toolCalls: []ai.ToolCall{{ID: "c1", Name: "edit_paper", Arguments: "{}"}}},
		{text: "当前部署不支持，我改用文字说明。"},
	}}
	rt := NewRuntime(prov, reg, ch, Budget{})

	events, err := collectRun(t, rt, baseRequest())
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	var content string
	for _, ev := range events {
		if e, ok := ev.(*ToolCallFinished); ok {
			content = e.Content
		}
	}
	if !strings.Contains(content, "不支持") {
		t.Fatalf("应明确告知不支持，实际 %q", content)
	}
}

// 轮次用尽时应标记 budget_exhausted，而不是假装正常结束。
func TestRuntimeBudgetMaxTurns(t *testing.T) {
	reg := NewRegistry()
	reg.MustRegister(&Tool{Name: "loop", Executor: ExecutorServer,
		Handler: func(context.Context, ToolCall) (ToolResult, error) { return TextResult("继续"), nil }})

	// 模型每一轮都要求调用工具，永不给出最终回答。
	turns := make([]fakeTurn, 10)
	for i := range turns {
		turns[i] = fakeTurn{toolCalls: []ai.ToolCall{{ID: "c", Name: "loop", Arguments: "{}"}}}
	}
	prov := &fakeProvider{turns: turns}
	rt := NewRuntime(prov, reg, NewChannel(time.Second), Budget{MaxTurns: 3})

	events, err := collectRun(t, rt, baseRequest())
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}

	var reason string
	for _, ev := range events {
		if e, ok := ev.(*RunFinished); ok {
			reason = e.StopReason
		}
	}
	if reason != StopBudgetExhausted {
		t.Fatalf("应用尽轮次标记为 budget_exhausted，实际 %q", reason)
	}
	if prov.calls() > 3 {
		t.Fatalf("不应超过 3 轮，实际 %d", prov.calls())
	}
}

// 单轮内发起过多工具调用时应被工具数上限拦住。
func TestRuntimeBudgetMaxToolCalls(t *testing.T) {
	reg := NewRegistry()
	reg.MustRegister(&Tool{Name: "t", Executor: ExecutorServer,
		Handler: func(context.Context, ToolCall) (ToolResult, error) { return TextResult("ok"), nil }})

	many := make([]ai.ToolCall, 10)
	for i := range many {
		many[i] = ai.ToolCall{ID: "c", Name: "t", Arguments: "{}"}
	}
	prov := &fakeProvider{turns: []fakeTurn{{toolCalls: many}, {text: "停"}}}
	rt := NewRuntime(prov, reg, NewChannel(time.Second), Budget{MaxToolCalls: 3})

	events, err := collectRun(t, rt, baseRequest())
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}

	var reason string
	for _, ev := range events {
		if e, ok := ev.(*RunFinished); ok {
			reason = e.StopReason
		}
	}
	if reason != StopToolLimit {
		t.Fatalf("应用尽工具数标记为 tool_limit，实际 %q", reason)
	}
}

// 墙钟超时应标记 time_limit，而不是和轮次用尽共用 budget_exhausted——
// 两者对用户是完全不同的两句话。
func TestRuntimeBudgetWallClock(t *testing.T) {
	reg := NewRegistry()
	reg.MustRegister(&Tool{Name: "loop", Executor: ExecutorServer,
		Handler: func(context.Context, ToolCall) (ToolResult, error) { return TextResult("继续"), nil }})

	// 第一轮就耗掉 50ms，而墙钟预算只有 5ms：
	// 第二轮开始前必然判超时，且不会再调用模型。
	prov := &fakeProvider{turns: []fakeTurn{{
		delay:     50 * time.Millisecond,
		toolCalls: []ai.ToolCall{{ID: "c", Name: "loop", Arguments: "{}"}},
	}}}
	rt := NewRuntime(prov, reg, NewChannel(time.Second),
		Budget{MaxTurns: 3, MaxToolCalls: 5, WallClock: 5 * time.Millisecond})

	events, err := collectRun(t, rt, baseRequest())
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}

	var finished *RunFinished
	for _, ev := range events {
		if e, ok := ev.(*RunFinished); ok {
			finished = e
		}
	}
	if finished == nil {
		t.Fatal("应发出结束事件")
	}
	if finished.StopReason != StopTimeLimit {
		t.Fatalf("墙钟超时应标记 time_limit，实际 %q", finished.StopReason)
	}
	if prov.calls() != 1 {
		t.Fatalf("预算耗尽后不应再调用模型，实际 %d 次", prov.calls())
	}
	// 预算要随事件带出去，界面才说得出"3 分钟 / 5 轮 / 20 次"。
	if finished.Budget.MaxTurns != 3 || finished.Budget.MaxToolCalls != 5 ||
		finished.Budget.WallClock != 5*time.Millisecond {
		t.Fatalf("应带上本次预算，实际 %+v", finished.Budget)
	}
}

// 模型调用失败 → RunFailed，且不再发 RunFinished。
func TestRuntimeProviderError(t *testing.T) {
	prov := &fakeProvider{turns: []fakeTurn{{err: errors.New("网络中断")}}}
	rt := NewRuntime(prov, NewRegistry(), NewChannel(time.Second), Budget{})

	events, err := collectRun(t, rt, baseRequest())
	if err == nil {
		t.Fatal("模型失败应返回错误")
	}
	var failed bool
	for _, ev := range events {
		if _, ok := ev.(*RunFailed); ok {
			failed = true
		}
	}
	if !failed {
		t.Fatal("应发出失败事件")
	}
}

// 取消是正常操作，不应被当成失败。
func TestRuntimeCancelIsNotFailure(t *testing.T) {
	prov := &fakeProvider{turns: []fakeTurn{{err: ai.ErrCanceled}}}
	rt := NewRuntime(prov, NewRegistry(), NewChannel(time.Second), Budget{})

	events, err := collectRun(t, rt, baseRequest())
	if err != nil {
		t.Fatalf("取消不应返回错误: %v", err)
	}
	for _, ev := range events {
		if e, ok := ev.(*RunFinished); ok {
			if e.StopReason != StopCanceled {
				t.Fatalf("结束原因应为 canceled，实际 %s", e.StopReason)
			}
			return
		}
	}
	t.Fatal("取消应发出结束事件")
}

// 调用方的 emit 返回错误时应立即停止。
func TestRuntimeStopsWhenEmitFails(t *testing.T) {
	prov := &fakeProvider{turns: []fakeTurn{{text: "很长的回答"}}}
	rt := NewRuntime(prov, NewRegistry(), NewChannel(time.Second), Budget{})

	stop := errors.New("客户端断开")
	count := 0
	err := rt.Run(context.Background(), baseRequest(), func(Event) error {
		count++
		return stop
	})
	if !errors.Is(err, stop) {
		t.Fatalf("应返回 emit 的错误，实际 %v", err)
	}
	if count != 1 {
		t.Fatalf("应在第一帧后停止，实际发出 %d 帧", count)
	}
}

// AssistantTurn 用于落库，且不应产生对外帧。
func TestRuntimeEmitsAssistantTurn(t *testing.T) {
	reg := NewRegistry()
	reg.MustRegister(&Tool{Name: "t", Executor: ExecutorServer,
		Handler: func(context.Context, ToolCall) (ToolResult, error) { return TextResult("ok"), nil }})

	prov := &fakeProvider{turns: []fakeTurn{
		{text: "我先查一下。", toolCalls: []ai.ToolCall{{ID: "c1", Name: "t", Arguments: "{}"}}},
		{text: "查到了。"},
	}}
	rt := NewRuntime(prov, reg, NewChannel(time.Second), Budget{})

	events, _ := collectRun(t, rt, baseRequest())
	var turn *AssistantTurn
	for _, ev := range events {
		if e, ok := ev.(*AssistantTurn); ok {
			turn = e
		}
	}
	if turn == nil {
		t.Fatal("带工具调用的一轮应发出 AssistantTurn 供落库")
	}
	if turn.Text != "我先查一下。" || len(turn.ToolCalls) != 1 {
		t.Fatalf("记录内容错误: %+v", turn)
	}
	if frames := ToFrames(turn); len(frames) != 0 {
		t.Fatalf("AssistantTurn 不应产生对外帧，实际 %d 帧", len(frames))
	}
}

// ---- SSE ----

func TestSSEFrameRendering(t *testing.T) {
	frame := SSEFrame{Event: EventMessage, Data: map[string]any{"text": "你好"}}
	got := frame.Render()

	if !strings.HasPrefix(got, "event: message\n") {
		t.Fatalf("应以 event 行开头: %q", got)
	}
	if !strings.Contains(got, `data: {"text":"你好"}`) {
		t.Fatalf("data 应是紧凑 JSON: %q", got)
	}
	if !strings.HasSuffix(got, "\n\n") {
		t.Fatalf("应以空行结束: %q", got)
	}
}

// 所有事件的 data 都应是 JSON 对象——包括 message。
// 这样前端一套解析逻辑就能处理全部事件，不必为某一种多 parse 一次。
func TestSSEAllDataAreObjects(t *testing.T) {
	rt := NewRuntime(&fakeProvider{turns: []fakeTurn{{text: "x"}}}, NewRegistry(), NewChannel(time.Second), Budget{})
	events, _ := collectRun(t, rt, baseRequest())

	for _, ev := range events {
		for _, f := range ToFrames(ev) {
			raw := f.Render()
			i := strings.Index(raw, "data: ")
			j := strings.Index(raw[i:], "\n")
			payload := raw[i+len("data: ") : i+j]
			var obj map[string]any
			if err := json.Unmarshal([]byte(payload), &obj); err != nil {
				t.Fatalf("事件 %s 的 data 不是 JSON 对象: %s", f.Event, payload)
			}
		}
	}
}

// 终止帧必须存在且名为 done，前端靠它判断流正常结束。
func TestSSEDoneIsTerminal(t *testing.T) {
	fin := &RunFinished{RunID: "r", Text: "", StopReason: StopCompleted}
	frames := ToFrames(fin)
	if len(frames) != 1 || frames[0].Event != EventDone {
		t.Fatalf("应只发一个 done 帧，实际 %+v", frames)
	}

	// 失败时也要以 done 收尾，否则前端会一直转圈。
	failed := ToFrames(&RunFailed{RunID: "r", Error: "出错了"})
	if len(failed) != 2 {
		t.Fatalf("失败应发 error + done 两帧，实际 %d", len(failed))
	}
	if failed[0].Event != EventError || failed[1].Event != EventDone {
		t.Fatalf("帧顺序应为 error 后 done，实际 %s, %s", failed[0].Event, failed[1].Event)
	}
}

// done 帧要带上预算上限，否则 stop_reason 只是一个说不清缘由的英文枚举。
func TestSSEDoneCarriesBudget(t *testing.T) {
	fin := &RunFinished{
		RunID:      "r",
		StopReason: StopTimeLimit,
		Budget:     Budget{MaxTurns: 5, MaxToolCalls: 20, WallClock: 3 * time.Minute},
	}
	data := ToFrames(fin)[0].Data.(map[string]any)
	budget, ok := data["budget"].(map[string]any)
	if !ok {
		t.Fatalf("done 帧应携带预算，实际 data: %+v", data)
	}
	if budget["max_turns"] != 5 || budget["max_tool_calls"] != 20 ||
		budget["wall_clock_seconds"] != 180 {
		t.Fatalf("预算字段错误: %+v", budget)
	}
}

func TestSSEServerToolFrames(t *testing.T) {
	started := ToFrames(&ToolCallStarted{ToolCallID: "c1", Name: "search", Arguments: map[string]any{"k": 1}})
	if len(started) != 1 || started[0].Event != EventToolCall {
		t.Fatalf("工具开始应发 tool_call 帧: %+v", started)
	}

	finished := ToFrames(&ToolCallFinished{
		ToolCallID: "c1", Name: "search", Content: "结果",
		UI: []UIDirective{ProposalDirective("batch", []uint{1, 2})},
	})
	// UI 指令先于结果摘要。
	if len(finished) != 2 || finished[0].Event != "proposal" || finished[1].Event != EventToolResult {
		t.Fatalf("UI 指令应先于结果帧: %+v", finished)
	}
	if finished[0].Data.(map[string]any)["type"] != "batch" {
		t.Fatalf("提案指令负载错误: %+v", finished[0].Data)
	}
}

// 结果摘要要截断：完整的工具输出可能很长，没必要全部推给前端展示。
func TestSSEToolResultTruncated(t *testing.T) {
	long := strings.Repeat("字", 500)
	frames := ToFrames(&ToolCallFinished{ToolCallID: "c1", Name: "t", Content: long})
	output := frames[0].Data.(map[string]any)["output"].(string)
	if len([]rune(output)) > 210 {
		t.Fatalf("结果摘要应被截断，实际 %d 字", len([]rune(output)))
	}
}

func TestSSEHeadersDisableProxyBuffering(t *testing.T) {
	h := SSEHeaders()
	if h["X-Accel-Buffering"] != "no" {
		t.Fatal("必须关闭 nginx 缓冲，否则流式失效")
	}
	if !strings.HasPrefix(h["Content-Type"], "text/event-stream") {
		t.Fatalf("Content-Type 错误: %s", h["Content-Type"])
	}
}

// ---- ToolCall 参数取值 ----

func TestToolCallArgAccessors(t *testing.T) {
	call := ToolCall{Arguments: map[string]any{
		"text":   "字符串",
		"num":    float64(42),
		"ids":    []any{float64(1), float64(2)},
		"names":  []any{"a", "b"},
		"absent": nil,
	}}

	if call.ArgString("text") != "字符串" {
		t.Fatal("ArgString 错误")
	}
	// 模型有时把数字写成数字类型，也应能取到字符串。
	if call.ArgString("num") != "42" {
		t.Fatalf("数字应能按字符串取出，实际 %q", call.ArgString("num"))
	}
	if call.ArgInt("num") != 42 {
		t.Fatal("ArgInt 错误")
	}
	if ids := call.ArgUintSlice("ids"); len(ids) != 2 || ids[1] != 2 {
		t.Fatalf("ArgUintSlice 错误: %v", ids)
	}
	if names := call.ArgStringSlice("names"); len(names) != 2 || names[0] != "a" {
		t.Fatalf("ArgStringSlice 错误: %v", names)
	}
	// 缺失或类型不符时返回零值，不 panic。
	if call.ArgString("absent") != "" || call.ArgInt("absent") != 0 {
		t.Fatal("缺失参数应返回零值")
	}
	if call.ArgUintSlice("text") != nil || call.ArgStringSlice("num") != nil {
		t.Fatal("类型不符应返回 nil")
	}
}

func TestParseScene(t *testing.T) {
	if ParseScene("composition_editor") != SceneCompositionEditor {
		t.Fatal("已知场景应正常解析")
	}
	// 未知场景回落而不是报错：前后端版本不一致不能把对话打挂。
	if ParseScene("未知场景") != SceneUnscoped {
		t.Fatal("未知场景应回落为 unscoped")
	}
	if ParseScene("") != SceneUnscoped {
		t.Fatal("空场景应为 unscoped")
	}
}
