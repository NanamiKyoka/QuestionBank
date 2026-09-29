package service

import (
	"context"
	"testing"

	"github.com/NanamiKyoka/questionmind/internal/model"
)

// 回收站的两条链路曾经都是断的：
//
//  1. 列表侧只有 include_deleted，它返回"活的 + 删掉的"混在一起的结果，
//     而 SoftBase.DeletedAt 标记了 json:"-"，客户端拿不到任何字段区分两者。
//  2. 恢复侧先 Get 再恢复，而 Get 会被 GORM 自动追加 deleted_at IS NULL，
//     于是"恢复一条已删除的记录"永远 404——路由挂着，实现却从未成功过。
//
// 这两个用例锁死修复后的行为，避免以后再退化回去。

func newRecycleFixture(t *testing.T) (context.Context, *Repos, *model.User, *model.Subject) {
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
	return ctx, repos, admin, subject
}

func TestQuestionOnlyDeletedReturnsOnlyRemovedRows(t *testing.T) {
	ctx, repos, admin, subject := newRecycleFixture(t)

	alive := &model.Question{SubjectID: subject.ID, QuestionType: model.QuestionTypeSingleChoice,
		Status: model.QuestionStatusDraft, Stem: model.JSON(`{"type":"doc","content":[]}`), Answer: model.JSON(`{"kind":"single","correct":[]}`), CreatedBy: admin.ID}
	gone := &model.Question{SubjectID: subject.ID, QuestionType: model.QuestionTypeSingleChoice,
		Status: model.QuestionStatusDraft, Stem: model.JSON(`{"type":"doc","content":[]}`), Answer: model.JSON(`{"kind":"single","correct":[]}`), CreatedBy: admin.ID}
	for _, q := range []*model.Question{alive, gone} {
		if err := repos.Questions.Create(ctx, q); err != nil {
			t.Fatalf("建题目失败: %v", err)
		}
	}
	if err := repos.Questions.SoftDelete(ctx, []uint{gone.ID}); err != nil {
		t.Fatalf("软删除失败: %v", err)
	}

	svc := NewQuestionService(repos)

	// 默认列表：只看得到活的。
	list, total, err := svc.List(ctx, admin, subject.ID, ListQuery{}, 50, 0)
	if err != nil {
		t.Fatalf("默认列表失败: %v", err)
	}
	if total != 1 || len(list) != 1 || list[0].ID != alive.ID {
		t.Fatalf("默认列表应只含活题，实际 total=%d len=%d", total, len(list))
	}

	// include_deleted：活的与删掉的混在一起返回（既有语义，不改）。
	mixed, total, err := svc.List(ctx, admin, subject.ID, ListQuery{IncludeDeleted: true}, 50, 0)
	if err != nil {
		t.Fatalf("include_deleted 列表失败: %v", err)
	}
	if total != 2 || len(mixed) != 2 {
		t.Fatalf("include_deleted 应返回 2 条，实际 total=%d len=%d", total, len(mixed))
	}

	// only_deleted：只有被删掉的那条。
	only, total, err := svc.List(ctx, admin, subject.ID, ListQuery{OnlyDeleted: true}, 50, 0)
	if err != nil {
		t.Fatalf("only_deleted 列表失败: %v", err)
	}
	if total != 1 || len(only) != 1 || only[0].ID != gone.ID {
		t.Fatalf("only_deleted 应只含被删的题，实际 total=%d len=%d", total, len(only))
	}
}

func TestRestoreQuestionWorksAfterSoftDelete(t *testing.T) {
	ctx, repos, admin, subject := newRecycleFixture(t)

	q := &model.Question{SubjectID: subject.ID, QuestionType: model.QuestionTypeSingleChoice,
		Status: model.QuestionStatusDraft, Stem: model.JSON(`{"type":"doc","content":[]}`), Answer: model.JSON(`{"kind":"single","correct":[]}`), CreatedBy: admin.ID}
	if err := repos.Questions.Create(ctx, q); err != nil {
		t.Fatalf("建题目失败: %v", err)
	}
	if err := repos.Questions.SoftDelete(ctx, []uint{q.ID}); err != nil {
		t.Fatalf("软删除失败: %v", err)
	}

	// 恢复前：普通 Get 查不到——这正是旧实现永远 404 的原因。
	if _, err := repos.Questions.Get(ctx, q.ID); err == nil {
		t.Fatal("已删除的题目不该被普通 Get 读到")
	}
	// GetDeleted 能读到。
	if _, err := repos.Questions.GetDeleted(ctx, q.ID); err != nil {
		t.Fatalf("GetDeleted 读已删除题目失败: %v", err)
	}

	svc := NewQuestionService(repos)
	if err := svc.Restore(ctx, admin, q.ID); err != nil {
		t.Fatalf("恢复失败: %v", err)
	}

	// 恢复后重新出现在默认列表里。
	if _, err := repos.Questions.Get(ctx, q.ID); err != nil {
		t.Fatalf("恢复后应能正常读取: %v", err)
	}
	_, total, err := svc.List(ctx, admin, subject.ID, ListQuery{}, 50, 0)
	if err != nil {
		t.Fatalf("恢复后列表失败: %v", err)
	}
	if total != 1 {
		t.Fatalf("恢复后应重新可见，实际 total=%d", total)
	}

	// 再恢复一次应当被拒绝，而不是静默成功。
	if err := svc.Restore(ctx, admin, q.ID); err == nil {
		t.Fatal("恢复一条未被删除的题目应当报错")
	}
}

func TestPaperOnlyDeletedAndRestore(t *testing.T) {
	ctx, repos, admin, subject := newRecycleFixture(t)

	alive := &model.Paper{SubjectID: subject.ID, Title: "活的卷子", Status: model.PaperStatusDraft, CreatedBy: admin.ID}
	gone := &model.Paper{SubjectID: subject.ID, Title: "删掉的卷子", Status: model.PaperStatusDraft, CreatedBy: admin.ID}
	for _, p := range []*model.Paper{alive, gone} {
		if err := repos.Papers.Create(ctx, p); err != nil {
			t.Fatalf("建试卷失败: %v", err)
		}
	}
	if err := repos.Papers.SoftDelete(ctx, gone.ID); err != nil {
		t.Fatalf("软删除失败: %v", err)
	}

	svc := NewPaperService(repos)

	list, total, err := svc.ListPapers(ctx, admin, subject.ID, ListPapersQuery{}, 50, 0)
	if err != nil {
		t.Fatalf("默认列表失败: %v", err)
	}
	if total != 1 || len(list) != 1 {
		t.Fatalf("默认列表应只含活卷，实际 total=%d len=%d", total, len(list))
	}

	only, total, err := svc.ListPapers(ctx, admin, subject.ID, ListPapersQuery{OnlyDeleted: true}, 50, 0)
	if err != nil {
		t.Fatalf("only_deleted 列表失败: %v", err)
	}
	if total != 1 || len(only) != 1 {
		t.Fatalf("only_deleted 应只含被删的卷，实际 total=%d len=%d", total, len(only))
	}

	// 恢复：旧实现走 Get，永远 404。
	if err := svc.RestorePaper(ctx, admin, gone.ID); err != nil {
		t.Fatalf("恢复试卷失败: %v", err)
	}
	if _, err := repos.Papers.Get(ctx, gone.ID); err != nil {
		t.Fatalf("恢复后应能正常读取: %v", err)
	}
	// 重复恢复应被拒绝。
	if err := svc.RestorePaper(ctx, admin, gone.ID); err == nil {
		t.Fatal("恢复一张未被删除的试卷应当报错")
	}
}
