package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/NanamiKyoka/questionmind/internal/importing"
	"github.com/NanamiKyoka/questionmind/internal/infra/storage"
	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
	"github.com/NanamiKyoka/questionmind/internal/schema"
)

// ---- TASK 1：题目 ↔ 导入任务 ----

// 入库的题目必须能被追溯回产生它的那次导入。
//
// 这是整个导入域里最有价值的一条线索：没有它，
// "这批题是哪份卷子带来的"和"这道题为什么长这样"都无法回答。
func TestCommitLinksQuestionsToImportTask(t *testing.T) {
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
	if committed.TaskID == 0 {
		t.Fatal("入库必须返回任务 id")
	}

	// 逐条核对题目列上的 import_task_id。
	for _, id := range committed.CreatedIDs {
		q, err := fx.repos.Questions.Get(ctx, id)
		if err != nil {
			t.Fatalf("读取题目 %d 失败: %v", id, err)
		}
		if q.ImportTaskID == nil || *q.ImportTaskID != committed.TaskID {
			t.Fatalf("题目 %d 的 import_task_id 应为 %d，实际 %v", id, committed.TaskID, q.ImportTaskID)
		}
	}

	// 按任务反查应当得到同一批题目，且不多不少。
	briefs, total, err := fx.svc.TaskQuestions(ctx, fx.editor, fx.subject.ID, committed.TaskID, 50, 0)
	if err != nil {
		t.Fatalf("按任务查题目失败: %v", err)
	}
	if total != int64(committed.CreatedCount) {
		t.Fatalf("按任务反查应得到 %d 道，实际 %d", committed.CreatedCount, total)
	}
	if len(briefs) != committed.CreatedCount {
		t.Fatalf("应返回 %d 条简要信息，实际 %d", committed.CreatedCount, len(briefs))
	}
	// 顺序按创建时间升序：导入的题目应当保持卷面顺序。
	if len(briefs) == 2 && briefs[0].ID > briefs[1].ID {
		t.Fatalf("题目应按入库顺序返回，实际 %d 先于 %d", briefs[0].ID, briefs[1].ID)
	}

	// 另一个任务不应看到这批题。
	other := &model.ImportTask{SubjectID: &fx.subject.ID, UserID: fx.editor.ID,
		Source: model.ImportSourceAIAssist, Status: model.ImportStatusCompleted}
	if err := fx.repos.ImportTasks.Create(ctx, other); err != nil {
		t.Fatalf("建任务失败: %v", err)
	}
	_, otherTotal, err := fx.svc.TaskQuestions(ctx, fx.editor, fx.subject.ID, other.ID, 50, 0)
	if err != nil {
		t.Fatalf("按另一个任务查题目失败: %v", err)
	}
	if otherTotal != 0 {
		t.Fatalf("不相关的任务不应查到题目，实际 %d", otherTotal)
	}
}

// 任务不存在、或属于别的学科时，一律按"不存在"处理。
func TestTaskQuestionsRejectsForeignTask(t *testing.T) {
	fx := newImportFixture(t, realisticExtraction)
	ctx := context.Background()

	otherSubject := &model.Subject{Name: "物理", ReviewThreshold: 1, CreatedBy: fx.admin.ID}
	if err := fx.repos.Subjects.Create(ctx, otherSubject); err != nil {
		t.Fatalf("建学科失败: %v", err)
	}
	foreign := &model.ImportTask{SubjectID: &otherSubject.ID, UserID: fx.admin.ID,
		Source: model.ImportSourceAIAssist, Status: model.ImportStatusCompleted}
	if err := fx.repos.ImportTasks.Create(ctx, foreign); err != nil {
		t.Fatalf("建任务失败: %v", err)
	}

	// 404 而不是 403：告诉调用者"它在别的学科"等于送出一台 id 探测器。
	if _, _, err := fx.svc.TaskQuestions(ctx, fx.admin, fx.subject.ID, foreign.ID, 10, 0); !apperr.Is(err, apperr.CodeNotFound) {
		t.Fatalf("跨学科访问应返回 404，实际 %v", err)
	}
	if _, _, err := fx.svc.TaskQuestions(ctx, fx.admin, fx.subject.ID, 999999, 10, 0); !apperr.Is(err, apperr.CodeNotFound) {
		t.Fatalf("不存在的任务应返回 404，实际 %v", err)
	}
}

// ---- TASK 2：任务操作 ----

