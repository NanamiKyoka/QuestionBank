package service

import (
	"context"
	"strings"
	"testing"

	"github.com/NanamiKyoka/questionmind/internal/importing"
	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
	"github.com/NanamiKyoka/questionmind/internal/schema"
)

// addVisionModel 给 fixture 补一个视觉模型（默认文本模型之外的第二类模型）。
func addVisionModel(t *testing.T, fx *importFixture, questionsJSON string) {
	t.Helper()
	provider, err := fx.svc.aiConfig.CreateProvider(context.Background(), fx.admin, &schema.ProviderCreateRequest{
		Name: "FakeVision", InterfaceType: "openai",
		BaseURL: fakeExtractor(t, questionsJSON).URL,
		APIKey:  "sk-test",
	})
	if err != nil {
		t.Fatalf("建视觉供应商失败: %v", err)
	}
	if _, err := fx.svc.aiConfig.CreateModel(context.Background(), fx.admin, &schema.ModelCreateRequest{
		ProviderID: provider.ID, Name: "fake-vision", ModelType: string(model.AIModelVision),
	}); err != nil {
		t.Fatalf("建视觉模型失败: %v", err)
	}
}

// 未配置视觉模型时必须明确报错，而不是把图片悄悄发给纯文本模型。
// 后者通常不会立刻失败，而是让模型"看不见"地编一份题出来——
// 错误答案比没有答案危险得多。
func TestExtractImagesRequireVisionModel(t *testing.T) {
	fx := newImportFixture(t, realisticExtraction)
	ctx := context.Background()

	_, err := fx.svc.Extract(ctx, fx.editor, fx.subject.ID, &schema.ExtractRequest{
		Images: []string{"data:image/png;base64,AAA"},
	})
	if err == nil {
		t.Fatal("未配置视觉模型时识图应报错")
	}
	if !apperr.Is(err, apperr.CodeAIUnavailable) {
		t.Fatalf("应报 AI 不可用（503），实际: %v", err)
	}
	if !strings.Contains(err.Error(), "视觉模型") {
		t.Fatalf("错误信息应指出缺少视觉模型，实际: %v", err)
	}
	if !strings.Contains(err.Error(), "AI 设置") {
		t.Fatalf("错误信息应给出可操作的下一步，实际: %v", err)
	}
}

// 配了视觉模型后，识图走视觉模型，纯文本导入仍然走文本模型。
func TestExtractImagesUseVisionModel(t *testing.T) {
	fx := newImportFixture(t, realisticExtraction)
	ctx := context.Background()
	addVisionModel(t, fx, realisticExtraction)

	res, err := fx.svc.Extract(ctx, fx.editor, fx.subject.ID, &schema.ExtractRequest{
		Images: []string{"data:image/png;base64,AAA"},
	})
	if err != nil {
		t.Fatalf("识图导入失败: %v", err)
	}
	if res.Model != "fake-vision" {
		t.Fatalf("识图应使用视觉模型，实际用了 %q", res.Model)
	}
	if res.TaskID == 0 {
		t.Fatal("应记录导入任务")
	}

	// 纯文本导入不受影响，仍走文本模型。
	textRes, err := fx.svc.Extract(ctx, fx.editor, fx.subject.ID, &schema.ExtractRequest{Content: "题目原文"})
	if err != nil {
		t.Fatalf("文本导入失败: %v", err)
	}
	if textRes.Model != "fake-model" {
		t.Fatalf("文本导入应使用文本模型，实际用了 %q", textRes.Model)
	}
}

// 文本 + 图片同时出现时，图片决定了用哪个模型——文本模型读不了图。
func TestExtractTextWithImagesPrefersVisionModel(t *testing.T) {
	fx := newImportFixture(t, realisticExtraction)
	ctx := context.Background()
	addVisionModel(t, fx, realisticExtraction)

	res, err := fx.svc.Extract(ctx, fx.editor, fx.subject.ID, &schema.ExtractRequest{
		Content: "试卷扫描件与文字",
		Images:  []string{"https://example.com/a.png"},
	})
	if err != nil {
		t.Fatalf("导入失败: %v", err)
	}
	if res.Model != "fake-vision" {
		t.Fatalf("带图时应使用视觉模型，实际用了 %q", res.Model)
	}
	// 审计记录要能看出这是一次图片导入。
	if res.TaskID == 0 {
		t.Fatal("应记录导入任务")
	}
}

