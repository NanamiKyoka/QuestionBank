package service

import (
	"context"
	"testing"

	"github.com/NanamiKyoka/questionmind/internal/importing"
	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
	"github.com/NanamiKyoka/questionmind/internal/schema"
)

// publishToPublished 把一道草稿一路推到已发布（提交 + 审核）。
func publishToPublished(t *testing.T, fx *reviewFixture, id uint) *schema.QuestionDetail {
	t.Helper()
	ctx := context.Background()
	if _, err := fx.svc.Submit(ctx, fx.editor, id); err != nil {
		t.Fatalf("提交审核失败: %v", err)
	}
	det, err := fx.svc.Review(ctx, fx.manager, id, &schema.ReviewRequest{Action: model.ReviewActionApprove})
	if err != nil {
		t.Fatalf("审核失败: %v", err)
	}
	if det.Status != model.QuestionStatusPublished {
		t.Fatalf("准备已发布题目失败，当前状态 %s", det.Status)
	}
	return det
}

// ---- 归档（Task 3）----

// 归档：已发布 → 已归档 → 取消归档回到已发布，并留下审核日志。
func TestArchiveRoundTrip(t *testing.T) {
	fx := newReviewFixture(t, 1)
	ctx := context.Background()
	q := fx.newDraftQuestion(t)
	publishToPublished(t, fx, q.ID)

	det, err := fx.svc.SetArchived(ctx, fx.manager, q.ID, true, "与新版教材口径不一致")
	if err != nil {
		t.Fatalf("归档失败: %v", err)
	}
	if det.Status != model.QuestionStatusArchived {
		t.Fatalf("归档后状态应为 archived，实际 %s", det.Status)
	}
	// 归档只改状态，不改题面，因此不能让引用它的试卷误报"题目已更新"。
	if det.ContentRevision != q.ContentRevision {
		t.Fatalf("归档不应递增 content_revision：%d → %d", q.ContentRevision, det.ContentRevision)
	}

	// 归档后必须能被状态筛选查到（否则"已归档"选项永远返回空）。
	_, total, err := fx.svc.List(ctx, fx.editor, fx.subject.ID, ListQuery{Status: model.QuestionStatusArchived}, 10, 0)
	if err != nil {
		t.Fatalf("按状态筛选失败: %v", err)
	}
	if total != 1 {
		t.Fatalf("归档筛选应命中 1 道题，实际 %d（archived 状态依然不可达）", total)
	}

	det, err = fx.svc.SetArchived(ctx, fx.manager, q.ID, false, "")
	if err != nil {
		t.Fatalf("取消归档失败: %v", err)
	}
	if det.Status != model.QuestionStatusPublished {
		t.Fatalf("取消归档应回到已发布，实际 %s", det.Status)
	}

	logs, err := fx.svc.ReviewLogs(ctx, fx.manager, q.ID, 50)
	if err != nil {
		t.Fatalf("读取审核日志失败: %v", err)
	}
	var archived, restored bool
	for _, l := range logs {
		if l.Action == model.ReviewActionArchive && l.ToStatus == model.QuestionStatusArchived &&
			l.Comment == "与新版教材口径不一致" {
			archived = true
		}
		if l.Action == model.ReviewActionRestore {
			restored = true
		}
	}
	if !archived || !restored {
		t.Fatalf("归档/取消归档都应留痕，实际日志 %+v", logs)
	}
}

// 草稿不能直接归档：待审与草稿本来就没对外，归档它们只会污染归档筛选。
func TestArchiveRequiresPublished(t *testing.T) {
	fx := newReviewFixture(t, 1)
	q := fx.newDraftQuestion(t)

	if _, err := fx.svc.SetArchived(context.Background(), fx.manager, q.ID, true, ""); err == nil {
		t.Fatal("草稿题不应能被归档")
	} else if !apperr.Is(err, apperr.CodeConflict) {
		t.Fatalf("应报状态冲突，实际: %v", err)
	}
}

