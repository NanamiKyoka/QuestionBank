package service

import (
	"context"
	"strings"
	"testing"

	"github.com/NanamiKyoka/questionmind/internal/aiagent"
	"github.com/NanamiKyoka/questionmind/internal/importing"
	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
	"github.com/NanamiKyoka/questionmind/internal/schema"
)

// 提示词模板的用例测试。
//
// 这些用例盯的是三件事：
//  1. 解析优先级（学科覆盖 > 全局 > 系统设置 > 内置默认）；
//  2. 配了的模板**真的**到达了提示词构建器（不是存进去就完事）；
//  3. 任何坏配置都只降级、不报错——一次导入不能因为一段提示词失败。

func newPromptFixture(t *testing.T) (context.Context, *PromptService, *Repos, *model.User, *model.Subject) {
	t.Helper()
	ctx := context.Background()
	repos := newTestRepos(t)
	svc := NewPromptService(repos)

	admin := adminUser()
	subject := &model.Subject{Name: "数学", Description: "含函数与导数", ReviewThreshold: 1, CreatedBy: admin.ID}
	if err := repos.Subjects.Create(ctx, subject); err != nil {
		t.Fatalf("建学科失败: %v", err)
	}
	return ctx, svc, repos, admin, subject
}

// ---- 鉴权 ----

// 提示词决定的是全系统送给模型的话术：普通用户改它等于改了所有人的
// 智能录题结果，因此整组接口只对全局管理员开放。
func TestPromptWritesRequireSuperuser(t *testing.T) {
	ctx, svc, _, _, subject := newPromptFixture(t)
	req := &schema.PromptCreateRequest{Key: importing.PromptKeyImportExtract, SystemPrompt: "x"}

	if _, err := svc.Create(ctx, normalUser(), req); !apperr.Is(err, apperr.CodeForbidden) {
		t.Fatalf("普通用户应被拒绝，实际 %v", err)
	}
	if _, err := svc.Create(ctx, nil, req); !apperr.Is(err, apperr.CodeUnauthorized) {
		t.Fatalf("空用户应返回未登录，实际 %v", err)
	}
	if _, err := svc.List(ctx, normalUser()); !apperr.Is(err, apperr.CodeForbidden) {
		t.Fatalf("列表也应拒绝普通用户，实际 %v", err)
	}
	if _, err := svc.SaveSubjectPrompts(ctx, normalUser(), subject.ID, &schema.SubjectPromptsUpdateRequest{
		Prompts: []schema.SubjectPromptUpsert{{Key: importing.PromptKeyImportExtract, SystemPrompt: "x"}},
	}); !apperr.Is(err, apperr.CodeForbidden) {
		t.Fatalf("学科覆盖写入应拒绝普通用户，实际 %v", err)
	}
	if err := svc.Delete(ctx, normalUser(), 1); !apperr.Is(err, apperr.CodeForbidden) {
		t.Fatalf("删除应拒绝普通用户，实际 %v", err)
	}
}

// 目录之外的键一律拒绝：存一条永远没人读的模板，只会让管理员以为改动生效了。
func TestPromptCreateRejectsUnknownKey(t *testing.T) {
	ctx, svc, _, admin, _ := newPromptFixture(t)

	_, err := svc.Create(ctx, admin, &schema.PromptCreateRequest{Key: "自编提示词", SystemPrompt: "x"})
	if !apperr.Is(err, apperr.CodeInvalidParam) {
		t.Fatalf("目录外的键应返回 400，实际 %v", err)
	}
	// 学科不存在时也不能建：否则会留下一条永远命不中的孤儿覆盖。
	_, err = svc.Create(ctx, admin, &schema.PromptCreateRequest{
		Key: importing.PromptKeyImportExtract, SubjectID: ptrUint(9999), SystemPrompt: "x",
	})
	if !apperr.Is(err, apperr.CodeNotFound) {
		t.Fatalf("不存在的学科应返回 404，实际 %v", err)
	}
	// 空正文不能存：空串等同于"没配"，存进去只会让人以为存成功了。
	_, err = svc.Create(ctx, admin, &schema.PromptCreateRequest{
		Key: importing.PromptKeyImportExtract, SystemPrompt: "   \n ",
	})
	if !apperr.Is(err, apperr.CodeInvalidParam) {
		t.Fatalf("空正文应被拒绝，实际 %v", err)
	}
}

// ---- CRUD ----

