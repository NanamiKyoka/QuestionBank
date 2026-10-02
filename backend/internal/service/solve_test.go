package service

import (
	"context"
	"testing"

	"github.com/NanamiKyoka/questionmind/internal/importing"
	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/schema"
)

// 模型对一道单选题给出的答案与解析。
const solvedSingleChoice = `{
  "questions": [
    {
      "temp_id": "q1",
      "stem": "已知函数 f(x)=x^2，则 f(2)=（  ）",
      "answer": "B",
      "analysis": "代入 x=2 得 f(2)=4。"
    }
  ]
}`

// solveFixture 一道已在库里、答案待补充的单选题 + 一套可用的 AI 配置。
type solveFixture struct {
	repos   *Repos
	svc     *SolveService
	subject *model.Subject
	editor  *model.User
	detail  *schema.QuestionDetail
}

func newSolveFixture(t *testing.T, modelReply string, opts ...solveFixtureOption) *solveFixture {
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

	editor := &model.User{Base: model.Base{ID: 2}, Username: "editor", IsActive: true}
	editor.Memberships = []model.SubjectMember{
		{UserID: editor.ID, SubjectID: subject.ID, Role: model.SubjectRoleEditor},
	}

	aiSvc := newAIService(t, repos)
	provider, err := aiSvc.CreateProvider(ctx, admin, &schema.ProviderCreateRequest{
		Name: "Fake", InterfaceType: "openai", BaseURL: fakeExtractor(t, modelReply).URL,
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

	questionSvc := NewQuestionService(repos)
	fx := &solveFixture{
		repos:   repos,
		svc:     NewSolveService(repos, aiSvc, questionSvc),
		subject: subject,
		editor:  editor,
	}
	fx.detail = fx.createQuestion(t, opts...)
	return fx
}

type solveFixtureOption func(*schema.QuestionCreateRequest)

// withExistingAnswer 造一道**已经有答案**的题（用于验证默认不覆盖）。
func withExistingAnswer(answer model.AnswerSpec) solveFixtureOption {
	return func(req *schema.QuestionCreateRequest) { req.Answer = answer }
}

func (fx *solveFixture) createQuestion(t *testing.T, opts ...solveFixtureOption) *schema.QuestionDetail {
	t.Helper()
	req := &schema.QuestionCreateRequest{
		SubjectID:    fx.subject.ID,
		QuestionType: model.QuestionTypeSingleChoice,
		Stem:         importing.MarkdownToRichDoc("已知函数 $f(x)=x^2$，则 $f(2)=$（  ）"),
		Options: []schema.OptionInput{
			{Label: "A", Content: importing.MarkdownToRichDoc("2")},
			{Label: "B", Content: importing.MarkdownToRichDoc("4")},
			{Label: "C", Content: importing.MarkdownToRichDoc("8")},
		},
		// 「仅录题」录进来的题：答案待补充。
		Answer: model.AnswerSpec{Kind: model.AnswerKindPending},
	}
	for _, opt := range opts {
		opt(req)
	}
	detail, err := NewQuestionService(fx.repos).Create(context.Background(), fx.editor, req)
	if err != nil {
		t.Fatalf("建题失败: %v", err)
	}
	return detail
}

// 「仅录题」的后半段：为库里的一道待补答案的题补写答案。
func TestSolveFillsPendingAnswer(t *testing.T) {
	fx := newSolveFixture(t, solvedSingleChoice)
	ctx := context.Background()

	res, err := fx.svc.Solve(ctx, fx.editor, fx.detail.ID, &schema.SolveRequest{})
	if err != nil {
		t.Fatalf("解题失败: %v", err)
	}
	if res.Status != "solved" {
		t.Fatalf("应成功写入，实际 %s：%s", res.Status, res.Message)
	}
	if res.AnsweredText != "B" {
		t.Fatalf("答案摘要应为 B，实际 %q", res.AnsweredText)
	}
	if res.Question == nil || res.Question.Answer.Kind != "single" {
		t.Fatalf("返回的题目应带上写入后的答案，实际 %+v", res.Question)
	}

	// 落库的答案必须是选项 **id**，不是标签：试卷与导出都按 id 引用选项。
	q, err := fx.repos.Questions.Get(ctx, fx.detail.ID)
	if err != nil {
		t.Fatalf("读题失败: %v", err)
	}
	var answer model.AnswerSpec
	if err := q.Answer.Into(&answer); err != nil {
		t.Fatalf("解析答案失败: %v", err)
	}
	if answer.Kind != "single" || len(answer.Correct) != 1 {
		t.Fatalf("答案应为一个选项，实际 %+v", answer)
	}
	if answer.Correct[0] == "B" {
		t.Fatal("答案落库时应已解析为选项 id，而不是标签")
	}
	if answer.Correct[0] != optionIDOf(t, q, "B") {
		t.Fatalf("答案指向的选项不对：%s", answer.Correct[0])
	}
	// 解析也要写进去，否则「AI 解题」只给了一个字母，用户看不到过程。
	if q.Analysis.IsEmpty() {
		t.Fatal("解析应一并写入")
	}
}

// 已经有人工答案的题默认不动：AI 解题最常见的用途是补空，不是覆盖。
func TestSolveSkipsQuestionThatAlreadyHasAnswer(t *testing.T) {
	fx := newSolveFixture(t, solvedSingleChoice,
		withExistingAnswer(model.AnswerSpec{Kind: "single", Correct: []string{"A"}}))
	ctx := context.Background()

	res, err := fx.svc.Solve(ctx, fx.editor, fx.detail.ID, &schema.SolveRequest{})
	if err != nil {
		t.Fatalf("解题失败: %v", err)
	}
	if res.Status != "skipped" {
		t.Fatalf("已有答案时应跳过，实际 %s", res.Status)
	}

	q, err := fx.repos.Questions.Get(ctx, fx.detail.ID)
	if err != nil {
		t.Fatalf("读题失败: %v", err)
	}
	var answer model.AnswerSpec
	if err := q.Answer.Into(&answer); err != nil {
		t.Fatalf("解析答案失败: %v", err)
	}
	if len(answer.Correct) != 1 || answer.Correct[0] != optionIDOf(t, q, "A") {
		t.Fatalf("原有答案不应被改动，实际 %+v", answer)
	}
}

// 显式要求覆盖时才动它。
func TestSolveOverwritesWhenAsked(t *testing.T) {
	fx := newSolveFixture(t, solvedSingleChoice,
		withExistingAnswer(model.AnswerSpec{Kind: "single", Correct: []string{"A"}}))
	ctx := context.Background()

	res, err := fx.svc.Solve(ctx, fx.editor, fx.detail.ID, &schema.SolveRequest{Overwrite: true})
	if err != nil {
		t.Fatalf("解题失败: %v", err)
	}
	if res.Status != "solved" {
		t.Fatalf("要求覆盖时应写入，实际 %s：%s", res.Status, res.Message)
	}

	q, err := fx.repos.Questions.Get(ctx, fx.detail.ID)
	if err != nil {
		t.Fatalf("读题失败: %v", err)
	}
	var answer model.AnswerSpec
	if err := q.Answer.Into(&answer); err != nil {
		t.Fatalf("解析答案失败: %v", err)
	}
	if answer.Correct[0] != optionIDOf(t, q, "B") {
		t.Fatalf("覆盖后答案应为 B 对应的选项，实际 %+v", answer)
	}
}

// 模型给出的答案过不了题型校验时，报告为"这一道失败"，而不是让整批中断。
//
// 这里让模型答出一个选项表里根本不存在的字母（模型幻觉出"E 选项"是常见事）：
// 解析会得到"空答案"，而单选题的空答案过不了 domain 的校验。
func TestSolveReportsUnusableAnswerAsFailure(t *testing.T) {
	const bogusAnswer = `{"questions":[{"stem":"x","answer":"E","analysis":"猜的"}]}`
	fx := newSolveFixture(t, bogusAnswer)
	ctx := context.Background()

	res, err := fx.svc.Solve(ctx, fx.editor, fx.detail.ID, &schema.SolveRequest{})
	if err != nil {
		t.Fatalf("校验失败不该是硬错误: %v", err)
	}
	if res.Status != "failed" {
		t.Fatalf("应报告失败，实际 %s（%s）", res.Status, res.Message)
	}
	if res.Message == "" {
		t.Fatal("失败必须带上原因，否则界面上只剩一个红叉")
	}

	// 失败时绝不能留下半截数据。
	q, err := fx.repos.Questions.Get(ctx, fx.detail.ID)
	if err != nil {
		t.Fatalf("读题失败: %v", err)
	}
	var answer model.AnswerSpec
	if err := q.Answer.Into(&answer); err != nil {
		t.Fatalf("解析答案失败: %v", err)
	}
	if answer.Kind != model.AnswerKindPending {
		t.Fatalf("写入失败后答案应保持待补充，实际 %+v", answer)
	}
}

// 没有权限的人不能借这个接口改题。
func TestSolveRequiresEditPermission(t *testing.T) {
	fx := newSolveFixture(t, solvedSingleChoice)
	outsider := &model.User{Base: model.Base{ID: 9}, Username: "outsider", IsActive: true}

	if _, err := fx.svc.Solve(context.Background(), outsider, fx.detail.ID, &schema.SolveRequest{}); err == nil {
		t.Fatal("无权限用户应被拒绝")
	}
}

// optionIDOf 取某个标签对应的选项 id。
func optionIDOf(t *testing.T, q *model.Question, label string) string {
	t.Helper()
	var options []model.Option
	if err := q.Options.Into(&options); err != nil {
		t.Fatalf("解析选项失败: %v", err)
	}
	for i := range options {
		if options[i].Label == label {
			return options[i].ID
		}
	}
	t.Fatalf("题目里没有选项 %s", label)
	return ""
}
