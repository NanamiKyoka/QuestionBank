package importing

import (
	"context"
	"strconv"
	"strings"
	"testing"
)

// memoryStore 内存模板源，用来验证解析链而不碰数据库。
type memoryStore struct {
	// byKey 键 → 模板正文。键可以是基础键，也可以是学科覆盖键。
	byKey map[string]string
	err   error
	// calls 记录每次查找的参数，用来证明"确实按学科查了"。
	calls []string
}

func (m *memoryStore) LookupTemplate(_ context.Context, key string, subjectID *uint) (TemplateHit, error) {
	if subjectID != nil {
		sk := key + "@subject:" + strconv.FormatUint(uint64(*subjectID), 10)
		m.calls = append(m.calls, sk)
		if body, ok := m.byKey[sk]; ok {
			return TemplateHit{Body: body, Source: SourceSubject, TemplateID: 7, SubjectID: subjectID}, nil
		}
	} else {
		m.calls = append(m.calls, key)
	}
	if m.err != nil {
		return TemplateHit{}, m.err
	}
	if body, ok := m.byKey[key]; ok {
		return TemplateHit{Body: body, Source: SourceGlobal, TemplateID: 3}, nil
	}
	return TemplateHit{}, nil
}

// withStore 临时替换全局模板源。
//
// 必须恢复：全局变量会泄漏到同包的其他测试里，而"别的测试莫名其妙
// 读到了我塞进去的模板"是极难定位的一种失败。
func withStore(t *testing.T, s TemplateStore) {
	t.Helper()
	prev := currentStore.Load()
	currentStore.Store(&storeHolder{store: s})
	t.Cleanup(func() { currentStore.Store(prev) })
}

func idPtr(n uint) *uint { return &n }

// ---- 渲染 ----

// 占位符取值为空时整行删除——这是"可选段落"的实现方式，
// 也是内置模板里知识点/标签两块能自然消失的原因。
func TestRenderDropsLinesWithEmptyPlaceholders(t *testing.T) {
	out := Render("固定第一行\n{{maybe}}\n{{name}} 是本学科\n{{empty}}尾巴",
		Vars{"maybe": "", "name": "数学", "empty": ""})
	// 最后一行也一起没了——这正是那条规则的代价：不要把必须保留的
	// 固定文字和可能为空的占位符写在同一行。规则写在 Render 的注释里，
	// 也写给了管理界面。
	want := "固定第一行\n数学 是本学科"
	if out != want {
		t.Fatalf("渲染结果不对:\n实际 %q\n期望 %q", out, want)
	}
}

// 一行里只要有一个已知占位符为空就删整行，即使另一个有值——
// 不这么定的话，"{{a}}{{b}}"（a 空 b 有值）这类组合的行为就没法一句话说清。
func TestRenderKeepsLineWhenAnyPlaceholderHasValue(t *testing.T) {
	out := Render("{{name}}{{description}}", Vars{"name": "数学", "description": ""})
	if out != "数学" {
		t.Fatalf("应保留学科名，实际 %q", out)
	}
}

// 写错的占位符必须原样留着：悄悄替换成空会让"为什么这段没生效"无从查起。
func TestRenderKeepsUnknownPlaceholders(t *testing.T) {
	out := Render("你好 {{nmae}}", Vars{"name": "数学"})
	if out != "你好 {{nmae}}" {
		t.Fatalf("未知占位符应原样保留，实际 %q", out)
	}
}

// 双大括号是为了不和提示词里的 JSON / LaTeX 撞车。
func TestRenderDoesNotTouchJSONAndLatex(t *testing.T) {
	tmpl := `{"questions": [{"answer": "\frac{1}{2}"}]}`
	if out := Render(tmpl, Vars{"subject_name": "数学"}); out != tmpl {
		t.Fatalf("JSON 与 LaTeX 里的花括号被改动了: %q", out)
	}
}

