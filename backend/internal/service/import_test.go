package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/NanamiKyoka/questionmind/internal/importing"
	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/schema"
)

// fakeExtractor 起一个假的模型端点，返回给定的抽取结果 JSON。
func fakeExtractor(t *testing.T, questionsJSON string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 抽取是非流式请求，直接把内容包进标准响应体。
		inner, _ := json.Marshal(questionsJSON)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"choices":[{"message":{"content":%s}}]}`, inner)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// importFixture 搭好一套可用的环境：学科、知识点、标签、AI 配置。
type importFixture struct {
	repos   *Repos
	svc     *ImportService
	subject *model.Subject
	editor  *model.User
	admin   *model.User
}

func newImportFixture(t *testing.T, questionsJSON string) *importFixture {
	t.Helper()
	repos := newTestRepos(t)
	ctx := context.Background()

	admin := adminUser()
	subject := &model.Subject{
		Name: "数学", ReviewThreshold: 1, CreatedBy: admin.ID,
	}
	if err := repos.Subjects.Create(ctx, subject); err != nil {
		t.Fatalf("建学科失败: %v", err)
	}
	if err := repos.Subjects.AddMember(ctx, subject.ID, admin.ID, model.SubjectRoleManager); err != nil {
		t.Fatalf("加成员失败: %v", err)
	}

	// 预置一个知识点与一个标签，用于验证"按名称复用已有实体"。
	kp := &model.KnowledgePoint{SubjectID: subject.ID, Name: "函数单调性", Level: 1, CreatedBy: admin.ID}
	if err := repos.KnowledgePoints.Create(ctx, kp); err != nil {
		t.Fatalf("建知识点失败: %v", err)
	}
	if err := repos.KnowledgePoints.UpdateFields(ctx, kp.ID, map[string]any{"path": kpPath(kp.ID)}); err != nil {
		t.Fatalf("写路径失败: %v", err)
	}
	tag := &model.Tag{SubjectID: subject.ID, Name: "基础题"}
	if err := repos.Tags.Create(ctx, tag); err != nil {
		t.Fatalf("建标签失败: %v", err)
	}

	// 配好 AI：供应商指向假端点，模型设为默认文本模型。
	aiSvc := newAIService(t, repos)
	provider, err := aiSvc.CreateProvider(ctx, admin, &schema.ProviderCreateRequest{
		Name: "Fake", InterfaceType: "openai", BaseURL: fakeExtractor(t, questionsJSON).URL,
		APIKey: "sk-test",
	})
	if err != nil {
		t.Fatalf("建供应商失败: %v", err)
	}
	if _, err := aiSvc.CreateModel(ctx, admin, &schema.ModelCreateRequest{
		ProviderID: provider.ID, Name: "fake-model", ModelType: string(model.AIModelText),
	}); err != nil {
		t.Fatalf("建模型失败: %v", err)
	}

	// editor：有编辑权但没有审核权，用于验证默认入库状态。
	editor := &model.User{Base: model.Base{ID: 2}, Username: "editor", IsActive: true}
	editor.Memberships = []model.SubjectMember{
		{UserID: editor.ID, SubjectID: subject.ID, Role: model.SubjectRoleEditor},
	}

	questionSvc := NewQuestionService(repos)
	return &importFixture{
		repos:   repos,
		svc:     NewImportService(repos, aiSvc, questionSvc),
		subject: subject,
		editor:  editor,
		admin:   admin,
	}
}

func kpPath(id uint) string { return fmt.Sprintf("/%d/", id) }

const realisticExtraction = `{
  "questions": [
    {
      "temp_id": "q1",
      "question_type": "single_choice",
      "stem": "已知函数 $f(x)=x^2$，则 $f(2)=$（  ）",
      "options": [
        {"label":"A","content":"2"},
        {"label":"B","content":"4"},
        {"label":"C","content":"8"},
        {"label":"D","content":"16"}
      ],
      "answer": "B",
      "analysis": "代入得 $f(2)=4$。",
      "difficulty": 1,
      "knowledge_points": ["函数单调性"],
      "tags": ["基础题"]
    },
    {
      "temp_id": "q2",
      "question_type": "fill_blank",
      "stem": "三角形的内角和为 ______ 度。",
      "answer": "180",
      "analysis": "过顶点作平行线可证。"
    },
    {
      "temp_id": "q3",
      "question_type": "true_false",
      "stem": "所有的质数都是奇数。",
      "answer": "错",
      "analysis": "2 是质数但不是奇数。"
    }
  ]
}`

// 仅录题：模型按提示词要求只给题目，答案与解析一律留空。
const recordExtraction = `{
  "questions": [
    {
      "temp_id": "q1",
      "question_type": "single_choice",
      "stem": "已知函数 $f(x)=x^2$，则 $f(2)=$（  ）",
      "options": [
        {"label":"A","content":"2"},
        {"label":"B","content":"4"},
        {"label":"C","content":"8"},
        {"label":"D","content":"16"}
      ],
      "answer": "",
      "difficulty": 1
    },
    {
      "temp_id": "q2",
      "question_type": "essay",
      "stem": "证明：三角形的内角和为 180 度。",
      "answer": ""
    }
  ]
}`

// 仅录题的整条链路：草稿标成待补答案 → 入库成功 → 题目确实还没有答案。
// 中间任何一环把题目挡下来，这个模式就等于不能用。
func TestRecordModeCommitsQuestionsWithoutAnswers(t *testing.T) {
	fx := newImportFixture(t, recordExtraction)
	ctx := context.Background()

	res, err := fx.svc.Extract(ctx, fx.editor, fx.subject.ID, &schema.ExtractRequest{
		Content: "（试卷原文）",
		Mode:    string(importing.ModeRecord),
	})
	if err != nil {
		t.Fatalf("仅录题抽取失败: %v", err)
	}
	if len(res.Drafts) != 2 {
		t.Fatalf("应抽取 2 道题，实际 %d", len(res.Drafts))
	}
	for _, d := range res.Drafts {
		if d.Answer.Kind != model.AnswerKindPending {
			t.Fatalf("草稿答案应待补充，实际 %s", d.Answer.Kind)
		}
		if len(d.Warnings) != 0 {
			t.Fatalf("仅录题不该产生告警，实际 %v", d.Warnings)
		}
	}

	committed, err := fx.svc.Commit(ctx, fx.editor, fx.subject.ID, &schema.CommitRequest{Drafts: res.Drafts})
	if err != nil {
		t.Fatalf("入库失败: %v", err)
	}
	if committed.CreatedCount != 2 || committed.FailedCount != 0 {
		t.Fatalf("应入库 2 道题，实际成功 %d 失败 %d：%+v",
			committed.CreatedCount, committed.FailedCount, committed.Failures)
	}

	list, total, err := fx.repos.Questions.List(ctx, model.QuestionFilter{
		SubjectID: fx.subject.ID, ViewerID: fx.editor.ID,
	}, model.SortByCreatedDesc, 10, 0)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if total != 2 {
		t.Fatalf("库里应有 2 道题，实际 %d", total)
	}
	for i := range list {
		var answer model.AnswerSpec
		if err := list[i].Answer.Into(&answer); err != nil {
			t.Fatalf("解析答案失败: %v", err)
		}
		if answer.Kind != model.AnswerKindPending {
			t.Fatalf("入库后的答案应保持待补充，实际 %s", answer.Kind)
		}
	}
}

func TestExtractProducesDraftsWithoutWriting(t *testing.T) {
	fx := newImportFixture(t, realisticExtraction)
	ctx := context.Background()

	res, err := fx.svc.Extract(ctx, fx.editor, fx.subject.ID, &schema.ExtractRequest{
		Content: "（试卷原文，内容由假模型忽略）",
	})
	if err != nil {
		t.Fatalf("抽取失败: %v", err)
	}

	if res.Total != 3 || len(res.Drafts) != 3 {
		t.Fatalf("应抽取 3 道题，实际 %d", res.Total)
	}
	// 关键：抽取阶段绝不能写库。
	_, total, err := fx.repos.Questions.List(ctx, model.QuestionFilter{SubjectID: fx.subject.ID}, "", 100, 0)
	if err != nil {
		t.Fatalf("查询题目失败: %v", err)
	}
	if total != 0 {
		t.Fatalf("抽取阶段不应写入题库，实际有 %d 道题", total)
	}

	// 审计记录要落库，便于回答"这次导入发生了什么"。
	if res.TaskID == 0 {
		t.Fatal("应记录导入任务")
	}

	byType := map[model.QuestionType]importing.Draft{}
	for _, d := range res.Drafts {
		byType[d.QuestionType] = d
	}

	single, ok := byType[model.QuestionTypeSingleChoice]
	if !ok {
		t.Fatalf("缺少单选题，实际类型: %v", keysOfDrafts(res.Drafts))
	}
	if len(single.Options) != 4 {
		t.Fatalf("单选题应有 4 个选项，实际 %d", len(single.Options))
	}
	if len(single.Answer.Correct) != 1 || single.Answer.Correct[0] != "B" {
		t.Fatalf("答案应为 B，实际 %v", single.Answer.Correct)
	}

	fill, ok := byType[model.QuestionTypeFillBlank]
	if !ok {
		t.Fatal("缺少填空题")
	}
	if len(fill.Answer.Blanks) != 1 {
		t.Fatalf("填空题应有 1 个空，实际 %d", len(fill.Answer.Blanks))
	}

	tf, ok := byType[model.QuestionTypeTrueFalse]
	if !ok {
		t.Fatal("缺少判断题")
	}
	if tf.Answer.BoolAnswer == nil || *tf.Answer.BoolAnswer {
		t.Fatal("判断题答案应为「错」")
	}
}

func TestCommitWritesQuestions(t *testing.T) {
	fx := newImportFixture(t, realisticExtraction)
	ctx := context.Background()

	extracted, err := fx.svc.Extract(ctx, fx.editor, fx.subject.ID, &schema.ExtractRequest{Content: "x"})
	if err != nil {
		t.Fatalf("抽取失败: %v", err)
	}

	committed, err := fx.svc.Commit(ctx, fx.editor, fx.subject.ID, &schema.CommitRequest{
		Drafts: extracted.Drafts,
	})
	if err != nil {
		t.Fatalf("入库失败: %v", err)
	}

	if committed.CreatedCount != 3 {
		t.Fatalf("应入库 3 道题，实际 %d，失败 %+v", committed.CreatedCount, committed.Failures)
	}
	if committed.FailedCount != 0 {
		t.Fatalf("不应有失败项: %+v", committed.Failures)
	}

	// 从库里读回来核对。
	list, total, err := fx.repos.Questions.List(ctx, model.QuestionFilter{
		SubjectID: fx.subject.ID, ViewerID: fx.editor.ID,
	}, model.SortByCreatedDesc, 100, 0)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if total != 3 {
		t.Fatalf("库里应有 3 道题，实际 %d", total)
	}

	var single *model.Question
	for i := range list {
		if list[i].QuestionType == model.QuestionTypeSingleChoice {
			single = &list[i]
		}
	}
	if single == nil {
		t.Fatal("库里缺少单选题")
	}

	// editor 没有审核权，入库应为草稿。
	if single.Status != model.QuestionStatusDraft {
		t.Fatalf("editor 录入的题应为草稿，实际 %s", single.Status)
	}
	// 答案里的标签应已被解析为选项 id。
	var answer model.AnswerSpec
	if err := single.Answer.Into(&answer); err != nil {
		t.Fatalf("解析答案失败: %v", err)
	}
	if len(answer.Correct) != 1 || !strings.HasPrefix(answer.Correct[0], "opt_") {
		t.Fatalf("答案应已解析为选项 id，实际 %v", answer.Correct)
	}
	// 题干应存为富文本而不是 Markdown 原文。
	if strings.Contains(string(single.Stem), "（  ）") && !strings.Contains(string(single.Stem), `"type":"doc"`) {
		t.Fatal("题干应转换为富文本结构")
	}
	if !strings.Contains(single.StemText, "f(x)") && !strings.Contains(single.StemText, "已知函数") {
		t.Fatalf("题干纯文本提取失败: %q", single.StemText)
	}

	// 名称关联应命中已有实体。
	tags, _ := fx.repos.Questions.TagsForQuestions(ctx, []uint{single.ID})
	if len(tags[single.ID]) != 1 || tags[single.ID][0].Name != "基础题" {
		t.Fatalf("标签应按名称关联到已有实体，实际 %+v", tags[single.ID])
	}
	kps, _ := fx.repos.Questions.KnowledgePointsForQuestions(ctx, []uint{single.ID})
	if len(kps[single.ID]) != 1 || kps[single.ID][0].Name != "函数单调性" {
		t.Fatalf("知识点应按名称关联到已有实体，实际 %+v", kps[single.ID])
	}
}

// 部分成功：一道题不合格不应让整批失败。
func TestCommitPartialSuccess(t *testing.T) {
	fx := newImportFixture(t, realisticExtraction)
	ctx := context.Background()

	drafts := []importing.Draft{
		{
			QuestionType: model.QuestionTypeEssay,
			StemMarkdown: "证明：三角形内角和为 180 度。",
			Answer:       model.AnswerSpec{Kind: "reference", Reference: importing.MarkdownToRichDoc("略")},
		},
		{
			// 这一题非法：开放式题却带了选项，domain 校验会拒绝。
			QuestionType: model.QuestionTypeEssay,
			StemMarkdown: "非法题目",
			Options:      []model.Option{{Label: "A", Content: importing.MarkdownToRichDoc("甲")}},
			Answer:       model.AnswerSpec{Kind: "reference", Reference: importing.MarkdownToRichDoc("略")},
		},
		{
			QuestionType: model.QuestionTypeTrueFalse,
			StemMarkdown: "1 是质数。",
			Answer:       model.AnswerSpec{Kind: "boolean", BoolAnswer: boolPtr(false)},
		},
	}

	res, err := fx.svc.Commit(ctx, fx.editor, fx.subject.ID, &schema.CommitRequest{Drafts: drafts})
	if err != nil {
		t.Fatalf("提交不应整体失败: %v", err)
	}
	if res.CreatedCount != 2 {
		t.Fatalf("应有 2 道成功，实际 %d", res.CreatedCount)
	}
	if res.FailedCount != 1 {
		t.Fatalf("应有 1 道失败，实际 %d", res.FailedCount)
	}
	// 失败项要能定位到具体是哪一题，前端才好把用户填的内容留住。
	if res.Failures[0].Index != 1 {
		t.Fatalf("失败项下标应为 1，实际 %d", res.Failures[0].Index)
	}
	if res.Failures[0].Stem == "" {
		t.Fatal("失败项应带上题干片段便于定位")
	}
	if res.Failures[0].Error == "" {
		t.Fatal("失败项应带上失败原因")
	}
}

// 草稿里提到但本学科没有的知识点，应回传而不是自动创建。
func TestCommitReportsUnresolvedNames(t *testing.T) {
	fx := newImportFixture(t, realisticExtraction)
	ctx := context.Background()

	res, err := fx.svc.Commit(ctx, fx.editor, fx.subject.ID, &schema.CommitRequest{
		Drafts: []importing.Draft{{
			QuestionType:    model.QuestionTypeEssay,
			StemMarkdown:    "题目",
			Answer:          model.AnswerSpec{Kind: "reference", Reference: importing.MarkdownToRichDoc("略")},
			KnowledgePoints: []string{"函数单调性", "一个本学科不存在的知识点"},
			Tags:            []string{"一个本学科不存在的标签"},
		}},
	})
	if err != nil {
		t.Fatalf("入库失败: %v", err)
	}
	if res.CreatedCount != 1 {
		t.Fatalf("应成功入库 1 道，实际 %d", res.CreatedCount)
	}

	joined := strings.Join(res.UnresolvedNames, ",")
	if !strings.Contains(joined, "不存在的知识点") || !strings.Contains(joined, "不存在的标签") {
		t.Fatalf("应回传未能匹配的名称，实际 %v", res.UnresolvedNames)
	}
	if strings.Contains(joined, "函数单调性") {
		t.Fatalf("已命中的名称不应出现在未匹配列表: %v", res.UnresolvedNames)
	}

	// 未匹配的名称绝不能被自动创建。
	kpNames, _ := fx.repos.KnowledgePoints.NamesBySubject(ctx, fx.subject.ID)
	for _, n := range kpNames {
		if n == "一个本学科不存在的知识点" {
			t.Fatal("不应自动创建知识点")
		}
	}
}

// 无 AI 配置时要给出可操作的提示，而不是笼统的失败。
func TestExtractWithoutAIConfig(t *testing.T) {
	repos := newTestRepos(t)
	ctx := context.Background()

	admin := adminUser()
	subject := &model.Subject{Name: "数学", ReviewThreshold: 1, CreatedBy: admin.ID}
	repos.Subjects.Create(ctx, subject)
	repos.Subjects.AddMember(ctx, subject.ID, admin.ID, model.SubjectRoleManager)

	aiSvc := newAIService(t, repos)
	svc := NewImportService(repos, aiSvc, NewQuestionService(repos))

	_, err := svc.Extract(ctx, admin, subject.ID, &schema.ExtractRequest{Content: "题目"})
	if err == nil {
		t.Fatal("未配置模型时应报错")
	}
	if !strings.Contains(err.Error(), "文本模型") {
		t.Fatalf("错误信息应指明缺少文本模型，实际: %v", err)
	}
}

func TestExtractValidatesInput(t *testing.T) {
	fx := newImportFixture(t, realisticExtraction)
	ctx := context.Background()

	cases := []struct {
		name string
		req  *schema.ExtractRequest
	}{
		{"既无文本也无图片", &schema.ExtractRequest{}},
		{"只有空白文本", &schema.ExtractRequest{Content: "   "}},
		{"图片不是合法地址", &schema.ExtractRequest{Images: []string{"not-a-url"}}},
		{"模式非法", &schema.ExtractRequest{Content: "x", Mode: "unknown"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := fx.svc.Extract(ctx, fx.editor, fx.subject.ID, tc.req); err == nil {
				t.Fatal("应被拒绝")
			}
		})
	}
}

// solve 模式应使用另一份内容字段。
func TestExtractSolveModeUsesSolveContent(t *testing.T) {
	fx := newImportFixture(t, `{"questions":[{"temp_id":"q1","stem":"原题干","answer":"42","analysis":"计算得 42"}]}`)
	ctx := context.Background()

	res, err := fx.svc.Extract(ctx, fx.editor, fx.subject.ID, &schema.ExtractRequest{
		Mode:            "solve",
		Content:         "这段内容应被忽略",
		SolveForContent: "1. 求 6×7 的值。",
	})
	if err != nil {
		t.Fatalf("solve 模式失败: %v", err)
	}
	if len(res.Drafts) != 1 {
		t.Fatalf("应有 1 道题，实际 %d", len(res.Drafts))
	}
	if res.Drafts[0].Answer.Kind != "reference" {
		t.Fatalf("解答题答案类型应为 reference，实际 %s", res.Drafts[0].Answer.Kind)
	}
}

func TestImportRequiresEditPermission(t *testing.T) {
	fx := newImportFixture(t, realisticExtraction)
	ctx := context.Background()

	outsider := &model.User{Base: model.Base{ID: 9}, Username: "outsider", IsActive: true}
	if _, err := fx.svc.Extract(ctx, outsider, fx.subject.ID, &schema.ExtractRequest{Content: "x"}); err == nil {
		t.Fatal("非成员不应能发起导入")
	}

	viewer := &model.User{Base: model.Base{ID: 10}, Username: "viewer", IsActive: true}
	viewer.Memberships = []model.SubjectMember{
		{UserID: viewer.ID, SubjectID: fx.subject.ID, Role: model.SubjectRoleViewer},
	}
	if _, err := fx.svc.Extract(ctx, viewer, fx.subject.ID, &schema.ExtractRequest{Content: "x"}); err == nil {
		t.Fatal("只读角色不应能发起导入")
	}
}

func boolPtr(v bool) *bool { return &v }

func keysOfDrafts(drafts []importing.Draft) []model.QuestionType {
	out := make([]model.QuestionType, 0, len(drafts))
	for _, d := range drafts {
		out = append(out, d.QuestionType)
	}
	return out
}
