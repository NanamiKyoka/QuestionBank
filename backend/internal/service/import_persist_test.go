package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/NanamiKyoka/questionmind/internal/importing"
	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/schema"
)

// 抽取出来的草稿必须落进任务记录，而不能只活在那一个 HTTP 响应里。
//
// 这条链路的现实是：一次整卷抽取要跑几分钟，用户在等待期间刷新页面、
// 点走再回来、或者干脆关掉标签页都是常态。草稿只放在响应体里时，
// 那几分钟的结果就随响应一起蒸发——任务行上还剩一个"完成 8 道题"，
// 导入历史里点「审核草稿」却得到"该任务没有保存草稿"。
// 于是"提示成功"和"题库里一道题都没有"能同时成立。
func TestSyncExtractionPersistsDraftsForLaterReview(t *testing.T) {
	fx := newImportFixture(t, realisticExtraction)
	ctx := context.Background()

	res, err := fx.svc.Extract(ctx, fx.editor, fx.subject.ID, &schema.ExtractRequest{Content: "（试卷原文）"})
	if err != nil {
		t.Fatalf("抽取失败: %v", err)
	}
	if len(res.Drafts) == 0 {
		t.Fatal("用例本身应抽出题目")
	}

	// 模拟"换个浏览器再来"：完全不碰内存里的 res，只按任务 id 取。
	reloaded, err := fx.svc.TaskDrafts(ctx, fx.editor, fx.subject.ID, res.TaskID)
	if err != nil {
		t.Fatalf("按任务取回草稿失败: %v", err)
	}
	if len(reloaded.Drafts) != len(res.Drafts) {
		t.Fatalf("应取回 %d 份草稿，实际 %d", len(res.Drafts), len(reloaded.Drafts))
	}
	if reloaded.Total != len(res.Drafts) {
		t.Fatalf("total 应为 %d，实际 %d", len(res.Drafts), reloaded.Total)
	}

	// 取回的草稿必须仍然能直接入库：这才是"没白等"的判据。
	committed, err := fx.svc.Commit(ctx, fx.editor, fx.subject.ID, &schema.CommitRequest{
		Drafts: reloaded.Drafts,
	})
	if err != nil {
		t.Fatalf("用取回的草稿入库失败: %v", err)
	}
	if committed.CreatedCount != len(res.Drafts) || committed.FailedCount != 0 {
		t.Fatalf("应入库 %d 道，实际成功 %d 失败 %d",
			len(res.Drafts), committed.CreatedCount, committed.FailedCount)
	}
}

// 只入库题的草稿同样要落库：那是"直接在题库里补答案"这条路的入口。
func TestRecordExtractionPersistsDrafts(t *testing.T) {
	fx := newImportFixture(t, recordExtraction)
	ctx := context.Background()

	res, err := fx.svc.Extract(ctx, fx.editor, fx.subject.ID, &schema.ExtractRequest{
		Content: "（试卷原文）",
		Mode:    string(importing.ModeRecord),
	})
	if err != nil {
		t.Fatalf("仅录题抽取失败: %v", err)
	}

	stored, err := fx.svc.TaskDrafts(ctx, fx.editor, fx.subject.ID, res.TaskID)
	if err != nil {
		t.Fatalf("按任务取回草稿失败: %v", err)
	}
	if len(stored.Drafts) != len(res.Drafts) {
		t.Fatalf("应取回 %d 份草稿，实际 %d", len(res.Drafts), len(stored.Drafts))
	}
	for i := range stored.Drafts {
		if stored.Drafts[i].Answer.Kind != model.AnswerKindPending {
			t.Fatalf("第 %d 份草稿的答案应待补充，实际 %s", i, stored.Drafts[i].Answer.Kind)
		}
	}

	// 落在库里的必须是**结构化的草稿**，不是一段文本：
	// 历史页要按它渲染题目、按它算总数。
	task, err := fx.repos.ImportTasks.Get(ctx, res.TaskID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if task.Result.IsEmpty() {
		t.Fatal("任务记录里应当存下草稿")
	}
	var decoded schema.ExtractResponse
	if err := json.Unmarshal(task.Result, &decoded); err != nil {
		t.Fatalf("任务里的草稿应能还原成抽取响应: %v", err)
	}
	if len(decoded.Drafts) != len(res.Drafts) {
		t.Fatalf("还原出 %d 份草稿，期望 %d", len(decoded.Drafts), len(res.Drafts))
	}
}
