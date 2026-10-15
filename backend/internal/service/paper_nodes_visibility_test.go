package service

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/NanamiKyoka/questionmind/internal/domain"
	"github.com/NanamiKyoka/questionmind/internal/importing"
	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
	"github.com/NanamiKyoka/questionmind/internal/schema"
)

// paperVisibilityFixture 学科 + 三种角色 + 可共享/私有的题目 + 两种空间的试卷。
type paperVisibilityFixture struct {
	repos   *Repos
	subject *model.Subject
	editor  *model.User // 录入者，私有题的创建者
	peer    *model.User // 同学科的另一个编辑者，模拟"别人"
	papers  *PaperService
	qs      *QuestionService
}

func newPaperVisibilityFixture(t *testing.T) *paperVisibilityFixture {
	t.Helper()
	repos := newTestRepos(t)
	ctx := context.Background()

	subject := &model.Subject{Name: "数学", ReviewThreshold: 1, CreatedBy: 1}
	if err := repos.Subjects.Create(ctx, subject); err != nil {
		t.Fatalf("建学科失败: %v", err)
	}
	editor := &model.User{Base: model.Base{ID: 2}, Username: "editor", IsActive: true}
	editor.Memberships = []model.SubjectMember{
		{UserID: editor.ID, SubjectID: subject.ID, Role: model.SubjectRoleEditor},
	}
	peer := &model.User{Base: model.Base{ID: 3}, Username: "peer", IsActive: true}
	peer.Memberships = []model.SubjectMember{
		{UserID: peer.ID, SubjectID: subject.ID, Role: model.SubjectRoleEditor},
	}
	return &paperVisibilityFixture{
		repos:   repos,
		subject: subject,
		editor:  editor,
		peer:    peer,
		papers:  NewPaperService(repos),
		qs:      NewQuestionService(repos),
	}
}

// newQuestion 造一道指定可见性的题目。
func (fx *paperVisibilityFixture) newQuestion(t *testing.T, author *model.User, visibility model.Visibility, stem string) *schema.QuestionDetail {
	t.Helper()
	det, err := fx.qs.Create(context.Background(), author, &schema.QuestionCreateRequest{
		SubjectID:    fx.subject.ID,
		QuestionType: model.QuestionTypeTrueFalse,
		Stem:         importing.MarkdownToRichDoc(stem),
		Answer:       model.AnswerSpec{Kind: "boolean", BoolAnswer: boolPtr(true)},
		Visibility:   visibility,
	})
	if err != nil {
		t.Fatalf("建题失败: %v", err)
	}
	return det
}

func (fx *paperVisibilityFixture) newPaper(t *testing.T, scope model.PaperScope) *schema.PaperDetail {
	t.Helper()
	det, err := fx.papers.CreatePaper(context.Background(), fx.editor, fx.subject.ID, &schema.PaperCreateRequest{
		Title: "试卷", Scope: scope,
	})
	if err != nil {
		t.Fatalf("建卷失败: %v", err)
	}
	return det
}

// nodeSeq 生成全局唯一的节点 id：paper_nodes.id 是 UUID 主键，
// 两张不同的卷子不能复用同一批 id。
var nodeSeq atomic.Uint64

// nodesOf 生成一个只含题目块的节点树。
func nodesOf(questionIDs ...uint) []domain.NodeInput {
	nodes := make([]domain.NodeInput, 0, len(questionIDs))
	for i, id := range questionIDs {
		nodes = append(nodes, domain.NodeInput{
			ID:       fmt.Sprintf("n%d-%d", nodeSeq.Add(1), i),
			NodeKind: model.NodeKindBlock,
			NodeType: model.NodeTypeQuestion,
			Content:  mustJSON(domain.QuestionRef{QuestionID: id, ContentRevision: 1}),
		})
	}
	return nodes
}