func newPendingTask(t *testing.T, fx *importFixture, status model.ImportTaskStatus) *model.ImportTask {
	t.Helper()
	task := &model.ImportTask{
		SubjectID:        &fx.subject.ID,
		UserID:           fx.editor.ID,
		Source:           model.ImportSourceAIAssist,
		Status:           status,
		FileType:         "text",
		Mode:             "extract",
		OriginalFilename: "粘贴文本",
	}
	if err := fx.repos.ImportTasks.Create(context.Background(), task); err != nil {
		t.Fatalf("建任务失败: %v", err)
	}
	return task
}

// 放弃 = 离开队列，而不是中断正在跑的抽取。
func TestCancelTaskAbandonsActiveTask(t *testing.T) {
	fx := newImportFixture(t, realisticExtraction)
	ctx := context.Background()
	task := newPendingTask(t, fx, model.ImportStatusProcessing)

	res, err := fx.svc.CancelTask(ctx, fx.editor, fx.subject.ID, task.ID)
	if err != nil {
		t.Fatalf("放弃失败: %v", err)
	}
	if res.Status != model.ImportStatusCancelled {
		t.Fatalf("状态应为已放弃，实际 %s", res.Status)
	}
	if res.Cancellable {
		t.Fatal("已放弃的任务不应再可取消")
	}
	if res.FinishedAt == nil {
		t.Fatal("放弃应写入结束时间")
	}

	stored, err := fx.repos.ImportTasks.Get(ctx, task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if stored.Status != model.ImportStatusCancelled {
		t.Fatalf("库里状态应为已放弃，实际 %s", stored.Status)
	}
	if stored.ErrorMsg == "" {
		t.Fatal("放弃应留下原因，避免历史里出现一条没有解释的记录")
	}
}

// 终态任务不能被放弃：否则"已完成"的历史会被悄悄改写成"已放弃"。
func TestCancelTaskRejectsTerminalState(t *testing.T) {
	fx := newImportFixture(t, realisticExtraction)
	ctx := context.Background()

	for _, status := range []model.ImportTaskStatus{
		model.ImportStatusCompleted, model.ImportStatusFailed, model.ImportStatusCancelled,
	} {
		task := newPendingTask(t, fx, status)
		_, err := fx.svc.CancelTask(ctx, fx.editor, fx.subject.ID, task.ID)
		if !apperr.Is(err, apperr.CodeConflict) {
			t.Fatalf("状态 %s 应返回 409，实际 %v", status, err)
		}
		stored, _ := fx.repos.ImportTasks.Get(ctx, task.ID)
		if stored.Status != status {
			t.Fatalf("失败后状态被改成了 %s（期望保持 %s）", stored.Status, status)
		}
	}
}

// 重试：失败 / 已放弃 / "完成了但一题都没抽出来"都可以。
func TestRetryTaskRequeuesRecoverableTasks(t *testing.T) {
	fx := newImportFixture(t, realisticExtraction)
	ctx := context.Background()

	cases := []struct {
		name   string
		status model.ImportTaskStatus
		total  int
	}{
		{"失败", model.ImportStatusFailed, 5},
		{"已放弃", model.ImportStatusCancelled, 5},
		// 模型把整卷当成了非题目内容（扫描件模糊、只有封面页），
		// 换个模型重跑是有意义的，不能因为"已完成"就判它没戏。
		{"完成但一题都没抽出来", model.ImportStatusCompleted, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			task := newPendingTask(t, fx, tc.status)
			task.TotalCount = tc.total
			task.ErrorMsg = "旧错误"
			if err := fx.repos.ImportTasks.UpdateFields(ctx, task.ID, map[string]any{
				"total_count": task.TotalCount, "error_msg": task.ErrorMsg,
			}); err != nil {
				t.Fatalf("写任务失败: %v", err)
			}

			res, err := fx.svc.RetryTask(ctx, fx.editor, fx.subject.ID, task.ID)
			if err != nil {
				t.Fatalf("重试失败: %v", err)
			}
			if res.Status != model.ImportStatusPending {
				t.Fatalf("重试后应回到等待处理，实际 %s", res.Status)
			}
			if res.ErrorMsg != "" {
				t.Fatalf("重试应清空旧错误，实际 %q", res.ErrorMsg)
			}
			if res.Retryable {
				t.Fatal("已重新排队的任务不应还能再次重试，否则就是个无限循环的按钮")
			}
		})
	}
}