// ---- 母子题 ----

// materialQuestionDrafts 造一份"1 道材料题 + 2 道小问"的草稿，顺序刻意打乱：
// 模型输出的小问不一定排在母题前面。
func materialQuestionDrafts() []importing.Draft {
	return []importing.Draft{
		{
			TempID: "q2", ParentTempID: "q1",
			QuestionType: model.QuestionTypeSingleChoice,
			StemMarkdown: "（1）下列说法正确的是（  ）",
			Options: []model.Option{
				{Label: "A", Content: importing.MarkdownToRichDoc("甲")},
				{Label: "B", Content: importing.MarkdownToRichDoc("乙")},
			},
			Answer: model.AnswerSpec{Kind: "single", Correct: []string{"A"}},
		},
		{
			TempID:       "q1",
			QuestionType: model.QuestionTypeEssay,
			StemMarkdown: "阅读材料并回答下列问题：给定一个二次函数……",
			Answer:       model.AnswerSpec{Kind: "reference", Reference: importing.MarkdownToRichDoc("见解析")},
		},
		{
			TempID: "q3", ParentTempID: "q1",
			QuestionType: model.QuestionTypeSingleChoice,
			StemMarkdown: "（2）该函数的顶点横坐标为（  ）",
			Options: []model.Option{
				{Label: "A", Content: importing.MarkdownToRichDoc("1")},
				{Label: "B", Content: importing.MarkdownToRichDoc("-1")},
			},
			Answer: model.AnswerSpec{Kind: "single", Correct: []string{"B"}},
		},
	}
}

// 核心回归：parent_temp_id 必须被应用，否则材料题会散成三道互不相干的大题。
func TestCommitAppliesParentTempID(t *testing.T) {
	fx := newImportFixture(t, realisticExtraction)
	ctx := context.Background()
	drafts := materialQuestionDrafts()

	res, err := fx.svc.Commit(ctx, fx.editor, fx.subject.ID, &schema.CommitRequest{Drafts: drafts})
	if err != nil {
		t.Fatalf("入库失败: %v", err)
	}
	if res.CreatedCount != 3 || res.FailedCount != 0 {
		t.Fatalf("应全部入库，实际成功 %d 失败 %d：%+v", res.CreatedCount, res.FailedCount, res.Failures)
	}

	byTemp := make(map[string]uint, len(drafts))
	for i := range drafts {
		byTemp[drafts[i].TempID] = res.CreatedIDs[i]
	}

	parent, err := fx.repos.Questions.Get(ctx, byTemp["q1"])
	if err != nil {
		t.Fatalf("读取母题失败: %v", err)
	}
	if parent.ParentID != nil {
		t.Fatalf("母题不应有父节点，实际 parent_id=%v", *parent.ParentID)
	}

	for _, tempID := range []string{"q2", "q3"} {
		child, err := fx.repos.Questions.Get(ctx, byTemp[tempID])
		if err != nil {
			t.Fatalf("读取小问 %s 失败: %v", tempID, err)
		}
		if child.ParentID == nil {
			t.Fatalf("小问 %s 没有挂到母题上（parent_temp_id 被丢弃了）", tempID)
		}
		if *child.ParentID != parent.ID {
			t.Fatalf("小问 %s 的母题 id = %d，期望 %d", tempID, *child.ParentID, parent.ID)
		}
		if child.SubjectID != parent.SubjectID {
			t.Fatalf("小问 %s 跨学科了", tempID)
		}
	}

	// 详情里应当能看到"含 2 个小问"。
	children, err := fx.repos.Questions.Children(ctx, parent.ID)
	if err != nil {
		t.Fatalf("读取子题失败: %v", err)
	}
	if len(children) != 2 {
		t.Fatalf("母题应带 2 个小问，实际 %d", len(children))
	}
}