// 私有题一旦进入共享试卷的节点树，projectQuestions 就不再做可见性过滤，
// 全学科成员都能读到它的完整题干与答案——这是实实在在的数据泄露。
func TestReplaceNodesRejectsPrivateQuestionInSharedPaper(t *testing.T) {
	fx := newPaperVisibilityFixture(t)
	ctx := context.Background()

	public := fx.newQuestion(t, fx.editor, model.VisibilityPublic, "公开题")
	private := fx.newQuestion(t, fx.editor, model.VisibilityPrivate, "私有题")
	paper := fx.newPaper(t, model.PaperScopeShared)

	// 公开题放进共享卷：允许。
	if _, err := fx.papers.ReplaceNodes(ctx, fx.editor, paper.ID, &schema.ReplaceNodesRequest{
		ExpectedRevision: paper.Revision,
		Nodes:            nodesOf(public.ID),
	}); err != nil {
		t.Fatalf("公开题应能加入共享试卷: %v", err)
	}

	// 换成私有题：拒绝，且必须给出可理解的提示。
	paper, err := fx.papers.GetPaper(ctx, fx.editor, paper.ID)
	if err != nil {
		t.Fatalf("读取试卷失败: %v", err)
	}
	err = fx.replaceNodesExpectError(t, fx.editor, paper, private.ID)
	if !apperr.Is(err, apperr.CodeForbidden) {
		t.Fatalf("私有题加入共享卷应报 403，实际: %v", err)
	}
	if !strings.Contains(err.Error(), "私有") {
		t.Fatalf("错误信息应点明是私有题的问题，实际: %v", err)
	}

	// 被拒绝后不能留下任何痕迹。
	after, err := fx.papers.GetPaper(ctx, fx.editor, paper.ID)
	if err != nil {
		t.Fatalf("读取试卷失败: %v", err)
	}
	if len(after.Questions) != 1 {
		t.Fatalf("失败的保存不应改动试卷，实际包含 %d 道题", len(after.Questions))
	}
	if _, leaked := after.Questions[private.ID]; leaked {
		t.Fatal("私有题不应出现在共享试卷里")
	}
}

// 连超管也不行：他有权"看"，不等于有权替别人把私有题公开出去。
func TestReplaceNodesRejectsPrivateQuestionEvenForSuperuser(t *testing.T) {
	fx := newPaperVisibilityFixture(t)

	private := fx.newQuestion(t, fx.editor, model.VisibilityPrivate, "私有题")
	// 学科负责人有 view_private_any，本就能读到这道题。
	manager := &model.User{Base: model.Base{ID: 4}, Username: "manager", IsActive: true}
	manager.Memberships = []model.SubjectMember{
		{UserID: manager.ID, SubjectID: fx.subject.ID, Role: model.SubjectRoleManager},
	}
	paper := fx.newPaper(t, model.PaperScopeShared)

	err := fx.replaceNodesExpectError(t, manager, paper, private.ID)
	if !apperr.Is(err, apperr.CodeForbidden) {
		t.Fatalf("共享试卷仍应拒绝私有题，实际: %v", err)
	}
}

// 个人试卷只有本人读，放自己的私有题没有问题——否则私有题就永远无法组卷了。
func TestReplaceNodesAllowsOwnPrivateQuestionInPersonalPaper(t *testing.T) {
	fx := newPaperVisibilityFixture(t)
	ctx := context.Background()

	private := fx.newQuestion(t, fx.editor, model.VisibilityPrivate, "我的私有题")
	paper := fx.newPaper(t, model.PaperScopePersonal)

	if _, err := fx.papers.ReplaceNodes(ctx, fx.editor, paper.ID, &schema.ReplaceNodesRequest{
		ExpectedRevision: paper.Revision,
		Nodes:            nodesOf(private.ID),
	}); err != nil {
		t.Fatalf("自己的私有题应能加入个人试卷: %v", err)
	}
}

// 但别人的私有题即使放进个人卷也不该放行——那是绕过题库的可见性规则去抄题。
func TestReplaceNodesRejectsOtherUsersPrivateQuestionInPersonalPaper(t *testing.T) {
	fx := newPaperVisibilityFixture(t)

	private := fx.newQuestion(t, fx.editor, model.VisibilityPrivate, "editor 的私有题")
	paper := fx.newPaper(t, model.PaperScopePersonal)

	err := fx.replaceNodesExpectError(t, fx.peer, paper, private.ID)
	if err == nil {
		t.Fatal("引用他人的私有题应被拒绝")
	}
	if !apperr.Is(err, apperr.CodeForbidden) {
		t.Fatalf("应报 403，实际: %v", err)
	}
}

