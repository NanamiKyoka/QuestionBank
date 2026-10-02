package aiagent

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/NanamiKyoka/questionmind/internal/ai"
	"github.com/NanamiKyoka/questionmind/internal/model"
)

// Executor 工具的执行方。
type Executor string

const (
	// ExecutorServer 服务端直接执行（查库、写库）。
	ExecutorServer Executor = "server"
	// ExecutorClient 由前端执行并回传。
	//
	// 存在的意义：用户在编辑器里改了一半还没保存，此时 AI 说"我帮你加了两道题"，
	// 如果只改数据库，用户界面不会变、体验就断了。
	// 客户端工具让 AI 能操作用户**眼前的状态**。
	ExecutorClient Executor = "client"
)

// Scene 前端页面场景，用于收敛暴露给模型的工具集。
//
// ⚠️ scene 由客户端自报，**不可信**，它只用来减少干扰与省 token，
// 不是授权机制。真正的门禁在 service 层的鉴权。
type Scene string

const (
	SceneUnscoped          Scene = "unscoped"
	SceneQuestionLibrary   Scene = "question_library"
	SceneCompositionEditor Scene = "composition_editor"
	SceneImportReview      Scene = "import_review"
)

// ParseScene 解析场景；未知值回落为 unscoped 而不是报错，
// 避免前后端版本不一致时把对话直接打挂。
func ParseScene(v string) Scene {
	switch Scene(v) {
	case SceneQuestionLibrary, SceneCompositionEditor, SceneImportReview:
		return Scene(v)
	default:
		return SceneUnscoped
	}
}

// ToolCall 一次工具调用的上下文。
type ToolCall struct {
	// User 当前用户，工具实现必须据此做数据级鉴权。
	User *model.User
	// SubjectID 当前学科作用域。
	SubjectID uint
	// Arguments 模型给出的参数（已解析为 map；解析失败时为空 map）。
	Arguments map[string]any
	// RawArguments 原始参数字符串，便于错误回传时展示。
	RawArguments string
	// ToolCallID 模型给出的调用 id。
	ToolCallID string
}

// ArgString 取字符串参数。
func (c ToolCall) ArgString(key string) string {
	v, ok := c.Arguments[key]
	if !ok || v == nil {
		return ""
	}
	switch s := v.(type) {
	case string:
		return s
	case float64:
		// 模型有时把数字写成数字类型。
		return fmt.Sprintf("%.0f", s)
	case bool:
		return fmt.Sprintf("%v", s)
	}
	return fmt.Sprintf("%v", v)
}

// ArgInt 取整数参数。
func (c ToolCall) ArgInt(key string) int {
	v, ok := c.Arguments[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case string:
		var out int
		fmt.Sscanf(n, "%d", &out)
		return out
	}
	return 0
}

// ArgUintSlice 取整数数组参数。
func (c ToolCall) ArgUintSlice(key string) []uint {
	v, ok := c.Arguments[key]
	if !ok {
		return nil
	}
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]uint, 0, len(list))
	for _, item := range list {
		switch n := item.(type) {
		case float64:
			out = append(out, uint(n))
		case int:
			out = append(out, uint(n))
		}
	}
	return out
}