func TestPromptTemplateCRUD(t *testing.T) {
	ctx, svc, _, admin, _ := newPromptFixture(t)
	key := importing.PromptKeyImportExtract

	created, err := svc.Create(ctx, admin, &schema.PromptCreateRequest{Key: key, SystemPrompt: "第一版\n{{subject_name}}"})
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if created.Key != key || created.Placeholders == nil {
		t.Fatalf("响应里应带上提示词键与占位符清单，实际 %+v", created)
	}

	// 同一个键不能建两次：重复键会让"我改的是哪一条"变得不可解释。
	if _, err := svc.Create(ctx, admin, &schema.PromptCreateRequest{Key: key, SystemPrompt: "重复"}); !apperr.Is(err, apperr.CodeDuplicateName) {
		t.Fatalf("重复键应被拒绝，实际 %v", err)
	}

	updated, err := svc.Update(ctx, admin, created.ID, &schema.PromptUpdateRequest{SystemPrompt: ptrString("第二版")})
	if err != nil {
		t.Fatalf("更新失败: %v", err)
	}
	if updated.SystemPrompt != "第二版" {
		t.Fatalf("更新未生效，实际 %q", updated.SystemPrompt)
	}
	if updated.UpdatedBy != admin.ID {
		t.Fatalf("updated_by 应为 %d，实际 %d", admin.ID, updated.UpdatedBy)
	}

	hit, err := svc.LookupTemplate(ctx, key, nil)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if hit.Source != importing.SourceGlobal || !strings.HasPrefix(hit.Body, "第二版") {
		t.Fatalf("更新后应立即生效，实际 %s / %q", hit.Source, hit.Body)
	}

	// 删掉之后回退到内置默认：删模板是"关掉覆盖"，不是"禁用"。
	if err := svc.Delete(ctx, admin, created.ID); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	hit, err = svc.LookupTemplate(ctx, key, nil)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if hit.Body != "" {
		t.Fatalf("删除后不应再命中模板，实际 %q", hit.Body)
	}
	// 硬删除：软删除的行仍然占着唯一索引，会让"重新配同一个提示词"失败。
	if _, err := svc.Create(ctx, admin, &schema.PromptCreateRequest{Key: key, SystemPrompt: "重建"}); err != nil {
		t.Fatalf("删除后应能重新创建，实际 %v", err)
	}
}

// ---- 解析优先级 ----

// 学科覆盖 > 全局模板：这是"数学与语文的抽取要求本就不同"能落地的前提。
func TestPromptSubjectOverrideWinsOverGlobal(t *testing.T) {
	ctx, svc, repos, admin, subject := newPromptFixture(t)
	key := importing.PromptKeyImportExtract

	if _, err := svc.Create(ctx, admin, &schema.PromptCreateRequest{Key: key, SystemPrompt: "全局抽取"}); err != nil {
		t.Fatalf("建全局模板失败: %v", err)
	}

	// 另一个学科只配全局模板。
	other := &model.Subject{Name: "语文", ReviewThreshold: 1, CreatedBy: admin.ID}
	if err := repos.Subjects.Create(ctx, other); err != nil {
		t.Fatalf("建学科失败: %v", err)
	}

	res, err := svc.SaveSubjectPrompts(ctx, admin, subject.ID, &schema.SubjectPromptsUpdateRequest{
		Prompts: []schema.SubjectPromptUpsert{{Key: key, SystemPrompt: "数学专用抽取 {{subject_name}}"}},
	})
	if err != nil {
		t.Fatalf("保存学科覆盖失败: %v", err)
	}

	hit, err := svc.LookupTemplate(ctx, key, &subject.ID)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if hit.Source != importing.SourceSubject || !strings.Contains(hit.Body, "数学专用") {
		t.Fatalf("本学科应命中自己的覆盖，实际 %s / %q", hit.Source, hit.Body)
	}

	// 另一个学科必须回退到全局模板，不能串台。
	hit, err = svc.LookupTemplate(ctx, key, &other.ID)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if hit.Source != importing.SourceGlobal || !strings.Contains(hit.Body, "全局抽取") {
		t.Fatalf("未覆盖的学科应命中全局模板，实际 %s / %q", hit.Source, hit.Body)
	}

	// 响应里要能看出覆盖存在、能看出最终生效的是哪一份。
	var math *schema.SubjectPromptItem
	for i := range res.Prompts {
		if res.Prompts[i].Key == key {
			math = &res.Prompts[i]
		}
	}
	if math == nil || math.OverrideID == 0 || math.Source != importing.SourceSubject {
		t.Fatalf("学科响应应标出覆盖存在且已生效，实际 %+v", math)
	}
	if !strings.Contains(math.Preview, "数学") {
		t.Fatalf("预览应代入学科名，实际 %q", math.Preview)
	}

	// 空正文 = 删除覆盖。
	res, err = svc.SaveSubjectPrompts(ctx, admin, subject.ID, &schema.SubjectPromptsUpdateRequest{
		Prompts: []schema.SubjectPromptUpsert{{Key: key, SystemPrompt: ""}},
	})
	if err != nil {
		t.Fatalf("删除覆盖失败: %v", err)
	}
	hit, err = svc.LookupTemplate(ctx, key, &subject.ID)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if hit.Source != importing.SourceGlobal {
		t.Fatalf("删除覆盖后应回退到全局，实际 %s", hit.Source)
	}
	for i := range res.Prompts {
		if res.Prompts[i].Key == key && res.Prompts[i].OverrideID != 0 {
			t.Fatalf("覆盖应已删除，实际 override_id=%d", res.Prompts[i].OverrideID)
		}
	}
}