func TestUsableRejectsEmptyAndOversized(t *testing.T) {
	if Usable("") || Usable("   \n\t ") {
		t.Fatal("空白提示词不可用")
	}
	if !Usable("你是一位数学老师") {
		t.Fatal("普通短提示词应当可用")
	}
	if Usable(strings.Repeat("字", maxPromptRunes+1)) {
		t.Fatal("超长提示词应当判为不可用")
	}
}

// ---- 解析链 ----

// 优先级：学科覆盖 > 全局模板 > 系统设置 > 内置默认。
func TestResolvePrefersSubjectOverGlobal(t *testing.T) {
	withStore(t, &memoryStore{byKey: map[string]string{
		PromptKeyImportExtract:                "全局抽取提示词",
		PromptKeyImportExtract + "@subject:9": "语文专用抽取提示词",
	}})

	res := ResolveSystemPrompt(context.Background(), PromptKeyImportExtract, idPtr(9), Vars{
		"subject_name":  "语文",
		"output_schema": "{}",
	})
	if !strings.HasPrefix(res.Text, "语文专用抽取提示词") {
		t.Fatalf("学科覆盖应赢，实际来源 %s 正文 %q", res.Source, res.Text)
	}
	if res.Source != SourceSubject {
		t.Fatalf("来源应为 subject，实际 %s", res.Source)
	}
	if res.SubjectID == nil || *res.SubjectID != 9 {
		t.Fatalf("应记录命中的学科，实际 %v", res.SubjectID)
	}
}

func TestResolveFallsBackToGlobalWhenSubjectHasNoOverride(t *testing.T) {
	withStore(t, &memoryStore{byKey: map[string]string{
		PromptKeyImportExtract: "全局抽取提示词",
	}})
	res := ResolveSystemPrompt(context.Background(), PromptKeyImportExtract, idPtr(3), Vars{
		"output_schema": "{}",
	})
	if !strings.HasPrefix(res.Text, "全局抽取提示词") || res.Source != SourceGlobal {
		t.Fatalf("应回退到全局模板，实际 %s / %q", res.Source, res.Text)
	}
}

// 读模板失败时必须降级到内置默认，不能把错误抛给调用方——
// 一次导入要跑 30 秒到 3 分钟，为了一段提示词打断它是本末倒置。
func TestResolveFailsSoftOnStoreError(t *testing.T) {
	withStore(t, &memoryStore{err: context.DeadlineExceeded})
	res := ResolveSystemPrompt(context.Background(), PromptKeyImportExtract, nil, Vars{
		"subject_name":  "数学",
		"output_schema": "{}",
	})
	if res.Source != SourceBuiltin {
		t.Fatalf("读失败应回落到内置默认，实际 %s", res.Source)
	}
	if !strings.Contains(res.Text, "资深") {
		t.Fatalf("应回落到内置默认正文，实际 %q", truncate(res.Text, 60))
	}
}

// 模板坏掉（渲染成空）时必须降级，并把原因带出去给管理界面显示。
// 静默降级最糟：管理员会以为自己的模板已经接管了提示词。
func TestResolveFailsSoftOnTemplateRenderingEmpty(t *testing.T) {
	withStore(t, &memoryStore{byKey: map[string]string{
		PromptKeyImportExtract: "{{knowledge_point_block}}",
	}})
	res := ResolveSystemPrompt(context.Background(), PromptKeyImportExtract, nil, Vars{
		"subject_name":          "数学",
		"knowledge_point_block": "",
		"output_schema":         "{}",
	})
	if res.Source != SourceBuiltin {
		t.Fatalf("渲染成空的模板应判为不可用，实际来源 %s", res.Source)
	}
	if res.Fallback == "" {
		t.Fatal("降级必须带原因，否则管理员看不出自己的模板为什么没生效")
	}
	if !strings.Contains(res.Text, "资深") {
		t.Fatalf("应回落到内置默认正文，实际 %q", truncate(res.Text, 60))
	}
}