// 母题不合格时，它的小问必须一起跳过——否则会留下挂在空父节点上的孤儿题。
func TestCommitSkipsChildrenWhenParentFails(t *testing.T) {
	fx := newImportFixture(t, realisticExtraction)
	ctx := context.Background()

	drafts := []importing.Draft{
		{
			TempID: "bad",
			// 解答题带选项 ⇒ domain 校验必然拒绝。
			QuestionType: model.QuestionTypeEssay,
			StemMarkdown: "母题（不合法）",
			Options:      []model.Option{{Label: "A", Content: importing.MarkdownToRichDoc("甲")}},
			Answer:       model.AnswerSpec{Kind: "reference", Reference: importing.MarkdownToRichDoc("略")},
		},
		{
			TempID: "child", ParentTempID: "bad",
			QuestionType: model.QuestionTypeTrueFalse,
			StemMarkdown: "小问",
			Answer:       model.AnswerSpec{Kind: "boolean", BoolAnswer: boolPtr(true)},
		},
		{
			// 独立题不受牵连。
			TempID:       "solo",
			QuestionType: model.QuestionTypeTrueFalse,
			StemMarkdown: "独立题",
			Answer:       model.AnswerSpec{Kind: "boolean", BoolAnswer: boolPtr(true)},
		},
	}

	res, err := fx.svc.Commit(ctx, fx.editor, fx.subject.ID, &schema.CommitRequest{Drafts: drafts})
	if err != nil {
		t.Fatalf("入库不应整体失败: %v", err)
	}
	if res.CreatedCount != 1 {
		t.Fatalf("只有独立题应入库，实际成功 %d：%+v", res.CreatedCount, res.Failures)
	}
	if res.FailedCount != 2 {
		t.Fatalf("母题与小问都应失败，实际 %d：%+v", res.FailedCount, res.Failures)
	}

	var childFailure *schema.CommitFailure
	for i := range res.Failures {
		if res.Failures[i].TempID == "child" {
			childFailure = &res.Failures[i]
		}
	}
	if childFailure == nil {
		t.Fatalf("应回报小问被跳过：%+v", res.Failures)
	}
	if !strings.Contains(childFailure.Error, "母题") {
		t.Fatalf("小问的失败原因应说明与母题有关，实际 %q", childFailure.Error)
	}

	// 库里不能留下 parent_id 指向不存在记录的孤儿题。
	list, total, err := fx.repos.Questions.List(ctx, model.QuestionFilter{SubjectID: fx.subject.ID}, "", 100, 0)
	if err != nil {
		t.Fatalf("查询题目失败: %v", err)
	}
	if total != 1 || len(list) != 1 {
		t.Fatalf("库里应只剩 1 道题，实际 %d", total)
	}
	if list[0].ParentID != nil {
		t.Fatal("不应留下指向空父节点的孤儿题")
	}
}

// 指向批次外母题的小问按独立题处理（normalize 阶段已附警告），
// 不能凭空造出一个父节点。
func TestCommitTreatsUnknownParentAsRoot(t *testing.T) {
	fx := newImportFixture(t, realisticExtraction)
	ctx := context.Background()

	drafts := []importing.Draft{{
		TempID: "x", ParentTempID: "not-in-batch",
		QuestionType: model.QuestionTypeTrueFalse,
		StemMarkdown: "孤儿小问",
		Answer:       model.AnswerSpec{Kind: "boolean", BoolAnswer: boolPtr(true)},
	}}
	// 前端会把草稿原样送回来，因此 normalize 清掉的 parent_temp_id 可能被用户又填回来；
	// 服务端必须自己兜住。
	res, err := fx.svc.Commit(ctx, fx.editor, fx.subject.ID, &schema.CommitRequest{Drafts: drafts})
	if err != nil {
		t.Fatalf("入库失败: %v", err)
	}
	if res.CreatedCount != 1 {
		t.Fatalf("应作为独立题入库，实际 %d：%+v", res.CreatedCount, res.Failures)
	}
	q, err := fx.repos.Questions.Get(ctx, res.CreatedIDs[0])
	if err != nil {
		t.Fatalf("读取题目失败: %v", err)
	}
	if q.ParentID != nil {
		t.Fatalf("母题不存在时应按独立题入库，实际 parent_id=%v", *q.ParentID)
	}
}

