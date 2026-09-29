package service

import (
	"context"
	"testing"

	"github.com/NanamiKyoka/questionmind/internal/importing"
	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
	"github.com/NanamiKyoka/questionmind/internal/schema"
)

// reviewFixture 一个"阈值=2"（要求两人复核）的学科，以及录入者与审核者。
type reviewFixture struct {
	repos   *Repos
	svc     *QuestionService
	subject *model.Subject
	editor  *model.User
	manager *model.User
}

func newReviewFixture(t *testing.T, threshold int) *reviewFixture {
	t.Helper()
	repos := newTestRepos(t)
	ctx := context.Background()

	subject := &model.Subject{Name: "数学", ReviewThreshold: threshold, CreatedBy: 1}
	if err := repos.Subjects.Create(ctx, subject); err != nil {
		t.Fatalf("建学科失败: %v", err)
	}

	editor := &model.User{Base: model.Base{ID: 2}, Username: "editor", IsActive: true}
	editor.Memberships = []model.SubjectMember{
		{UserID: editor.ID, SubjectID: subject.ID, Role: model.SubjectRoleEditor},
	}
	manager := &model.User{Base: model.Base{ID: 3}, Username: "manager", IsActive: true}
	manager.Memberships = []model.SubjectMember{
		{UserID: manager.ID, SubjectID: subject.ID, Role: model.SubjectRoleManager},
	}
	return &reviewFixture{
		repos:   repos,
		svc:     NewQuestionService(repos),
		subject: subject,
		editor:  editor,
		manager: manager,
	}
}

// newDraftQuestion 造一道待审的草稿题（editor 录入 ⇒ 草稿状态）。
func (fx *reviewFixture) newDraftQuestion(t *testing.T) *schema.QuestionDetail {
	t.Helper()
	detail, err := fx.svc.Create(context.Background(), fx.editor, &schema.QuestionCreateRequest{
		SubjectID:    fx.subject.ID,
		QuestionType: model.QuestionTypeSingleChoice,
		Stem:         importing.MarkdownToRichDoc("已知 f(x)=x^2，则 f(2)=（  ）"),
		Options: []schema.OptionInput{
			{Label: "A", Content: importing.MarkdownToRichDoc("2")},
			{Label: "B", Content: importing.MarkdownToRichDoc("4")},
		},
		Answer: model.AnswerSpec{Kind: "single", Correct: []string{"B"}},
	})
	if err != nil {
		t.Fatalf("建题失败: %v", err)
	}
	return detail
}

// 核心回归：驳回必须清零审核次数。
//
// 阈值=2 时，清零与否的区别是致命的：
// 不清零 ⇒ 通过1次 → 驳回 → 再通过1次 = 2 ≥ 2 ⇒ 直接发布，
// 那次驳回被完全遗忘，双人复核实质失效。
func TestReviewRejectResetsReviewCount(t *testing.T) {
	fx := newReviewFixture(t, 2)
	ctx := context.Background()
	q := fx.newDraftQuestion(t)

	if _, err := fx.svc.Submit(ctx, fx.editor, q.ID); err != nil {
		t.Fatalf("提交审核失败: %v", err)
	}
	// 第一次通过：还没到阈值，留在待审队列。
	det, err := fx.svc.Review(ctx, fx.manager, q.ID, &schema.ReviewRequest{Action: "approve"})
	if err != nil {
		t.Fatalf("第一次通过失败: %v", err)
	}
	if det.Status != model.QuestionStatusPending {
		t.Fatalf("阈值未达成时应留在待审，实际 %s", det.Status)
	}
	if det.ReviewCount != 1 {
		t.Fatalf("第一次通过后审核次数应为 1，实际 %d", det.ReviewCount)
	}

	// 驳回：状态退回草稿，审核次数清零。
	det, err = fx.svc.Review(ctx, fx.manager, q.ID, &schema.ReviewRequest{
		Action:  "reject",
		Comment: "选项 B 有误",
	})
	if err != nil {
		t.Fatalf("驳回失败: %v", err)
	}
	if det.Status != model.QuestionStatusDraft {
		t.Fatalf("驳回后应退回草稿，实际 %s", det.Status)
	}
	if det.ReviewCount != 0 {
		t.Fatalf("驳回后审核次数必须清零，实际 %d（驳回前的通过被当成了对当前稿子的背书）", det.ReviewCount)
	}

	// 清零的是计数，审核日志必须完整保留——它是"这道题被谁打回过"的唯一证据。
	logs, err := fx.svc.ReviewLogs(ctx, fx.manager, q.ID, 50)
	if err != nil {
		t.Fatalf("读取审核日志失败: %v", err)
	}
	if len(logs) != 2 {
		t.Fatalf("审核日志应有 2 条（1 次通过 + 1 次驳回），实际 %d", len(logs))
	}
	var rejected bool
	for _, l := range logs {
		if l.Action == "reject" {
			rejected = true
			if l.ToStatus != model.QuestionStatusDraft || l.Comment != "选项 B 有误" {
				t.Fatalf("驳回日志内容不对: %+v", l)
			}
		}
	}
	if !rejected {
		t.Fatal("审核日志里应保留驳回记录")
	}

	// 重新提交并再通过一次：计数从头开始，仍未达到阈值，不得发布。
	if _, err := fx.svc.Submit(ctx, fx.editor, q.ID); err != nil {
		t.Fatalf("重新提交失败: %v", err)
	}
	det, err = fx.svc.Review(ctx, fx.manager, q.ID, &schema.ReviewRequest{Action: "approve"})
	if err != nil {
		t.Fatalf("重新提交后的第一次通过失败: %v", err)
	}
	if det.Status != model.QuestionStatusPending {
		t.Fatalf("被驳回过的题重新通过 1 次后仍应待审，实际 %s（阈值被一次驳回架空了）", det.Status)
	}
	if det.ReviewCount != 1 {
		t.Fatalf("重新计数后审核次数应为 1，实际 %d", det.ReviewCount)
	}

	// 第二次通过才发布。
	det, err = fx.svc.Review(ctx, fx.manager, q.ID, &schema.ReviewRequest{Action: "approve"})
	if err != nil {
		t.Fatalf("第二次通过失败: %v", err)
	}
	if det.Status != model.QuestionStatusPublished || det.ReviewCount != 2 {
		t.Fatalf("达到阈值后应发布，状态 %s 次数 %d", det.Status, det.ReviewCount)
	}
}