// 没有注入模板源（单测、独立工具程序）时就是"没配置"，走内置默认。
func TestResolveWithoutStoreUsesBuiltin(t *testing.T) {
	withStore(t, nil)
	res := ResolveSystemPrompt(context.Background(), PromptKeyAssistant, nil, Vars{})
	if res.Source != SourceBuiltin || !strings.Contains(res.Text, "QuestionMind") {
		t.Fatalf("无存储时应使用内置默认，实际 %s / %q", res.Source, truncate(res.Text, 60))
	}
}

// 未知键不 panic、不抛错：这是程序错误，但不能让它变成一次导入失败。
func TestResolveUnknownKeyIsSafe(t *testing.T) {
	withStore(t, &memoryStore{byKey: map[string]string{}})
	res := ResolveSystemPrompt(context.Background(), "no_such_prompt", nil, Vars{})
	if res.Text != "" || res.Fallback == "" {
		t.Fatalf("未知键应返回空文本并说明原因，实际 %+v", res)
	}
}

// ---- 内置默认 ----

// 这是整个功能的"行为不变"承诺：没配任何模板时，
// BuildSystemPrompt 必须与改造前的硬编码版本逐字一致。
func TestBuildSystemPromptKeepsLegacyOutput(t *testing.T) {
	withStore(t, &memoryStore{byKey: map[string]string{}})
	opt := PromptOptions{
		Mode:                    ModeExtract,
		SubjectName:             "数学",
		ExistingKnowledgePoints: []string{"导数", "单调性"},
		ExistingTags:            []string{"高考"},
	}
	got := BuildSystemPrompt(opt)

	mustContain(t, got,
		"你是一位资深的中小学与大学学科教研员，擅长把试卷、讲义、教案等材料整理成结构化的题库数据。",
		"任务：从下面的材料中识别出所有题目，并整理成结构化数据。",
		"1. 材料里可能有标题、考号说明、分数说明等非题目内容，这些不要当成题目。",
		"数学公式必须使用 LaTeX：行内用 $...$，独立成行用 $$...$$。",
		"本材料属于学科：数学",
		"知识点请优先从下列已有清单中选取（原样使用名称，不要改写）：",
		"  导数、单调性",
		"清单里确实没有合适的，才新建知识点名称。",
		"标签请优先从下列已有清单中选取：",
		"  高考",
		"严格只输出一个 JSON 对象，不要输出任何解释文字，也不要用 Markdown 代码块包裹。",
		`"question_type": "single_choice | multiple_choice | fill_blank | true_false | essay"`,
	)
	if strings.Contains(got, "{{") {
		t.Fatalf("默认提示词不应残留未渲染的占位符: %s", truncate(got, 200))
	}
}

// 解题模式的正文与结构说明都与抽取不同，不能串台。
func TestBuildSystemPromptSolveMode(t *testing.T) {
	withStore(t, &memoryStore{byKey: map[string]string{}})
	got := BuildSystemPrompt(PromptOptions{Mode: ModeSolve, SubjectName: "物理"})
	mustContain(t, got, "任务：为下面给出的每一道题补写答案与解析。题干必须原样返回")
	if strings.Contains(got, "从下面的材料中识别出所有题目") {
		t.Fatal("解题模式不应包含抽取模式的任务描述")
	}
	mustContain(t, got, `"stem": "原样返回题干，不要改动"`)
}

// 仅录题模式：结构说明里没有 analysis，answer 固定为空串。
// 留着这两个字段，模型就会"顺手"给出一份没人要的推导——
// 那正是这个模式要省掉的开销。
func TestBuildSystemPromptRecordModeAsksForNoAnswers(t *testing.T) {
	withStore(t, &memoryStore{byKey: map[string]string{}})
	got := BuildSystemPrompt(PromptOptions{Mode: ModeRecord, SubjectName: "物理"})

	mustContain(t, got, "**不要解题**", `"answer": "固定输出空字符串`)
	if strings.Contains(got, `"analysis"`) {
		t.Fatal("仅录题的结构说明里不应再要求解析")
	}
}