// 一个批次里出现重复键必须被拒绝：后写的会静默覆盖先写的。
func TestSaveSubjectPromptsRejectsDuplicateKeys(t *testing.T) {
	ctx, svc, _, admin, subject := newPromptFixture(t)
	key := importing.PromptKeyImportExtract

	_, err := svc.SaveSubjectPrompts(ctx, admin, subject.ID, &schema.SubjectPromptsUpdateRequest{
		Prompts: []schema.SubjectPromptUpsert{
			{Key: key, SystemPrompt: "一"},
			{Key: key, SystemPrompt: "二"},
		},
	})
	if !apperr.Is(err, apperr.CodeInvalidParam) {
		t.Fatalf("重复键应返回 400，实际 %v", err)
	}
	hit, err := svc.LookupTemplate(ctx, key, &subject.ID)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if hit.Body != "" {
		t.Fatalf("整批校验失败时不应写入任何一条，实际 %q", hit.Body)
	}
}

// ---- 系统设置兜底 ----

// ai_extract_prompt / ai_solve_prompt 曾经"声明了但全仓无人读取"。
// 现在它们接在解析链的最后一层：没有学科覆盖、也没有全局模板时才生效。
func TestPromptSettingIsUsedAsLastResortFallback(t *testing.T) {
	ctx, svc, repos, admin, _ := newPromptFixture(t)
	key := importing.PromptKeyImportExtract

	if err := repos.Settings.Set(ctx, model.SettingAIExtractPrompt, "系统设置里的抽取提示词", "历史遗留", admin.ID); err != nil {
		t.Fatalf("写设置失败: %v", err)
	}
	hit, err := svc.LookupTemplate(ctx, key, nil)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if hit.Source != importing.SourceSetting || hit.Body != "系统设置里的抽取提示词" {
		t.Fatalf("应命中设置兜底，实际 %s / %q", hit.Source, hit.Body)
	}

	// 模板表优先级更高：一旦管理员在「提示词」页配了，设置里这份就不再被读。
	if _, err := svc.Create(ctx, admin, &schema.PromptCreateRequest{Key: key, SystemPrompt: "模板表里的抽取提示词"}); err != nil {
		t.Fatalf("建模板失败: %v", err)
	}
	hit, err = svc.LookupTemplate(ctx, key, nil)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if hit.Source != importing.SourceGlobal {
		t.Fatalf("模板表应压过设置兜底，实际 %s", hit.Source)
	}
}

// ---- 真的被读到 ----