// 抽到题目的成功任务不该被重试：重跑只会得到同一份结果。
func TestRetryTaskRejectsCompletedWithResults(t *testing.T) {
	fx := newImportFixture(t, realisticExtraction)
	ctx := context.Background()
	task := newPendingTask(t, fx, model.ImportStatusCompleted)
	task.TotalCount = 12
	if err := fx.repos.ImportTasks.UpdateFields(ctx, task.ID, map[string]any{"total_count": 12}); err != nil {
		t.Fatalf("写任务失败: %v", err)
	}

	if _, err := fx.svc.RetryTask(ctx, fx.editor, fx.subject.ID, task.ID); !apperr.Is(err, apperr.CodeConflict) {
		t.Fatalf("应返回 409，实际 %v", err)
	}
}

// 重试次数有上限，否则一个必定失败的输入能无限点。
func TestRetryTaskRespectsAttemptCap(t *testing.T) {
	fx := newImportFixture(t, realisticExtraction)
	ctx := context.Background()
	task := newPendingTask(t, fx, model.ImportStatusFailed)
	if err := fx.repos.ImportTasks.UpdateFields(ctx, task.ID, map[string]any{
		"attempt": model.MaxImportAttempt,
	}); err != nil {
		t.Fatalf("写任务失败: %v", err)
	}

	if _, err := fx.svc.RetryTask(ctx, fx.editor, fx.subject.ID, task.ID); !apperr.Is(err, apperr.CodeConflict) {
		t.Fatalf("达到上限后应拒绝重试，实际 %v", err)
	}
}

// 任务状态用 CAS 写入：并发的第二次放弃必须失败而不是静默覆盖。
func TestCancelTaskIsCompareAndSwap(t *testing.T) {
	fx := newImportFixture(t, realisticExtraction)
	ctx := context.Background()
	task := newPendingTask(t, fx, model.ImportStatusProcessing)

	if _, err := fx.svc.CancelTask(ctx, fx.editor, fx.subject.ID, task.ID); err != nil {
		t.Fatalf("首次放弃失败: %v", err)
	}
	// 第二次：状态已是 cancelled，CAS 的前置条件不成立。
	if _, err := fx.svc.CancelTask(ctx, fx.editor, fx.subject.ID, task.ID); !apperr.Is(err, apperr.CodeConflict) {
		t.Fatalf("重复放弃应返回 409，实际 %v", err)
	}
}

func TestDeleteTaskRemovesRecordOnly(t *testing.T) {
	fx := newImportFixture(t, realisticExtraction)
	ctx := context.Background()

	extracted, err := fx.svc.Extract(ctx, fx.editor, fx.subject.ID, &schema.ExtractRequest{Content: "x"})
	if err != nil {
		t.Fatalf("抽取失败: %v", err)
	}
	committed, err := fx.svc.Commit(ctx, fx.editor, fx.subject.ID, &schema.CommitRequest{Drafts: extracted.Drafts})
	if err != nil {
		t.Fatalf("入库失败: %v", err)
	}

	if err := fx.svc.DeleteTask(ctx, fx.editor, fx.subject.ID, committed.TaskID); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if _, err := fx.repos.ImportTasks.Get(ctx, committed.TaskID); !apperr.Is(err, apperr.CodeNotFound) {
		t.Fatalf("记录应已消失，实际 %v", err)
	}

	// 题目必须还在：导入记录是一次操作的历史，题目是内容。
	_, total, err := fx.repos.Questions.List(ctx, model.QuestionFilter{
		SubjectID: fx.subject.ID, ViewerID: fx.editor.ID,
	}, model.SortByCreatedDesc, 100, 0)
	if err != nil {
		t.Fatalf("查询题目失败: %v", err)
	}
	if total != int64(committed.CreatedCount) {
		t.Fatalf("删记录不应删题，实际剩下 %d 道", total)
	}
}