// 三种模式各走各的提示词键：串台会让管理员在「提示词」页改了一条却不生效。
func TestPromptKeyOfModePerMode(t *testing.T) {
	cases := map[Mode]string{
		ModeExtract: PromptKeyImportExtract,
		ModeSolve:   PromptKeyImportSolve,
		ModeRecord:  PromptKeyImportRecord,
		Mode(""):    PromptKeyImportExtract,
	}
	for mode, want := range cases {
		if got := PromptKeyOfMode(mode); got != want {
			t.Fatalf("模式 %q 应取提示词 %s，实际 %s", mode, want, got)
		}
	}
}

// 答案页的转写提示只在完整抽取里出现：仅录题要的就是不碰答案，
// 补写答案模式则连题干都由用户给全了。
func TestAnswerImagesHintOnlyInExtractMode(t *testing.T) {
	opt := PromptOptions{AnswerImages: 2}
	for _, mode := range []Mode{ModeExtract, ModeRecord, ModeSolve} {
		opt.Mode = mode
		got := BuildUserContent("材料", opt)
		hasHint := strings.Contains(got, "答案与解析页")
		if want := mode == ModeExtract; hasHint != want {
			t.Fatalf("模式 %q 的答案页提示应为 %v，实际 %v", mode, want, hasHint)
		}
	}
	opt.Mode = ModeRecord
	mustContain(t, BuildUserContent("材料", opt), "只记录题目本身，不要给出答案与解析")
}

// 没有知识点/标签时，对应的引导段整段消失，而不是留下空标题。
func TestBuildSystemPromptOmitsEmptyBlocks(t *testing.T) {
	withStore(t, &memoryStore{byKey: map[string]string{}})
	got := BuildSystemPrompt(PromptOptions{Mode: ModeExtract, SubjectName: "化学"})
	if strings.Contains(got, "知识点请优先") || strings.Contains(got, "标签请优先") {
		t.Fatalf("没有清单时不应出现引导段: %s", truncate(got, 300))
	}
}

// 本次改造的核心承诺：配置了的模板必须真的到达提示词构建器。
func TestConfiguredTemplateReachesPromptBuilder(t *testing.T) {
	withStore(t, &memoryStore{byKey: map[string]string{
		PromptKeyImportExtract: "【自定义抽取提示词】学科：{{subject_name}}\n{{output_schema}}",
	}})
	got := BuildSystemPrompt(PromptOptions{Mode: ModeExtract, SubjectName: "生物"})
	if !strings.HasPrefix(got, "【自定义抽取提示词】") {
		t.Fatalf("配置的模板没有到达 BuildSystemPrompt，实际开头: %q", truncate(got, 80))
	}
	if !strings.Contains(got, "学科：生物") {
		t.Fatal("占位符没有被替换")
	}
	if strings.Contains(got, "资深") {
		t.Fatal("配了模板就不该再出现内置默认的正文")
	}
}

// 学科覆盖同样要真的到达构建器（依赖 PromptOptions.SubjectID 被填上）。
func TestSubjectOverrideReachesPromptBuilder(t *testing.T) {
	withStore(t, &memoryStore{byKey: map[string]string{
		PromptKeyImportExtract:                 "全局抽取",
		PromptKeyImportExtract + "@subject:12": "【历史学科抽取】{{subject_name}}",
	}})
	got := BuildSystemPrompt(PromptOptions{
		Mode:        ModeExtract,
		SubjectID:   idPtr(12),
		SubjectName: "历史",
	})
	if !strings.HasPrefix(got, "【历史学科抽取】历史") {
		t.Fatalf("学科覆盖没有到达 BuildSystemPrompt，实际 %q", truncate(got, 80))
	}

	// 换一个没有覆盖的学科，必须拿到全局模板而不是上面那条。
	other := BuildSystemPrompt(PromptOptions{
		Mode:        ModeExtract,
		SubjectID:   idPtr(13),
		SubjectName: "地理",
	})
	if strings.Contains(other, "【历史学科抽取】") {
		t.Fatal("别的学科不该命中这条覆盖")
	}
}

