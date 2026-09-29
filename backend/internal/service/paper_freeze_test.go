package service

import (
	"context"
	"testing"

	"github.com/NanamiKyoka/questionmind/internal/domain"
	"github.com/NanamiKyoka/questionmind/internal/importing"
	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/schema"
)

// 试卷是稿件，不是题库的一个视图。
//
// 这组用例锁死"题目内容在加入卷子时冻结、之后不被题库改动带偏"这条语义。
// 在冻结之前，试卷的每个题目块只存 question_id，每次读取与导出都回题目表
// 取当前值——于是改一道题会静默改掉所有引用它的卷子，删一道题会在每张卷子上
// 留下一个洞。这对"上周发给学生的卷子"是不可接受的。
type paperFreezeFixture struct {
	repos   *Repos
	subject *model.Subject
	author  *model.User
	papers  *PaperService
	qs      *QuestionService
}

func newPaperFreezeFixture(t *testing.T) *paperFreezeFixture {
	t.Helper()
	repos := newTestRepos(t)
	ctx := context.Background()

	subject := &model.Subject{Name: "数学", ReviewThreshold: 1, CreatedBy: 1}
	if err := repos.Subjects.Create(ctx, subject); err != nil {
		t.Fatalf("建学科失败: %v", err)
	}
	author := &model.User{Base: model.Base{ID: 2}, Username: "author", IsActive: true}
	author.Memberships = []model.SubjectMember{
		{UserID: author.ID, SubjectID: subject.ID, Role: model.SubjectRoleEditor},
	}
	return &paperFreezeFixture{
		repos:   repos,
		subject: subject,
		author:  author,
		papers:  NewPaperService(repos),
		qs:      NewQuestionService(repos),
	}
}

func (fx *paperFreezeFixture) newQuestion(t *testing.T, stem string) *schema.QuestionDetail {
	t.Helper()
	det, err := fx.qs.Create(context.Background(), fx.author, &schema.QuestionCreateRequest{
		SubjectID:    fx.subject.ID,
		QuestionType: model.QuestionTypeTrueFalse,
		Stem:         importing.MarkdownToRichDoc(stem),
		Answer:       model.AnswerSpec{Kind: "boolean", BoolAnswer: boolPtr(true)},
	})
	if err != nil {
		t.Fatalf("建题失败: %v", err)
	}
	return det
}

func (fx *paperFreezeFixture) newPaper(t *testing.T) *schema.PaperDetail {
	t.Helper()
	det, err := fx.papers.CreatePaper(context.Background(), fx.author, fx.subject.ID, &schema.PaperCreateRequest{
		Title: "试卷", Scope: model.PaperScopePersonal,
	})
	if err != nil {
		t.Fatalf("建卷失败: %v", err)
	}
	return det
}

// 往卷子里放一道题，返回保存后的试卷。
func (fx *paperFreezeFixture) addQuestion(t *testing.T, paper *schema.PaperDetail, questionID uint, nodeID string) *schema.PaperDetail {
	t.Helper()
	if _, err := fx.papers.ReplaceNodes(context.Background(), fx.author, paper.ID, &schema.ReplaceNodesRequest{
		ExpectedRevision: paper.Revision,
		Nodes: []domain.NodeInput{{
			ID:       nodeID,
			NodeKind: model.NodeKindBlock,
			NodeType: model.NodeTypeQuestion,
			Content:  mustJSON(domain.QuestionRef{QuestionID: questionID, ContentRevision: 1}),
		}},
	}); err != nil {
		t.Fatalf("写入题目块失败: %v", err)
	}
	detail, err := fx.papers.GetPaper(context.Background(), fx.author, paper.ID)
	if err != nil {
		t.Fatalf("读取试卷失败: %v", err)
	}
	return detail
}

// 题库改字之后，卷子里的内容必须纹丝不动。
// plainOf 从富文本里取纯文本，用于断言"卷面上到底是哪一句"。
func plainOf(raw model.JSON) string {
	if raw.IsEmpty() {
		return ""
	}
	var doc domain.RichDoc
	if err := raw.Into(&doc); err != nil {
		return ""
	}
	return domain.PlainText(doc)
}