// ArgStringSlice 取字符串数组参数。
func (c ToolCall) ArgStringSlice(key string) []string {
	v, ok := c.Arguments[key]
	if !ok {
		return nil
	}
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// ArgBool 取布尔参数，缺省为 false。
func (c ToolCall) ArgBool(key string) bool {
	return c.ArgBoolOr(key, false)
}

// ArgBoolOr 取布尔参数，缺省由 def 决定。
//
// 对「安全侧是默认值」的开关必须用这个而不是 ArgBool：模型偶尔会漏写
// 一个可选参数，而漏写 dry_run 落到的分支决定了数据到底写不写。
// 那种情况下让"没写"等同于"先别动"，比让"没写"等同于"照做"安全得多。
func (c ToolCall) ArgBoolOr(key string, def bool) bool {
	switch v := c.Arguments[key].(type) {
	case bool:
		return v
	case string:
		if parsed, err := strconv.ParseBool(strings.TrimSpace(v)); err == nil {
			return parsed
		}
	case float64:
		return v != 0
	}
	return def
}

// ToolResult 工具执行结果。
type ToolResult struct {
	// Content 回喂给模型的文本。应简明扼要——它会占用上下文。
	Content string
	// Data 结构化结果，供前端使用，不喂给模型。
	Data map[string]any
	// UI 需要前端渲染的指令（如题目提案卡片）。
	UI []UIDirective
}

// TextResult 构造纯文本结果。
func TextResult(content string) ToolResult {
	return ToolResult{Content: content}
}

// ErrorResult 构造给模型看的错误结果。
//
// 工具失败**不中断** run：把错误文本回喂给模型，让它自我纠正
// （换个参数重试、或改用文字向用户说明）。这比直接报错体验好得多。
func ErrorResult(format string, args ...any) ToolResult {
	return ToolResult{Content: "错误：" + fmt.Sprintf(format, args...)}
}

// ToolHandler 服务端工具的执行体。
type ToolHandler func(ctx context.Context, call ToolCall) (ToolResult, error)

// ToolPreparer 客户端工具下发前的预处理。
//
// 在服务端先跑一遍校验、归一化与富化（如 Markdown → RichDoc），
// 好处是：前端只需做纯粹的结构操作，而且明显不合法的请求
// 会在开票之前就被拦下，不会让用户白等一轮超时。
type ToolPreparer func(ctx context.Context, call ToolCall) (map[string]any, error)

// Tool 一个暴露给模型的工具。
type Tool struct {
	Name        string
	Description string
	// Parameters 面向模型的 JSON Schema。
	// 刻意手写而不是由结构体反射生成：不同厂商对 $ref / anyOf 的支持
	// 参差不齐，手写最可控。
	Parameters map[string]any

	Executor Executor
	// Mutating 是否写操作。写操作必须落到 service 层做鉴权与校验。
	Mutating bool
	// Capability 记录写操作最终落到哪个业务能力上，纯文档用途。
	Capability string
	// Scenes 限定可用场景；为空表示处处可用。
	Scenes []Scene

	Handler ToolHandler
	Prepare ToolPreparer
}

// AvailableIn 判断该工具在某场景下是否可用。
func (t *Tool) AvailableIn(scene Scene) bool {
	if len(t.Scenes) == 0 || scene == SceneUnscoped {
		return true
	}
	for _, s := range t.Scenes {
		if s == scene {
			return true
		}
	}
	return false
}

// Spec 转换为 provider 需要的工具定义。
func (t *Tool) Spec() ai.ToolSpec {
	return ai.ToolSpec{
		Name:        t.Name,
		Description: t.Description,
		Parameters:  t.Parameters,
	}
}

// Registry 工具注册表。
type Registry struct {
	tools map[string]*Tool
	order []string
}

// NewRegistry 构造空注册表。
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]*Tool)}
}

// Register 注册工具。重名直接报错——重名意味着有人的实现被静默覆盖，
// 而这种问题在运行时极难定位。
func (r *Registry) Register(t *Tool) error {
	if t == nil || t.Name == "" {
		return fmt.Errorf("aiagent: 工具必须有名字")
	}
	if _, dup := r.tools[t.Name]; dup {
		return fmt.Errorf("aiagent: 工具重名 %q", t.Name)
	}
	if t.Executor == ExecutorServer && t.Handler == nil {
		return fmt.Errorf("aiagent: 服务端工具 %q 缺少 Handler", t.Name)
	}
	r.tools[t.Name] = t
	r.order = append(r.order, t.Name)
	return nil
}

// MustRegister 注册并在失败时 panic。用于包初始化的固定工具集。
func (r *Registry) MustRegister(t *Tool) {
	if err := r.Register(t); err != nil {
		panic(err)
	}
}

// Get 按名取工具。
func (r *Registry) Get(name string) (*Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// All 返回全部工具，顺序稳定（便于测试与日志对比）。
func (r *Registry) All() []*Tool {
	out := make([]*Tool, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, r.tools[name])
	}
	return out
}

// Schemas 返回指定场景下可见的工具定义，传给模型。
func (r *Registry) Schemas(scene Scene, clientToolsEnabled bool) []ai.ToolSpec {
	var out []ai.ToolSpec
	for _, name := range r.order {
		t := r.tools[name]
		if !t.AvailableIn(scene) {
			continue
		}
		// 多副本部署下不暴露客户端工具：通道会失效，
		// 暴露出去只会让用户遇到"点了没反应、等 30 秒超时"。
		if t.Executor == ExecutorClient && !clientToolsEnabled {
			continue
		}
		out = append(out, t.Spec())
	}
	return out
}

// Names 返回全部工具名（排序后），用于自检与测试断言。
func (r *Registry) Names() []string {
	out := append([]string(nil), r.order...)
	sort.Strings(out)
	return out
}