// 删除任务时顺手清掉它附带的落盘文件。
func TestDeleteTaskRemovesAttachedFile(t *testing.T) {
	fx := newImportFixture(t, realisticExtraction)
	ctx := context.Background()

	mediaDir := t.TempDir()
	store := storage.NewLocal(mediaDir, "/media")
	rel := "2026/01/paper.md"
	full := filepath.Join(mediaDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	if err := os.WriteFile(full, []byte("# 试卷"), 0o600); err != nil {
		t.Fatalf("写文件失败: %v", err)
	}

	task := newPendingTask(t, fx, model.ImportStatusCompleted)
	if err := fx.repos.ImportTasks.UpdateFields(ctx, task.ID, map[string]any{"file_path": rel}); err != nil {
		t.Fatalf("写 file_path 失败: %v", err)
	}
	if err := fx.svc.WithStorage(store).DeleteTask(ctx, fx.editor, fx.subject.ID, task.ID); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if _, err := os.Stat(full); !os.IsNotExist(err) {
		t.Fatalf("附件应被清理，实际 err=%v", err)
	}
}

// 任务操作沿用录题的权限：只读成员不能改动别人的导入记录。
func TestTaskOpsRequireEditPermission(t *testing.T) {
	fx := newImportFixture(t, realisticExtraction)
	ctx := context.Background()
	task := newPendingTask(t, fx, model.ImportStatusProcessing)

	viewer := &model.User{Base: model.Base{ID: 10}, Username: "viewer", IsActive: true}
	viewer.Memberships = []model.SubjectMember{
		{UserID: viewer.ID, SubjectID: fx.subject.ID, Role: model.SubjectRoleViewer},
	}
	if _, err := fx.svc.CancelTask(ctx, viewer, fx.subject.ID, task.ID); !apperr.Is(err, apperr.CodeForbidden) {
		t.Fatalf("只读成员放弃任务应 403，实际 %v", err)
	}
	if err := fx.svc.DeleteTask(ctx, viewer, fx.subject.ID, task.ID); !apperr.Is(err, apperr.CodeForbidden) {
		t.Fatalf("只读成员删除任务应 403，实际 %v", err)
	}
	// 但读历史是允许的。
	if _, _, err := fx.svc.ListTasks(ctx, viewer, fx.subject.ID, "", 0, 10, 0); err != nil {
		t.Fatalf("只读成员应能读导入历史: %v", err)
	}
}

// 队列状态：计数 + FIFO 位置，并如实声明没有 worker。
func TestQueueStatusCountsAndPositions(t *testing.T) {
	fx := newImportFixture(t, realisticExtraction)
	ctx := context.Background()

	older := newPendingTask(t, fx, model.ImportStatusPending)
	newer := newPendingTask(t, fx, model.ImportStatusPending)
	processing := newPendingTask(t, fx, model.ImportStatusProcessing)
	failed := newPendingTask(t, fx, model.ImportStatusFailed)
	// 显式拉开 updated_at，确保 FIFO 顺序是确定的而不是靠插入顺序碰运气。
	if err := fx.repos.ImportTasks.UpdateFields(ctx, older.ID, map[string]any{
		"updated_at": older.CreatedAt.Add(-2 * time.Hour),
	}); err != nil {
		t.Fatalf("写时间失败: %v", err)
	}
	if err := fx.repos.ImportTasks.UpdateFields(ctx, newer.ID, map[string]any{
		"updated_at": newer.CreatedAt.Add(-1 * time.Hour),
	}); err != nil {
		t.Fatalf("写时间失败: %v", err)
	}
	_ = failed

	res, err := fx.svc.QueueStatus(ctx, fx.admin, fx.subject.ID)
	if err != nil {
		t.Fatalf("读队列状态失败: %v", err)
	}
	if res.Stats.Pending != 2 || res.Stats.Processing != 1 || res.Stats.Failed != 1 {
		t.Fatalf("计数不对: %+v", res.Stats)
	}
	if res.Stats.Total != 4 {
		t.Fatalf("总数应为 4，实际 %d", res.Stats.Total)
	}
	if res.WorkerOnline {
		t.Fatal("还没有 worker，worker_online 必须是 false")
	}
	if res.Hint == "" {
		t.Fatal("必须给用户一句说明，否则用户会以为队列在动")
	}
	if len(res.ActiveTasks) != 3 {
		t.Fatalf("应有 3 个活动任务，实际 %d", len(res.ActiveTasks))
	}
	// 处理中的排在最前：它已经占住了执行位。
	if res.ActiveTasks[0].TaskID != processing.ID {
		t.Fatalf("队首应是处理中的任务 %d，实际 %d", processing.ID, res.ActiveTasks[0].TaskID)
	}
	// 其余按排队先后。
	if res.ActiveTasks[1].TaskID != older.ID || res.ActiveTasks[2].TaskID != newer.ID {
		t.Fatalf("FIFO 顺序不对: %d,%d 期望 %d,%d",
			res.ActiveTasks[1].TaskID, res.ActiveTasks[2].TaskID, older.ID, newer.ID)
	}
	for i, item := range res.ActiveTasks {
		if item.QueuePosition != i+1 {
			t.Fatalf("第 %d 项的队列位置应为 %d，实际 %d", i, i+1, item.QueuePosition)
		}
	}
}

// ---- TASK 3：按人隔离 ----

// 任何能读该学科的成员都不该看到同事的导入历史。
func TestListTasksIsScopedPerUser(t *testing.T) {
	fx := newImportFixture(t, realisticExtraction)
	ctx := context.Background()

	mine := newPendingTask(t, fx, model.ImportStatusCompleted)
	mine.UserID = fx.editor.ID
	theirs := &model.ImportTask{
		SubjectID: &fx.subject.ID, UserID: fx.admin.ID,
		Source: model.ImportSourceAIAssist, Status: model.ImportStatusCompleted,
		OriginalFilename: "同事的卷子",
	}
	if err := fx.repos.ImportTasks.Create(ctx, theirs); err != nil {
		t.Fatalf("建任务失败: %v", err)
	}
	_ = mine

	// 普通成员只看到自己那一条。
	rows, total, err := fx.svc.ListTasks(ctx, fx.editor, fx.subject.ID, "", 0, 10, 0)
	if err != nil {
		t.Fatalf("读历史失败: %v", err)
	}
	if total != 1 || len(rows) != 1 {
		t.Fatalf("普通成员应只看到 1 条，实际 %d", total)
	}
	if rows[0].UserID != fx.editor.ID {
		t.Fatalf("不该看到别人的记录，实际 user_id=%d", rows[0].UserID)
	}

	// 管理员默认看全部。
	rows, total, err = fx.svc.ListTasks(ctx, fx.admin, fx.subject.ID, "", 0, 10, 0)
	if err != nil {
		t.Fatalf("管理员读历史失败: %v", err)
	}
	if total != 2 || len(rows) != 2 {
		t.Fatalf("管理员应看到 2 条，实际 %d", total)
	}

	// 管理员可以按人筛。
	_, total, err = fx.svc.ListTasks(ctx, fx.admin, fx.subject.ID, "", fx.admin.ID, 10, 0)
	if err != nil {
		t.Fatalf("管理员按人筛失败: %v", err)
	}
	if total != 1 {
		t.Fatalf("按人筛应得到 1 条，实际 %d", total)
	}
}

// 普通成员即使伪造 user_id 参数，也只能看到自己的记录。
func TestListTasksIgnoresUserIDForNonSuperuser(t *testing.T) {
	fx := newImportFixture(t, realisticExtraction)
	ctx := context.Background()

	foreign := &model.ImportTask{SubjectID: &fx.subject.ID, UserID: fx.admin.ID,
		Source: model.ImportSourceAIAssist, Status: model.ImportStatusCompleted}
	if err := fx.repos.ImportTasks.Create(ctx, foreign); err != nil {
		t.Fatalf("建任务失败: %v", err)
	}

	_, total, err := fx.svc.ListTasks(ctx, fx.editor, fx.subject.ID, "", fx.admin.ID, 10, 0)
	if err != nil {
		t.Fatalf("读历史失败: %v", err)
	}
	if total != 0 {
		t.Fatalf("普通成员不得借 user_id 越权，实际看到 %d 条", total)
	}
}

// ---- OriginalFilename ----

// 导入记录必须能回答"这次到底导了什么"，哪怕还没有文件上传。
func TestImportRecordsCarrySourceDescription(t *testing.T) {
	fx := newImportFixture(t, realisticExtraction)
	ctx := context.Background()

	t.Run("粘贴文本", func(t *testing.T) {
		res, err := fx.svc.Extract(ctx, fx.editor, fx.subject.ID, &schema.ExtractRequest{Content: "x"})
		if err != nil {
			t.Fatalf("抽取失败: %v", err)
		}
		rows, _, err := fx.svc.ListTasks(ctx, fx.editor, fx.subject.ID, "", 0, 10, 0)
		if err != nil {
			t.Fatalf("读历史失败: %v", err)
		}
		found := findTask(rows, res.TaskID)
		if found == nil {
			t.Fatal("历史里找不到这次抽取")
		}
		if found.OriginalFilename != "粘贴文本" {
			t.Fatalf("来源描述应为「粘贴文本」，实际 %q", found.OriginalFilename)
		}
		// 状态机字段必须自洽：完成了就该有结束时间与已执行次数。
		if found.Status != model.ImportStatusCompleted {
			t.Fatalf("状态应为已完成，实际 %s", found.Status)
		}
		if found.FinishedAt == nil {
			t.Fatal("完成的任务应有结束时间")
		}
		if found.Attempt != 1 {
			t.Fatalf("应记录 1 次尝试，实际 %d", found.Attempt)
		}
	})

	t.Run("图片识别", func(t *testing.T) {
		// 直接验标签生成函数：走完整 Extract 需要额外配置视觉模型，
		// 那是另一条链路的测试，不该混在这里。
		got := extractSourceLabel(&schema.ExtractRequest{
			Images: []string{"https://example.com/a.png", "https://example.com/b.png"},
		}, importing.ModeExtract)
		if got != "图片识别（2 张）" {
			t.Fatalf("来源描述应为「图片识别（2 张）」，实际 %q", got)
		}
		if got := extractSourceLabel(&schema.ExtractRequest{Content: "x"}, importing.ModeSolve); got != "补写答案" {
			t.Fatalf("solve 模式描述应为「补写答案」，实际 %q", got)
		}
	})

	t.Run("调用方给了文件名就用它", func(t *testing.T) {
		res, err := fx.svc.Extract(ctx, fx.editor, fx.subject.ID, &schema.ExtractRequest{
			Content: "x", Filename: "2026秋 高一数学期中.docx",
		})
		if err != nil {
			t.Fatalf("抽取失败: %v", err)
		}
		rows, _, _ := fx.svc.ListTasks(ctx, fx.editor, fx.subject.ID, "", 0, 20, 0)
		found := findTask(rows, res.TaskID)
		if found == nil {
			t.Fatal("历史里找不到这次抽取")
		}
		if found.OriginalFilename != "2026秋 高一数学期中.docx" {
			t.Fatalf("应保留调用方给的文件名，实际 %q", found.OriginalFilename)
		}
	})

	t.Run("入库记录也有来源描述", func(t *testing.T) {
		committed, err := fx.svc.Commit(ctx, fx.editor, fx.subject.ID, &schema.CommitRequest{
			Drafts: []importing.Draft{{
				QuestionType: model.QuestionTypeEssay,
				StemMarkdown: "证明 1+1=2。",
				Answer:       model.AnswerSpec{Kind: "reference", Reference: importing.MarkdownToRichDoc("略")},
			}},
		})
		if err != nil {
			t.Fatalf("入库失败: %v", err)
		}
		rows, _, _ := fx.svc.ListTasks(ctx, fx.editor, fx.subject.ID, "", 0, 50, 0)
		found := findTask(rows, committed.TaskID)
		if found == nil {
			t.Fatal("历史里找不到这次入库")
		}
		if found.OriginalFilename != "入库审核（1 题）" {
			t.Fatalf("来源描述应为「入库审核（1 题）」，实际 %q", found.OriginalFilename)
		}
	})
}

// 文件名要清洗：路径与控制字符都不该被原样存下并回显。
func TestSanitizeFilenameRejectsPathAndControlChars(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"   ", ""},
		{"期中试卷.docx", "期中试卷.docx"},
		{"../../etc/passwd", "passwd"},
		{`C:\Users\me\试卷.docx`, "试卷.docx"},
		{"a\x00b\x07c.txt", "abc.txt"},
	}
	for _, tc := range cases {
		if got := sanitizeFilename(tc.in); got != tc.want {
			t.Fatalf("sanitizeFilename(%q) = %q，期望 %q", tc.in, got, tc.want)
		}
	}
}