// 私有题先改成公开，再入卷即可——说明拦截的是"往共享空间泄露"，
// 而不是禁止私有题参与组卷。
func TestReplaceNodesAllowsPrivateQuestionAfterMadePublic(t *testing.T) {
	fx := newPaperVisibilityFixture(t)
	ctx := context.Background()

	q := fx.newQuestion(t, fx.editor, model.VisibilityPrivate, "先私有后公开")
	if _, err := fx.qs.Update(ctx, fx.editor, q.ID, &schema.QuestionUpdateRequest{
		Visibility: visibilityPtr(model.VisibilityPublic),
	}); err != nil {
		t.Fatalf("改为公开失败: %v", err)
	}
	paper := fx.newPaper(t, model.PaperScopeShared)

	if _, err := fx.papers.ReplaceNodes(ctx, fx.editor, paper.ID, &schema.ReplaceNodesRequest{
		ExpectedRevision: paper.Revision,
		Nodes:            nodesOf(q.ID),
	}); err != nil {
		t.Fatalf("已公开的题目应能加入共享试卷: %v", err)
	}
}

func visibilityPtr(v model.Visibility) *model.Visibility { return &v }

// ---- 个人空间的越权读取（IDOR）----

// 个人空间的卷子只有本人能读。此前只有 GetPaper 做了归属校验，
// 版本列表、版本快照、操作流水、版本导出全都只做了学科级校验——
// 同学科的任何成员猜到试卷 id 就能读走别人的整份卷子。
func TestPersonalPaperReadPathsAreOwnerOnly(t *testing.T) {
	fx := newPaperVisibilityFixture(t)
	ctx := context.Background()

	// 一份带题目的个人卷，并定稿出一个版本（快照里含完整题目内容）。
	q := fx.newQuestion(t, fx.editor, model.VisibilityPublic, "只给本人看的卷子")
	paper := fx.newPaper(t, model.PaperScopePersonal)
	if _, err := fx.papers.ReplaceNodes(ctx, fx.editor, paper.ID, &schema.ReplaceNodesRequest{
		ExpectedRevision: paper.Revision,
		Nodes:            nodesOf(q.ID),
	}); err != nil {
		t.Fatalf("保存节点失败: %v", err)
	}
	version, err := fx.papers.FinalizeVersion(ctx, fx.editor, paper.ID, &schema.FinalizeRequest{Note: "v1"})
	if err != nil {
		t.Fatalf("定稿失败: %v", err)
	}

	exporter := NewExportService(fx.repos, nil)

	// 同学科的另一个成员：四个读路径都必须 404（而不是 403——403 等于确认它存在）。
	if _, err := fx.papers.ListVersions(ctx, fx.peer, paper.ID); !apperr.Is(err, apperr.CodeNotFound) {
		t.Fatalf("他人读取版本列表应报 404，实际: %v", err)
	}
	if _, err := fx.papers.GetVersion(ctx, fx.peer, paper.ID, version.VersionNo); !apperr.Is(err, apperr.CodeNotFound) {
		t.Fatalf("他人读取版本快照应报 404，实际: %v", err)
	}
	if _, err := fx.papers.ListEvents(ctx, fx.peer, paper.ID, 30, 0); !apperr.Is(err, apperr.CodeNotFound) {
		t.Fatalf("他人读取操作流水应报 404，实际: %v", err)
	}
	if _, err := exporter.ExportVersion(ctx, fx.peer, paper.ID, version.VersionNo, FormatLaTeX, ContentAll); !apperr.Is(err, apperr.CodeNotFound) {
		t.Fatalf("他人导出版本应报 404，实际: %v", err)
	}
	// 试卷详情本身同样不给看（GetPaper 早就有这道校验，顺带回归）。
	if _, err := fx.papers.GetPaper(ctx, fx.peer, paper.ID); !apperr.Is(err, apperr.CodeNotFound) {
		t.Fatalf("他人读取试卷详情应报 404，实际: %v", err)
	}

	// 本人：四个路径都通。
	versions, err := fx.papers.ListVersions(ctx, fx.editor, paper.ID)
	if err != nil {
		t.Fatalf("本人读取版本列表失败: %v", err)
	}
	if len(versions) != 1 || versions[0].VersionNo != version.VersionNo {
		t.Fatalf("版本列表不对: %+v", versions)
	}
	detail, err := fx.papers.GetVersion(ctx, fx.editor, paper.ID, version.VersionNo)
	if err != nil {
		t.Fatalf("本人读取版本快照失败: %v", err)
	}
	if detail.Snapshot.IsEmpty() {
		t.Fatal("版本快照不应为空")
	}
	events, err := fx.papers.ListEvents(ctx, fx.editor, paper.ID, 30, 0)
	if err != nil {
		t.Fatalf("本人读取操作流水失败: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("操作流水不应为空（创建、定稿都会留痕）")
	}
	if _, err := exporter.ExportVersion(ctx, fx.editor, paper.ID, version.VersionNo, FormatLaTeX, ContentAll); err != nil {
		t.Fatalf("本人导出版本失败: %v", err)
	}

	// 共享卷的版本列表对成员开放——这道校验不能误伤共享空间。
	shared := fx.newPaper(t, model.PaperScopeShared)
	if _, err := fx.papers.ReplaceNodes(ctx, fx.editor, shared.ID, &schema.ReplaceNodesRequest{
		ExpectedRevision: shared.Revision,
		Nodes:            nodesOf(q.ID),
	}); err != nil {
		t.Fatalf("共享卷保存节点失败: %v", err)
	}
	if _, err := fx.papers.FinalizeVersion(ctx, fx.editor, shared.ID, &schema.FinalizeRequest{}); err != nil {
		t.Fatalf("共享卷定稿失败: %v", err)
	}
	if _, err := fx.papers.ListVersions(ctx, fx.peer, shared.ID); err != nil {
		t.Fatalf("成员读取共享卷版本列表应成功: %v", err)
	}
}

// 写入口的拦截只管"新增"。存量数据有两条绕过路径：
// 题还是公开时排进了共享卷、事后才改成私有；或者卷子建于修复之前。
// 这两种情况都必须由**读**侧兜住，否则"私有题不进共享卷"形同虚设——
// 只要数据早于规则存在，私有内容就能一直被读到。
func TestPaperProjectionHidesPrivateQuestionInsertedBeforeTheGuard(t *testing.T) {
	fx := newPaperVisibilityFixture(t)
	ctx := context.Background()

	// 先以公开身份把题排进共享卷（这在修复前是完全正常的操作）。
	q := fx.newQuestion(t, fx.editor, model.VisibilityPublic, "后来变私有的题")
	paper := fx.newPaper(t, model.PaperScopeShared)
	if _, err := fx.papers.ReplaceNodes(ctx, fx.editor, paper.ID, &schema.ReplaceNodesRequest{
		ExpectedRevision: paper.Revision,
		Nodes:            nodesOf(q.ID),
	}); err != nil {
		t.Fatalf("公开题加入共享卷本应成功: %v", err)
	}

	// 确认此刻确实可见，避免后面对照失效。
	if _, err := fx.papers.GetPaper(ctx, fx.peer, paper.ID); err != nil {
		t.Fatalf("同学科成员读取共享卷失败: %v", err)
	}

	// 题主把题改成私有。卷子内容没有跟着变——题目块还引用着它。
	if _, err := fx.qs.Update(ctx, fx.editor, q.ID, &schema.QuestionUpdateRequest{
		Visibility: &[]model.Visibility{model.VisibilityPrivate}[0],
	}); err != nil {
		t.Fatalf("改为私有失败: %v", err)
	}

	// 题主自己当然还能看到。
	own, err := fx.papers.GetPaper(ctx, fx.editor, paper.ID)
	if err != nil {
		t.Fatalf("题主读取失败: %v", err)
	}
	if _, ok := own.Questions[q.ID]; !ok {
		t.Fatal("题主应仍能看到自己的私有题")
	}

	// 别人必须看不到——节点树里可以留着这道题，但内容不能投影出去。
	peer, err := fx.papers.GetPaper(ctx, fx.peer, paper.ID)
	if err != nil {
		t.Fatalf("同学科成员读取共享卷失败: %v", err)
	}
	if _, leaked := peer.Questions[q.ID]; leaked {
		t.Fatal("已变私有的题目内容不应投影给其他成员")
	}
}

// replaceNodesExpectError 包一层断言：失败时必须保持试卷原样。
func (fx *paperVisibilityFixture) replaceNodesExpectError(t *testing.T, user *model.User, paper *schema.PaperDetail, questionID uint) error {
	t.Helper()
	_, err := fx.papers.ReplaceNodes(context.Background(), user, paper.ID, &schema.ReplaceNodesRequest{
		ExpectedRevision: paper.Revision,
		Nodes:            nodesOf(questionID),
	})
	if err == nil {
		t.Fatal("期望被拒绝，但保存成功了")
	}
	return err
}