// 归档需要审核权限：普通录入者不能把别人已经发布的题下架。
func TestArchiveRequiresReviewPermission(t *testing.T) {
	fx := newReviewFixture(t, 1)
	q := fx.newDraftQuestion(t)
	publishToPublished(t, fx, q.ID)

	if _, err := fx.svc.SetArchived(context.Background(), fx.editor, q.ID, true, ""); err == nil {
		t.Fatal("无审核权限者不应能归档")
	} else if !apperr.Is(err, apperr.CodeForbidden) {
		t.Fatalf("应报无权限，实际: %v", err)
	}
}

// ---- 批量提交 / 通过 / 驳回（Task 4）----

// 批量提交：草稿进队列，混进来的已发布题被跳过而不是整批失败。
func TestBatchSubmitSkipsNonDraft(t *testing.T) {
	fx := newReviewFixture(t, 1)
	ctx := context.Background()
	first := fx.newDraftQuestion(t)
	second := fx.newDraftQuestion(t)
	published := fx.newDraftQuestion(t)
	publishToPublished(t, fx, published.ID)

	handled, skipped, err := fx.svc.BatchReview(ctx, fx.editor,
		[]uint{first.ID, published.ID, second.ID, 9999}, "submit", "")
	if err != nil {
		t.Fatalf("批量提交失败: %v", err)
	}
	if len(handled) != 2 {
		t.Fatalf("应处理 2 道草稿，实际 %d", len(handled))
	}
	if len(skipped) != 2 {
		t.Fatalf("已发布题与不存在的 id 应被跳过共 2 个，实际 %d", len(skipped))
	}
	for _, id := range []uint{first.ID, second.ID} {
		det, err := fx.svc.Get(ctx, fx.editor, id)
		if err != nil {
			t.Fatalf("读取失败: %v", err)
		}
		if det.Status != model.QuestionStatusPending {
			t.Fatalf("题目 %d 应进入待审，实际 %s", id, det.Status)
		}
	}
}

// 批量通过必须遵守学科阈值：阈值=2 时一次批量通过只能过一道关。
func TestBatchApproveHonoursReviewThreshold(t *testing.T) {
	fx := newReviewFixture(t, 2)
	ctx := context.Background()
	q := fx.newDraftQuestion(t)
	if _, err := fx.svc.Submit(ctx, fx.editor, q.ID); err != nil {
		t.Fatalf("提交失败: %v", err)
	}

	handled, skipped, err := fx.svc.BatchReview(ctx, fx.manager, []uint{q.ID}, model.ReviewActionApprove, "")
	if err != nil {
		t.Fatalf("批量通过失败: %v", err)
	}
	if len(handled) != 1 || len(skipped) != 0 {
		t.Fatalf("应处理 1 条，实际 handled=%d skipped=%d", len(handled), len(skipped))
	}
	det, err := fx.svc.Get(ctx, fx.manager, q.ID)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if det.Status != model.QuestionStatusPending || det.ReviewCount != 1 {
		t.Fatalf("批量通过不得绕过学科阈值：状态 %s 次数 %d", det.Status, det.ReviewCount)
	}
}