// 母题引用成环时必须整串拒绝，而不是死循环或静默挑一个当母题。
func TestCommitRejectsCyclicParentRefs(t *testing.T) {
	fx := newImportFixture(t, realisticExtraction)
	ctx := context.Background()

	drafts := []importing.Draft{
		{
			TempID: "a", ParentTempID: "b",
			QuestionType: model.QuestionTypeTrueFalse,
			StemMarkdown: "A", Answer: model.AnswerSpec{Kind: "boolean", BoolAnswer: boolPtr(true)},
		},
		{
			TempID: "b", ParentTempID: "a",
			QuestionType: model.QuestionTypeTrueFalse,
			StemMarkdown: "B", Answer: model.AnswerSpec{Kind: "boolean", BoolAnswer: boolPtr(true)},
		},
	}

	res, err := fx.svc.Commit(ctx, fx.editor, fx.subject.ID, &schema.CommitRequest{Drafts: drafts})
	if err != nil {
		t.Fatalf("入库不应整体失败: %v", err)
	}
	if res.CreatedCount != 0 || res.FailedCount != 2 {
		t.Fatalf("成环的两题都应失败，实际成功 %d 失败 %d", res.CreatedCount, res.FailedCount)
	}
	for _, f := range res.Failures {
		if !strings.Contains(f.Error, "成环") {
			t.Fatalf("失败原因应说明成环，实际 %q", f.Error)
		}
	}
}

// 响应里的 id 仍按请求顺序给出：前端按草稿下标回填界面。
func TestCommitKeepsResponseOrder(t *testing.T) {
	fx := newImportFixture(t, realisticExtraction)
	ctx := context.Background()
	drafts := materialQuestionDrafts()

	res, err := fx.svc.Commit(ctx, fx.editor, fx.subject.ID, &schema.CommitRequest{Drafts: drafts})
	if err != nil {
		t.Fatalf("入库失败: %v", err)
	}

	// 每道草稿题干里都带一个唯一标记，用它核对 id 与草稿一一对应。
	markers := []string{"下列说法正确", "阅读材料", "顶点横坐标"}
	for i, id := range res.CreatedIDs {
		q, err := fx.repos.Questions.Get(ctx, id)
		if err != nil {
			t.Fatalf("读取题目失败: %v", err)
		}
		if !strings.Contains(q.StemText, markers[i]) {
			t.Fatalf("第 %d 个 id 对应的不是第 %d 个草稿（题干 %q）", i, i, q.StemText)
		}
	}
}

// draftDepths 的纯函数行为（落库顺序的依据）。
func TestDraftDepths(t *testing.T) {
	drafts := materialQuestionDrafts()
	depths, parents := draftDepths(drafts)
	// drafts 顺序：q2(子) q1(母) q3(子)
	if depths[1] != 0 || parents[1] != -1 {
		t.Fatalf("母题应无父且深度 0，实际 depth=%d parent=%d", depths[1], parents[1])
	}
	for _, i := range []int{0, 2} {
		if depths[i] != 1 {
			t.Fatalf("小问 %d 的深度应为 1，实际 %d", i, depths[i])
		}
		if parents[i] != 1 {
			t.Fatalf("小问 %d 的母题下标应为 1，实际 %d", i, parents[i])
		}
	}

	// 成环：整串判为不可解析。
	cyc := []importing.Draft{{TempID: "a", ParentTempID: "b"}, {TempID: "b", ParentTempID: "a"}}
	cycDepths, cycParents := draftDepths(cyc)
	for i := range cyc {
		if cycDepths[i] < maxDraftDepth {
			t.Fatalf("成环的第 %d 题应被标记为不可解析，实际深度 %d", i, cycDepths[i])
		}
		_ = cycParents[i]
	}

	// 空白字符必须被忽略：模型输出带空格时不应把母子关系拆散。
	spaced := []importing.Draft{{TempID: " p1 "}, {TempID: " c1 ", ParentTempID: " p1 "}}
	spacedDepths, spacedParents := draftDepths(spaced)
	if spacedDepths[1] != 1 || spacedParents[1] != 0 {
		t.Fatalf("带空白的临时 id 应能配上，实际 depth=%d parent=%d", spacedDepths[1], spacedParents[1])
	}
}