// 模板没引用输出结构时自动补上：抽取任务的输出形状是解析器的契约，
// 缺了它不会报错，只会让抽取结果莫名其妙地变差。
func TestSchemaIsAppendedWhenTemplateOmitsIt(t *testing.T) {
	withStore(t, &memoryStore{byKey: map[string]string{
		PromptKeyImportExtract: "你是严格遵守题库规范的{{subject_name}}老师。",
	}})
	got := BuildSystemPrompt(PromptOptions{Mode: ModeExtract, SubjectName: "数学"})
	if !strings.Contains(got, "你是严格遵守题库规范的数学老师。") {
		t.Fatalf("自定义正文应保留: %q", truncate(got, 80))
	}
	if !strings.Contains(got, `"questions"`) {
		t.Fatal("模板未引用 {{output_schema}} 时应自动补上输出结构")
	}
}

// 引用了 {{output_schema}} 时不能重复追加。
func TestSchemaIsNotAppendedTwice(t *testing.T) {
	withStore(t, &memoryStore{byKey: map[string]string{
		PromptKeyImportExtract: "正文\n{{output_schema}}",
	}})
	got := BuildSystemPrompt(PromptOptions{Mode: ModeExtract, SubjectName: "数学"})
	if n := strings.Count(got, `"questions"`); n != 1 {
		t.Fatalf("输出结构应只出现一次，实际 %d 次", n)
	}
}

// ---- 目录 ----

// 目录里每一条都必须给出内置默认：没有兜底的提示词一旦配置失效
// 就只能给模型一段空话，那比"不可配置"糟得多。
func TestEverySpecHasUsableDefault(t *testing.T) {
	specs := PromptSpecs()
	if len(specs) == 0 {
		t.Fatal("提示词目录为空")
	}
	seen := make(map[string]bool, len(specs))
	for _, s := range specs {
		if seen[s.Key] {
			t.Fatalf("提示词键重复: %s", s.Key)
		}
		seen[s.Key] = true
		if s.Name == "" || s.Description == "" {
			t.Fatalf("提示词 %s 缺少名称或说明", s.Key)
		}
		if strings.TrimSpace(s.Default) == "" {
			t.Fatalf("提示词 %s 没有内置默认模板", s.Key)
		}
		// 默认模板只能引用自己声明过的占位符：写了没声明的，
		// 管理员在界面上既看不到它、也不知道它不会被替换。
		for _, p := range s.Placeholders {
			if !strings.Contains(s.Default, "{{"+p.Name+"}}") {
				continue
			}
		}
		for name := range placeholderNames(s.Default) {
			if !hasPlaceholder(s.Placeholders, name) {
				t.Fatalf("提示词 %s 的默认模板用了未声明的占位符 {{%s}}", s.Key, name)
			}
		}
	}
}

// 录题两个键必须接上兜底设置键：它们曾经"声明了但无人读取"。
func TestImportSpecsHaveSettingFallback(t *testing.T) {
	for _, key := range []string{PromptKeyImportExtract, PromptKeyImportSolve} {
		spec, ok := SpecOf(key)
		if !ok {
			t.Fatalf("目录里缺少 %s", key)
		}
		if spec.SettingKey == "" {
			t.Fatalf("%s 没有设置兜底键", key)
		}
	}
	// 助手没有对应的历史设置键，就别硬凑一个。
	if spec, _ := SpecOf(PromptKeyAssistant); spec.SettingKey != "" {
		t.Fatal("助手提示词不应挂设置兜底键")
	}
}

func placeholderNames(tmpl string) map[string]bool {
	out := map[string]bool{}
	for _, m := range placeholderPattern.FindAllStringSubmatch(tmpl, -1) {
		out[m[1]] = true
	}
	return out
}

func hasPlaceholder(list []Placeholder, name string) bool {
	for _, p := range list {
		if p.Name == name {
			return true
		}
	}
	return false
}

func mustContain(t *testing.T, got string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Fatalf("提示词缺少片段 %q，实际内容:\n%s", w, truncate(got, 600))
		}
	}
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