func TestPaperKeepsFrozenContentAfterQuestionIsEdited(t *testing.T) {
	fx := newPaperFreezeFixture(t)
	ctx := context.Background()

	q := fx.newQuestion(t, "原始题干")
	paper := fx.addQuestion(t, fx.newPaper(t), q.ID, "n-1")

	before, ok := paper.Questions[q.ID]
	if !ok {
		t.Fatal("题目块应有内容投影")
	}
	if got := plainOf(before.Stem); got != "原始题干" {
		t.Fatalf("冻结内容不对，got=%q", got)
	}

	// 改题干。
	if _, err := fx.qs.Update(ctx, fx.author, q.ID, &schema.QuestionUpdateRequest{
		Stem: importing.MarkdownToRichDoc("改过的题干"),
	}); err != nil {
		t.Fatalf("改题失败: %v", err)
	}

	after, err := fx.papers.GetPaper(ctx, fx.author, paper.ID)
	if err != nil {
		t.Fatalf("读取试卷失败: %v", err)
	}
	proj, ok := after.Questions[q.ID]
	if !ok {
		t.Fatal("题目块应仍有内容投影")
	}
	if got := plainOf(proj.Stem); got != "原始题干" {
		t.Fatalf("题库改动不应带进已排好的卷子，got=%q", got)
	}
	// 但要告诉用户"可以同步最新内容"。
	if !proj.Stale {
		t.Fatal("题库改过之后应标记为可同步")
	}
}

// 题目被删除之后，卷子仍然要能读——稿件不该因为题库清理而散架。
func TestPaperKeepsFrozenContentAfterQuestionIsDeleted(t *testing.T) {
	fx := newPaperFreezeFixture(t)
	ctx := context.Background()

	q := fx.newQuestion(t, "会被删掉的题")
	paper := fx.addQuestion(t, fx.newPaper(t), q.ID, "n-1")

	if err := fx.repos.Questions.SoftDelete(ctx, []uint{q.ID}); err != nil {
		t.Fatalf("软删除失败: %v", err)
	}

	after, err := fx.papers.GetPaper(ctx, fx.author, paper.ID)
	if err != nil {
		t.Fatalf("题目删除后读取试卷失败: %v", err)
	}
	proj, ok := after.Questions[q.ID]
	if !ok {
		t.Fatal("题目删除后，卷子仍应保有冻结内容")
	}
	if got := plainOf(proj.Stem); got != "会被删掉的题" {
		t.Fatalf("冻结内容应原样保留，got=%q", got)
	}
	if !proj.Stale {
		t.Fatal("题目已删除，应标记为过期")
	}
}

// 自动保存是整棵树覆盖式的，每次都重新冻结就等于把题库改动悄悄带进卷子。
func TestAutosaveDoesNotRefreezeExistingQuestionNode(t *testing.T) {
	fx := newPaperFreezeFixture(t)
	ctx := context.Background()

	q := fx.newQuestion(t, "初始题干")
	paper := fx.addQuestion(t, fx.newPaper(t), q.ID, "n-1")

	if _, err := fx.qs.Update(ctx, fx.author, q.ID, &schema.QuestionUpdateRequest{
		Stem: importing.MarkdownToRichDoc("题库里改了"),
	}); err != nil {
		t.Fatalf("改题失败: %v", err)
	}

	// 用**同一个节点 id** 再保存一次（模拟自动保存）。
	detail := fx.addQuestion(t, paper, q.ID, "n-1")
	if got := plainOf(detail.Questions[q.ID].Stem); got != "初始题干" {
		t.Fatalf("自动保存不应重新冻结，got=%q", got)
	}
}

// 显式同步才应该采纳题库的最新内容。
func TestSyncQuestionNodesAdoptsLatestContent(t *testing.T) {
	fx := newPaperFreezeFixture(t)
	ctx := context.Background()

	q := fx.newQuestion(t, "初始题干")
	paper := fx.addQuestion(t, fx.newPaper(t), q.ID, "n-1")

	if _, err := fx.qs.Update(ctx, fx.author, q.ID, &schema.QuestionUpdateRequest{
		Stem: importing.MarkdownToRichDoc("题库里改了"),
	}); err != nil {
		t.Fatalf("改题失败: %v", err)
	}

	res, err := fx.papers.SyncQuestionNodes(ctx, fx.author, paper.ID, nil)
	if err != nil {
		t.Fatalf("同步失败: %v", err)
	}
	if len(res.Synced) != 1 {
		t.Fatalf("应同步 1 道题，实际 %d", len(res.Synced))
	}

	after, err := fx.papers.GetPaper(ctx, fx.author, paper.ID)
	if err != nil {
		t.Fatalf("读取试卷失败: %v", err)
	}
	if got := plainOf(after.Questions[q.ID].Stem); got != "题库里改了" {
		t.Fatalf("显式同步后应采用最新内容，got=%q", got)
	}
	if after.Questions[q.ID].Stale {
		t.Fatal("同步后不应再标记为过期")
	}
}