// 本次改造的核心承诺：存进去的模板必须真的出现在送进模型的提示词里。
// 这条用例同时覆盖智能录题与 AI 助手两条链路。
func TestStoredTemplatesActuallyReachPromptBuilders(t *testing.T) {
	ctx, svc, _, admin, subject := newPromptFixture(t)

	// 1. 全局录题模板 → importing.BuildSystemPrompt
	if _, err := svc.Create(ctx, admin, &schema.PromptCreateRequest{
		Key:          importing.PromptKeyImportExtract,
		SystemPrompt: "【全局抽取】学科 {{subject_name}}\n{{output_schema}}",
	}); err != nil {
		t.Fatalf("建模板失败: %v", err)
	}
	got := importing.BuildSystemPrompt(importing.PromptOptions{
		Mode: importing.ModeExtract, SubjectName: subject.Name,
	})
	if !strings.HasPrefix(got, "【全局抽取】学科 数学") {
		t.Fatalf("全局模板没有到达 BuildSystemPrompt，实际 %q", got)
	}

	// 2. 学科覆盖 → 同一学科命中覆盖、别的学科不命中
	if _, err := svc.SaveSubjectPrompts(ctx, admin, subject.ID, &schema.SubjectPromptsUpdateRequest{
		Prompts: []schema.SubjectPromptUpsert{
			{Key: importing.PromptKeyImportExtract, SystemPrompt: "【数学专用】{{subject_name}}\n{{output_schema}}"},
		},
	}); err != nil {
		t.Fatalf("保存学科覆盖失败: %v", err)
	}
	opt := importing.PromptOptions{
		Mode: importing.ModeExtract, SubjectID: &subject.ID, SubjectName: subject.Name,
	}
	if got := importing.BuildSystemPrompt(opt); !strings.HasPrefix(got, "【数学专用】数学") {
		t.Fatalf("学科覆盖没有到达 BuildSystemPrompt，实际 %q", got)
	}
	noSubject := importing.BuildSystemPrompt(importing.PromptOptions{
		Mode: importing.ModeExtract, SubjectName: "语文",
	})
	if !strings.HasPrefix(noSubject, "【全局抽取】") {
		t.Fatalf("未覆盖的学科应回退到全局模板，实际 %q", noSubject)
	}

	// 3. 助手模板 → ChatService.systemPrompt
	if _, err := svc.Create(ctx, admin, &schema.PromptCreateRequest{
		Key:          importing.PromptKeyAssistant,
		SystemPrompt: "【自定义助手】你是{{subject_name}}助教。{{scene_hint}}",
	}); err != nil {
		t.Fatalf("建助手模板失败: %v", err)
	}
	chat := &ChatService{repos: svc.repos}
	sp := chat.systemPrompt(ctx, aiagent.SceneQuestionLibrary, subject.ID)
	if !strings.Contains(sp, "【自定义助手】") {
		t.Fatalf("助手模板没有到达 systemPrompt，实际 %q", sp)
	}
	if !strings.Contains(sp, "题库页面") {
		t.Fatalf("{{scene_hint}} 应被替换成场景提示，实际 %q", sp)
	}
	if !strings.Contains(sp, "数学") {
		t.Fatalf("{{subject_name}} 应被替换成学科名，实际 %q", sp)
	}
}

// 助手模板也要能按学科覆盖：会话的 subject_id 是现成的。
func TestAssistantTemplateSupportsSubjectOverride(t *testing.T) {
	ctx, svc, _, admin, subject := newPromptFixture(t)
	if _, err := svc.Create(ctx, admin, &schema.PromptCreateRequest{
		Key: importing.PromptKeyAssistant, SystemPrompt: "【全局助手】",
	}); err != nil {
		t.Fatalf("建模板失败: %v", err)
	}
	if _, err := svc.SaveSubjectPrompts(ctx, admin, subject.ID, &schema.SubjectPromptsUpdateRequest{
		Prompts: []schema.SubjectPromptUpsert{
			{Key: importing.PromptKeyAssistant, SystemPrompt: "【数学助教】"},
		},
	}); err != nil {
		t.Fatalf("保存覆盖失败: %v", err)
	}

	chat := &ChatService{repos: svc.repos}
	if sp := chat.systemPrompt(ctx, aiagent.SceneUnscoped, subject.ID); !strings.Contains(sp, "【数学助教】") {
		t.Fatalf("应命中学科覆盖，实际 %q", sp)
	}
	// 没有学科的会话走全局模板。
	if sp := chat.systemPrompt(ctx, aiagent.SceneUnscoped, 0); !strings.Contains(sp, "【全局助手】") {
		t.Fatalf("无学科会话应命中全局模板，实际 %q", sp)
	}
}

// 没配任何模板时，助手提示词必须与改造前的硬编码版本一致。
// 少了这条，"接上配置"本身就可能悄悄改变每一次对话的行为。
func TestAssistantPromptFallsBackToLegacyText(t *testing.T) {
	ctx, svc, _, _, _ := newPromptFixture(t)
	chat := &ChatService{repos: svc.repos}
	sp := chat.systemPrompt(ctx, aiagent.SceneUnscoped, 0)

	for _, want := range []string{
		"你是 QuestionMind 题库系统的 AI 助手，帮助教师管理题目和组卷。",
		"1. 需要题库信息时先调用工具查，不要凭空编造题目 id 或内容。",
		"7. 找不到合适内容时如实说明，不要勉强凑数。",
		// 预演约定必须写进默认提示词：dry_run 只是工具上的一个参数，
		// 模型不会因为它"叫这个名字"就知道该怎么用它。
		"dry_run",
	} {
		if !strings.Contains(sp, want) {
			t.Fatalf("内置默认缺少片段 %q，实际 %q", want, sp)
		}
	}
	if strings.Contains(sp, "{{") {
		t.Fatalf("不应残留未渲染的占位符: %q", sp)
	}
}

