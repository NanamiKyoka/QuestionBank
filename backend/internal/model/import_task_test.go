package model

import (
	"testing"
	"time"
)

// 任务状态机是"能不能亮某个按钮"的唯一真相源，因此在这里钉住它的边界。
func TestImportTaskRetryable(t *testing.T) {
	cases := []struct {
		name string
		task ImportTask
		want bool
	}{
		{
			name: "失败且未达上限",
			task: ImportTask{Status: ImportStatusFailed, Attempt: 1},
			want: true,
		},
		{
			name: "失败但已达上限",
			task: ImportTask{Status: ImportStatusFailed, Attempt: MaxImportAttempt},
			want: false,
		},
		{
			name: "已放弃",
			task: ImportTask{Status: ImportStatusCancelled, Attempt: 1},
			want: true,
		},
		{
			name: "处理中不可重试",
			task: ImportTask{Status: ImportStatusProcessing, Attempt: 1},
			want: false,
		},
		{
			// 模型把整卷当成非题目内容时，任务"成功"了却一题没抽出来。
			// 这种情况必须留有第二次机会，否则用户只能整份重新粘一遍。
			name: "已完成但一题没抽出来",
			task: ImportTask{Status: ImportStatusCompleted, TotalCount: 0, Attempt: 1},
			want: true,
		},
		{
			name: "已抽到题",
			task: ImportTask{Status: ImportStatusCompleted, TotalCount: 12, Attempt: 1},
			want: false,
		},
		{
			name: "已排队（重试后）不可再重试",
			task: ImportTask{Status: ImportStatusPending, Attempt: 1},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			task := tc.task
			if got := task.Retryable(MaxImportAttempt); got != tc.want {
				t.Fatalf("Retryable = %v，期望 %v", got, tc.want)
			}
		})
	}
}

// 只有等待处理与处理中可以放弃。
func TestImportTaskIsActive(t *testing.T) {
	for _, s := range []ImportTaskStatus{ImportStatusPending, ImportStatusProcessing} {
		task := ImportTask{Status: s}
		if !task.IsActive() {
			t.Fatalf("%s 应视为活动状态", s)
		}
	}
	for _, s := range []ImportTaskStatus{ImportStatusCompleted, ImportStatusFailed, ImportStatusCancelled} {
		task := ImportTask{Status: s}
		if task.IsActive() {
			t.Fatalf("%s 是终态，不应可放弃", s)
		}
	}
}

// 重试把任务复位成"等待处理"，并清空上一轮的残留。
func TestImportTaskMarkRetriedClearsStaleState(t *testing.T) {
	now := time.Now().UTC()
	task := ImportTask{
		Status:     ImportStatusFailed,
		ErrorMsg:   "模型超时",
		Attempt:    2,
		StartedAt:  &now,
		FinishedAt: &now,
		Result:     JSON(`{"count":3}`),
	}
	task.MarkRetried()

	if task.Status != ImportStatusPending {
		t.Fatalf("状态应为 pending，实际 %s", task.Status)
	}
	if task.ErrorMsg != "" {
		t.Fatalf("应清空错误信息，实际 %q", task.ErrorMsg)
	}
	if !task.Result.IsEmpty() {
		t.Fatalf("应清空结构化结果，实际 %s", task.Result)
	}
	if task.StartedAt != nil || task.FinishedAt != nil {
		t.Fatal("重新排队不应带着上一轮的时间戳")
	}
	// 重新排队本身没有消耗执行代价，真正的计数在 MarkProcessing。
	if task.Attempt != 2 {
		t.Fatalf("Attempt 不应在排队时自增，实际 %d", task.Attempt)
	}
}

// Attempt 记的是"已经跑过几次"，而不是"排过几次队"。
func TestImportTaskMarkProcessingCountsAttempt(t *testing.T) {
	task := ImportTask{Status: ImportStatusPending}
	task.MarkProcessing()
	task.MarkProcessing()
	if task.Attempt != 2 {
		t.Fatalf("应记录 2 次尝试，实际 %d", task.Attempt)
	}
	if task.StartedAt == nil {
		t.Fatal("开始执行应写入开始时间")
	}
}