// 阈值=1 的常规路径不受影响：驳回清零后仍是一次通过即发布。
func TestReviewApproveWithoutRejectUnchanged(t *testing.T) {
	fx := newReviewFixture(t, 1)
	ctx := context.Background()
	q := fx.newDraftQuestion(t)

	if _, err := fx.svc.Submit(ctx, fx.editor, q.ID); err != nil {
		t.Fatalf("提交审核失败: %v", err)
	}
	det, err := fx.svc.Review(ctx, fx.manager, q.ID, &schema.ReviewRequest{Action: "approve"})
	if err != nil {
		t.Fatalf("通过失败: %v", err)
	}
	if det.Status != model.QuestionStatusPublished {
		t.Fatalf("阈值=1 时一次通过应直接发布，实际 %s", det.Status)
	}
	if det.ReviewCount != 1 {
		t.Fatalf("审核次数应为 1，实际 %d", det.ReviewCount)
	}
}

func TestReviewRejectsUnknownAction(t *testing.T) {
	fx := newReviewFixture(t, 1)
	ctx := context.Background()
	q := fx.newDraftQuestion(t)

	if _, err := fx.svc.Review(ctx, fx.manager, q.ID, &schema.ReviewRequest{Action: "archive"}); err == nil {
		t.Fatal("不支持的审核动作应被拒绝")
	} else if !apperr.Is(err, apperr.CodeInvalidParam) {
		t.Fatalf("应报参数错误，实际: %v", err)
	}
}

// 恢复回收站里的题目：必须真的能恢复（此前 Restore 走普通 Get，
// GORM 自动追加 deleted_at IS NULL，导致这个接口永远 404）。
func TestRestoreSoftDeletedQuestion(t *testing.T) {
	fx := newReviewFixture(t, 1)
	ctx := context.Background()
	q := fx.newDraftQuestion(t)

	deleted, _, err := fx.svc.Delete(ctx, fx.editor, []uint{q.ID})
	if err != nil {
		t.Fatalf("软删除失败: %v", err)
	}
	if len(deleted) != 1 {
		t.Fatalf("应删除 1 道题，实际 %d", len(deleted))
	}
	if _, _, err := fx.svc.List(ctx, fx.editor, fx.subject.ID, ListQuery{}, 10, 0); err != nil {
		t.Fatalf("列表查询失败: %v", err)
	}
	// 已删除的题目对普通读取不可见。
	if _, err := fx.svc.Get(ctx, fx.editor, q.ID); err == nil {
		t.Fatal("已软删除的题目不应能被读到")
	}

	if err := fx.svc.Restore(ctx, fx.editor, q.ID); err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	det, err := fx.svc.Get(ctx, fx.editor, q.ID)
	if err != nil {
		t.Fatalf("恢复后应能读到: %v", err)
	}
	if det.ID != q.ID {
		t.Fatalf("恢复后读到的是另一道题: %d", det.ID)
	}
}

// 恢复一道没删的题必须明确报错，而不是"静默成功"。
func TestRestoreLiveQuestionRejected(t *testing.T) {
	fx := newReviewFixture(t, 1)
	ctx := context.Background()
	q := fx.newDraftQuestion(t)

	err := fx.svc.Restore(ctx, fx.editor, q.ID)
	if err == nil {
		t.Fatal("恢复未删除的题目应被拒绝")
	}
	if !apperr.Is(err, apperr.CodeUnprocessable) {
		t.Fatalf("应报 422 语义错误，实际: %v", err)
	}
}

// 不存在的 id 仍然是 404，不能和"记录是活的"混为一谈。
func TestRestoreMissingQuestion(t *testing.T) {
	fx := newReviewFixture(t, 1)
	if err := fx.svc.Restore(context.Background(), fx.editor, 9999); err == nil {
		t.Fatal("恢复不存在的题目应报错")
	} else if !apperr.Is(err, apperr.CodeNotFound) {
		t.Fatalf("应报 404，实际: %v", err)
	}
}