// 批量驳回要清零审核次数，并且需要审核权限。
func TestBatchRejectResetsReviewCount(t *testing.T) {
	fx := newReviewFixture(t, 2)
	ctx := context.Background()
	q := fx.newDraftQuestion(t)
	if _, err := fx.svc.Submit(ctx, fx.editor, q.ID); err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	if _, err := fx.svc.Review(ctx, fx.manager, q.ID, &schema.ReviewRequest{Action: model.ReviewActionApprove}); err != nil {
		t.Fatalf("第一次通过失败: %v", err)
	}

	// 无审核权限者调用批量通过：整条请求不算失败（批量语义是逐条跳过），
	// 但这道题必须落在 skipped 里——绝不能悄悄把它算成"已处理"。
	handled, skipped, err := fx.svc.BatchReview(ctx, fx.editor, []uint{q.ID}, model.ReviewActionApprove, "")
	if err != nil {
		t.Fatalf("批量通过不应整体报错: %v", err)
	}
	if len(handled) != 0 || len(skipped) != 1 {
		t.Fatalf("无审核权限者不应处理任何题目，handled=%d skipped=%d", len(handled), len(skipped))
	}

	handled, _, err = fx.svc.BatchReview(ctx, fx.manager, []uint{q.ID}, model.ReviewActionReject, "答案有误")
	if err != nil {
		t.Fatalf("批量驳回失败: %v", err)
	}
	if len(handled) != 1 {
		t.Fatalf("应驳回 1 条，实际 %d", len(handled))
	}
	det, err := fx.svc.Get(ctx, fx.manager, q.ID)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if det.Status != model.QuestionStatusDraft || det.ReviewCount != 0 {
		t.Fatalf("批量驳回应退回草稿并清零计数，实际 状态 %s 次数 %d", det.Status, det.ReviewCount)
	}
}

// 非法动作必须被拒，而不是当成"什么都没做"返回成功。
func TestBatchReviewRejectsUnknownAction(t *testing.T) {
	fx := newReviewFixture(t, 1)
	q := fx.newDraftQuestion(t)
	_, _, err := fx.svc.BatchReview(context.Background(), fx.manager, []uint{q.ID}, "archive", "")
	if err == nil {
		t.Fatal("不支持的批量动作应被拒绝")
	}
	if !apperr.Is(err, apperr.CodeInvalidParam) {
		t.Fatalf("应报参数错误，实际: %v", err)
	}
}

// ---- 母子题（Task 1）----

// 改挂母题必须拒绝成环：A 挂到 B 下之后，不能再把 B 挂回 A 下。
//
// 成环的后果不是"数据难看"：级联软删靠逐层向下展开子 id，
// 遇到环会永远转下去，整个请求再也返回不了。
func TestReParentRejectsCycle(t *testing.T) {
	fx := newReviewFixture(t, 1)
	ctx := context.Background()

	parent := fx.newDraftQuestion(t)
	child, err := fx.svc.Create(ctx, fx.editor, &schema.QuestionCreateRequest{
		SubjectID:    fx.subject.ID,
		ParentID:     &parent.ID,
		QuestionType: model.QuestionTypeSingleChoice,
		Stem:         importing.MarkdownToRichDoc("小问：求导数。"),
		Options: []schema.OptionInput{
			{Label: "A", Content: importing.MarkdownToRichDoc("2x")},
			{Label: "B", Content: importing.MarkdownToRichDoc("0")},
		},
		Answer: model.AnswerSpec{Kind: "single", Correct: []string{"A"}},
	})
	if err != nil {
		t.Fatalf("建子题失败: %v", err)
	}

	// 正常改挂：把母题挂到另一道独立题下。
	other := fx.newDraftQuestion(t)
	otherID := other.ID
	if _, err := fx.svc.Update(ctx, fx.editor, parent.ID, &schema.QuestionUpdateRequest{ParentID: &otherID}); err != nil {
		t.Fatalf("改挂母题失败: %v", err)
	}

	// 成环：把 other 挂到 parent 下（parent 已经是 other 的子题）。
	parentID := parent.ID
	_, err = fx.svc.Update(ctx, fx.editor, other.ID, &schema.QuestionUpdateRequest{ParentID: &parentID})
	if err == nil {
		t.Fatal("把祖先挂到自己的后代下应被拒绝")
	}
	if !apperr.Is(err, apperr.CodeInvalidParam) {
		t.Fatalf("应报参数错误，实际: %v", err)
	}

	// 解除挂载：0 表示变回独立题。
	zero := uint(0)
	det, err := fx.svc.Update(ctx, fx.editor, child.ID, &schema.QuestionUpdateRequest{ParentID: &zero})
	if err != nil {
		t.Fatalf("解除挂载失败: %v", err)
	}
	if det.ParentID != nil {
		t.Fatalf("parent_id=0 应把子题变回独立题，实际仍挂在 %d 下", *det.ParentID)
	}
}