// 坏模板只降级不报错：助手这一侧同理，不能让一轮对话发不出去。
func TestAssistantPromptSurvivesBrokenTemplate(t *testing.T) {
	ctx, svc, repos, admin, subject := newPromptFixture(t)
	if _, err := svc.Create(ctx, admin, &schema.PromptCreateRequest{
		// 只有一个可选占位符、且取值为空 → 渲染成空 → 必须回落。
		Key: importing.PromptKeyAssistant, SystemPrompt: "{{subject_description}}",
	}); err != nil {
		t.Fatalf("建模板失败: %v", err)
	}
	// 让 description 有值，模板就能渲染；换成没有 description 的学科则应当回落。
	if _, err := svc.SaveSubjectPrompts(ctx, admin, subject.ID, &schema.SubjectPromptsUpdateRequest{
		Prompts: []schema.SubjectPromptUpsert{{Key: importing.PromptKeyAssistant, SystemPrompt: ""}},
	}); err != nil {
		t.Fatalf("清理覆盖失败: %v", err)
	}
	bare := &model.Subject{Name: "体育", ReviewThreshold: 1, CreatedBy: admin.ID}
	if err := repos.Subjects.Create(ctx, bare); err != nil {
		t.Fatalf("建学科失败: %v", err)
	}

	chat := &ChatService{repos: svc.repos}
	sp := chat.systemPrompt(ctx, aiagent.SceneUnscoped, bare.ID)
	if !strings.Contains(sp, "你是 QuestionMind 题库系统的 AI 助手") {
		t.Fatalf("坏模板应回落到内置默认，实际 %q", sp)
	}
}

// ---- 列表 ----

// 列表必须包含全部三个提示词，哪怕一条都没配：
// 只列数据库里有的行，管理员会以为这个功能不存在。
func TestPromptListShowsUnconfiguredPrompts(t *testing.T) {
	ctx, svc, _, _, _ := newPromptFixture(t)
	res, err := svc.List(ctx, adminUser())
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if len(res.Items) != len(importing.PromptSpecs()) {
		t.Fatalf("应返回全部 %d 个提示词，实际 %d", len(importing.PromptSpecs()), len(res.Items))
	}
	for _, item := range res.Items {
		if item.Template != nil {
			t.Fatalf("%s 还没配置，不应有模板", item.Key)
		}
		if item.EffectiveSource != importing.SourceBuiltin {
			t.Fatalf("%s 应标为内置默认，实际 %s", item.Key, item.EffectiveSource)
		}
		if item.Fallback == "" {
			t.Fatalf("%s 未配置时应说明原因", item.Key)
		}
		if item.Default == "" {
			t.Fatalf("%s 缺少内置默认正文，界面无法提供对照", item.Key)
		}
		if len(item.Placeholders) == 0 {
			t.Fatalf("%s 缺少占位符说明", item.Key)
		}
	}
}

// 存下来了、但渲染不出内容的模板必须被界面指出来。
// 这是提示词配置里最糟的一种状态：看着配好了，运行时却每次都悄悄用默认。
func TestPromptListWarnsAboutUnusableStoredTemplate(t *testing.T) {
	ctx, svc, _, admin, _ := newPromptFixture(t)
	key := importing.PromptKeyImportExtract

	// 只含空白的正文在写入时就会被挡下：空串等同于"没配"。
	if _, err := svc.Create(ctx, admin, &schema.PromptCreateRequest{
		Key: key, SystemPrompt: "     \n\t ",
	}); !apperr.Is(err, apperr.CodeInvalidParam) {
		t.Fatalf("空白正文不应被存下，实际 %v", err)
	}

	// 直接落一行"只有一个可选占位符"的模板：有上下文时它能渲染，
	// 没有上下文时渲染成空——正是那种"存了却不生效"的配置。
	if err := svc.tpl.Create(ctx, &model.PromptTemplate{
		Key: key, Name: "x", Scene: "import", SystemPrompt: "{{knowledge_point_block}}", UpdatedBy: admin.ID,
	}); err != nil {
		t.Fatalf("预置数据失败: %v", err)
	}

	res, err := svc.List(ctx, admin)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	var found bool
	for _, item := range res.Items {
		if item.Key != key {
			continue
		}
		found = true
		if item.Template == nil {
			t.Fatal("应能看到已保存的模板")
		}
		if item.Fallback == "" {
			t.Fatal("存了却渲染不出内容的模板必须给出提示")
		}
	}
	if !found {
		t.Fatal("列表里缺少该提示词")
	}
}

func ptrUint(v uint) *uint       { return &v }
func ptrString(v string) *string { return &v }