// 状态筛选依然可用（与按人隔离叠加，而不是互相替代）。
func TestListTasksStatusFilter(t *testing.T) {
	fx := newImportFixture(t, realisticExtraction)
	ctx := context.Background()
	ok := newPendingTask(t, fx, model.ImportStatusCompleted)
	bad := newPendingTask(t, fx, model.ImportStatusFailed)

	rows, total, err := fx.svc.ListTasks(ctx, fx.admin, fx.subject.ID, model.ImportStatusFailed, 0, 10, 0)
	if err != nil {
		t.Fatalf("读历史失败: %v", err)
	}
	if total != 1 || len(rows) != 1 || rows[0].ID != bad.ID {
		t.Fatalf("状态筛选结果不对: total=%d ids=%v", total, idsOf(rows))
	}
	if ok.ID == bad.ID {
		t.Fatal("测试数据构造有误")
	}
}

func findTask(rows []schema.ImportTaskResponse, id uint) *schema.ImportTaskResponse {
	for i := range rows {
		if rows[i].ID == id {
			return &rows[i]
		}
	}
	return nil
}

func idsOf(rows []schema.ImportTaskResponse) []uint {
	out := make([]uint, 0, len(rows))
	for i := range rows {
		out = append(out, rows[i].ID)
	}
	return out
}