// 改挂只能挂到同学科的题上。
func TestReParentRejectsCrossSubject(t *testing.T) {
	fx := newReviewFixture(t, 1)
	ctx := context.Background()
	q := fx.newDraftQuestion(t)

	other := &model.Subject{Name: "物理", ReviewThreshold: 1, CreatedBy: 1}
	if err := fx.repos.Subjects.Create(ctx, other); err != nil {
		t.Fatalf("建学科失败: %v", err)
	}
	// 学科权限按成员关系判定，不加成员的话造题这一步自己就 403 了。
	fx.manager.Memberships = append(fx.manager.Memberships, model.SubjectMember{
		UserID: fx.manager.ID, SubjectID: other.ID, Role: model.SubjectRoleManager,
	})
	foreign, err := fx.svc.Create(ctx, fx.manager, &schema.QuestionCreateRequest{
		SubjectID:    other.ID,
		QuestionType: model.QuestionTypeSingleChoice,
		Stem:         importing.MarkdownToRichDoc("物理题。"),
		Options: []schema.OptionInput{
			{Label: "A", Content: importing.MarkdownToRichDoc("1")},
			{Label: "B", Content: importing.MarkdownToRichDoc("2")},
		},
		Answer: model.AnswerSpec{Kind: "single", Correct: []string{"A"}},
	})
	if err != nil {
		t.Fatalf("建题失败: %v", err)
	}

	foreignID := foreign.ID
	if _, err := fx.svc.Update(ctx, fx.manager, q.ID, &schema.QuestionUpdateRequest{ParentID: &foreignID}); err == nil {
		t.Fatal("挂到别的小学科的母题下应被拒绝")
	} else if !apperr.Is(err, apperr.CodeCrossSubject) {
		t.Fatalf("应报跨学科错误，实际: %v", err)
	}
}

// ---- 已发布题目的内容修改（Task 2）----

// 已发布题目的内容可以被改（服务端本来就没有状态守卫），
// 但每一次改都必须留痕——题面已经在别人的试卷里了。
func TestEditingPublishedQuestionIsAudited(t *testing.T) {
	fx := newReviewFixture(t, 1)
	ctx := context.Background()
	q := fx.newDraftQuestion(t)
	published := publishToPublished(t, fx, q.ID)

	newStem := importing.MarkdownToRichDoc("已知 f(x)=x^2，则 f(2)=（  ）【勘误】")
	det, err := fx.svc.Update(ctx, fx.editor, q.ID, &schema.QuestionUpdateRequest{
		Stem:       newStem,
		ChangeNote: "修正上标",
	})
	if err != nil {
		t.Fatalf("修改已发布题目失败: %v", err)
	}
	if det.ContentRevision <= published.ContentRevision {
		t.Fatalf("内容变更必须递增 content_revision（试卷据此提示题目已改动）：%d → %d",
			published.ContentRevision, det.ContentRevision)
	}
	// 状态不变：改已发布的题不重新触发审核。
	if det.Status != model.QuestionStatusPublished {
		t.Fatalf("改内容不应改变状态，实际 %s", det.Status)
	}

	logs, err := fx.svc.ReviewLogs(ctx, fx.editor, q.ID, 50)
	if err != nil {
		t.Fatalf("读取审核日志失败: %v", err)
	}
	var edited bool
	for _, l := range logs {
		if l.Action == model.ReviewActionEdit && l.Comment == "修正上标" {
			edited = true
		}
	}
	if !edited {
		t.Fatalf("已发布题目的内容改动必须留痕，实际日志 %+v", logs)
	}
}

// 草稿的内容修改不写审核日志：它还没上线，审核记录里出现的
// 每一条都应该是状态流转，多出来的噪音会让"谁改了这道已发布的题"更难找。
func TestEditingDraftQuestionLeavesNoReviewTrail(t *testing.T) {
	fx := newReviewFixture(t, 1)
	ctx := context.Background()
	q := fx.newDraftQuestion(t)

	if _, err := fx.svc.Update(ctx, fx.editor, q.ID, &schema.QuestionUpdateRequest{
		Stem: importing.MarkdownToRichDoc("草稿改一版。"),
	}); err != nil {
		t.Fatalf("修改草稿失败: %v", err)
	}
	logs, err := fx.svc.ReviewLogs(ctx, fx.editor, q.ID, 50)
	if err != nil {
		t.Fatalf("读取审核日志失败: %v", err)
	}
	if len(logs) != 0 {
		t.Fatalf("草稿的内容修改不应产生审核日志，实际 %+v", logs)
	}
}

// ---- 筛选维度（Task 6）----

// 来源模糊匹配 + 审核次数等值 + "被谁审过"。
func TestListFiltersSourceReviewCountAndReviewer(t *testing.T) {
	fx := newReviewFixture(t, 2)
	ctx := context.Background()

	matched := fx.newDraftQuestion(t)
	if _, err := fx.svc.Update(ctx, fx.editor, matched.ID, &schema.QuestionUpdateRequest{
		Source: strptr("2024 期中试卷.pdf"),
	}); err != nil {
		t.Fatalf("改来源失败: %v", err)
	}
	other := fx.newDraftQuestion(t)
	if _, err := fx.svc.Update(ctx, fx.editor, other.ID, &schema.QuestionUpdateRequest{
		Source: strptr("2024 期末试卷.pdf"),
	}); err != nil {
		t.Fatalf("改来源失败: %v", err)
	}

	briefs, total, err := fx.svc.List(ctx, fx.editor, fx.subject.ID, ListQuery{Source: "期中"}, 10, 0)
	if err != nil {
		t.Fatalf("按来源筛选失败: %v", err)
	}
	if total != 1 || briefs[0].ID != matched.ID {
		t.Fatalf("来源模糊匹配应只命中 1 道，实际 total=%d", total)
	}

	// 审核次数为 0 是一个有意义的取值，必须能筛出来。
	zero := 0
	if _, total, err = fx.svc.List(ctx, fx.editor, fx.subject.ID, ListQuery{ReviewCount: &zero}, 10, 0); err != nil {
		t.Fatalf("按审核次数筛选失败: %v", err)
	}
	if total != 2 {
		t.Fatalf("两道题都没被审过，审核次数=0 应命中 2 道，实际 %d", total)
	}

	// 还没人审过 → 按审核人筛选命中 0 条。
	if _, total, err = fx.svc.List(ctx, fx.editor, fx.subject.ID, ListQuery{ReviewerID: fx.manager.ID}, 10, 0); err != nil {
		t.Fatalf("按审核人筛选失败: %v", err)
	}
	if total != 0 {
		t.Fatalf("无人审核过，命中应为 0，实际 %d", total)
	}

	if _, err := fx.svc.Submit(ctx, fx.editor, matched.ID); err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	if _, err := fx.svc.Review(ctx, fx.manager, matched.ID, &schema.ReviewRequest{Action: model.ReviewActionApprove}); err != nil {
		t.Fatalf("审核失败: %v", err)
	}
	if _, total, err = fx.svc.List(ctx, fx.editor, fx.subject.ID, ListQuery{ReviewerID: fx.manager.ID}, 10, 0); err != nil {
		t.Fatalf("按审核人筛选失败: %v", err)
	}
	if total != 1 {
		t.Fatalf("按审核人筛选应命中 1 道，实际 %d", total)
	}
}

func strptr(s string) *string { return &s }
