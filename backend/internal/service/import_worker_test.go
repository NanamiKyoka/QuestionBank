package service

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/NanamiKyoka/questionmind/internal/importing"
	"github.com/NanamiKyoka/questionmind/internal/infra/storage"
	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/schema"
)

// 队列的意义只有一个：把"一次一个文件、同步等结果"变成"整批丢进去"。
// 下面这些用例盯的就是它真的做到了——不是建了个 pending 行就算数。

type queueFixture struct {
	*importFixture
	// svc 在这里**再声明一次**是有意的：外层字段遮蔽内嵌结构体的同名字段。
	// 队列用的 service 额外注入了 storage、uploadDir 与 worker，
	// 用内嵌那个原始的 fx.svc 会得到"未配置存储"这种莫名其妙的失败。
	svc    *ImportService
	worker *ImportWorker

	// owner / other 是**真实落库**的用户。
	//
	// importFixture 里的 admin/editor 只是内存对象，没有用户行——
	// 那是够用的，因为常规 service 调用只读 user.Can()。
	// 但 worker 必须按 id 把用户捞出来（权限要看成员关系），
	// 所以这里必须造真的用户，否则测试会卡在一个和被测逻辑无关的地方。
	owner *model.User
	other *model.User
}

func newQueueFixture(t *testing.T) *queueFixture {
	t.Helper()
	base := newImportFixture(t, realisticExtraction)
	// 排队任务必须能从磁盘读回内容，所以这里给注入一个真实的存储。
	store := storage.NewLocal(filepath.Join(t.TempDir(), "media"), "/media")

	svc := NewImportService(base.repos, nil, base.svc.questions).
		WithStorage(store).
		WithUploadDir(t.TempDir())
	w := NewImportWorker(svc)
	svc = svc.WithWorker(w)

	ctx := context.Background()
	newUser := func(username string) *model.User {
		u := &model.User{Username: username, IsActive: true}
		if err := base.repos.Users.Create(ctx, u); err != nil {
			t.Fatalf("建用户 %s 失败: %v", username, err)
		}
		return u
	}
	owner := newUser("queue-owner")
	other := newUser("queue-other")
	for _, u := range []*model.User{owner, other} {
		if err := base.repos.Subjects.AddMember(ctx, base.subject.ID, u.ID, model.SubjectRoleEditor); err != nil {
			t.Fatalf("加成员失败: %v", err)
		}
	}
	// Create 返回的对象不带成员关系，而 user.Can() 读的是内存里的 Memberships。
	// 补一次带成员的读取，否则权限判定一律为假，症状是"入队就被拒"。
	owner, err := base.repos.Users.GetWithMemberships(ctx, owner.ID)
	if err != nil {
		t.Fatalf("读取 owner 失败: %v", err)
	}
	other, err = base.repos.Users.GetWithMemberships(ctx, other.ID)
	if err != nil {
		t.Fatalf("读取 other 失败: %v", err)
	}

	return &queueFixture{importFixture: base, svc: svc, worker: w, owner: owner, other: other}
}

func (f *queueFixture) waitFor(t *testing.T, taskID uint, want model.ImportTaskStatus) *model.ImportTask {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		task, err := f.repos.ImportTasks.Get(context.Background(), taskID)
		if err == nil && task.Status == want {
			return task
		}
		time.Sleep(20 * time.Millisecond)
	}
	task, _ := f.repos.ImportTasks.Get(context.Background(), taskID)
	t.Fatalf("任务未在预期时间内变成 %s，实际 %s", want, task.Status)
	return nil
}

const queuePaper = "1.【题目】下列说法正确的是（　）\nA. 甲\nB. 乙\n【答案】A\n"

// 一个文件进队列、被 worker 认领、跑完并把草稿存下来。
func TestWorkerProcessesQueuedFile(t *testing.T) {
	fx := newQueueFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fx.worker.Start(ctx)
	defer fx.worker.Stop()

	task, err := fx.svc.EnqueueFile(ctx, fx.owner, fx.subject.ID, "随堂卷.md",
		[]byte(queuePaper), string(importing.MethodTemplate), string(importing.ModeExtract))
	if err != nil {
		t.Fatalf("入队失败: %v", err)
	}
	if task.Status != model.ImportStatusPending {
		t.Fatalf("刚入队的任务应是 pending，实际 %s", task.Status)
	}

	done := fx.waitFor(t, task.ID, model.ImportStatusCompleted)

	// 草稿必须落库：worker 跑的时候 HTTP 响应早就返回了。
	if done.TotalCount != 1 {
		t.Fatalf("任务应记录抽出 1 题，实际 %d", done.TotalCount)
	}
	res, err := fx.svc.TaskDrafts(ctx, fx.owner, fx.subject.ID, task.ID)
	if err != nil {
		t.Fatalf("取回草稿失败: %v", err)
	}
	if len(res.Drafts) != 1 {
		t.Fatalf("应取回 1 道草稿，实际 %d", len(res.Drafts))
	}
	if got := res.Drafts[0].Answer.Correct; len(got) != 1 || got[0] != "A" {
		t.Fatalf("草稿答案不对: %+v", res.Drafts[0].Answer)
	}
	if res.Drafts[0].SourceFile != "随堂卷.md" {
		t.Fatalf("草稿应保留来源文件名，实际 %q", res.Drafts[0].SourceFile)
	}
}

// 坏文件必须变成"失败 + 可读原因"，而不是让 worker 静默退出。
func TestWorkerMarksUnparsableFileFailed(t *testing.T) {
	fx := newQueueFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fx.worker.Start(ctx)
	defer fx.worker.Stop()

	task, err := fx.svc.EnqueueFile(ctx, fx.owner, fx.subject.ID, "空的.md", []byte("   \n"),
		string(importing.MethodTemplate), "")
	if err != nil {
		t.Fatalf("入队失败: %v", err)
	}
	failed := fx.waitFor(t, task.ID, model.ImportStatusFailed)
	if failed.ErrorMsg == "" {
		t.Fatal("失败的任务必须留下可读原因，否则用户只知道'失败了'")
	}
}

// 认领必须互斥：两个 worker 同时轮询时，同一个任务不能被跑两遍。
// 一次导入要调模型跑几分钟，重复执行等于白烧额度。
func TestClaimPendingIsExclusive(t *testing.T) {
	fx := newQueueFixture(t)
	ctx := context.Background()

	first := &model.ImportTask{
		SubjectID: &fx.subject.ID, UserID: fx.owner.ID,
		Source: model.ImportSourceUpload, Status: model.ImportStatusPending,
		OriginalFilename: "x.md", FileType: "markdown", Mode: "extract",
	}
	if err := fx.repos.ImportTasks.Create(ctx, first); err != nil {
		t.Fatalf("建任务失败: %v", err)
	}
	second := &model.ImportTask{
		SubjectID: &fx.subject.ID, UserID: fx.owner.ID,
		Source: model.ImportSourceUpload, Status: model.ImportStatusPending,
		OriginalFilename: "y.md", FileType: "markdown", Mode: "extract",
	}
	if err := fx.repos.ImportTasks.Create(ctx, second); err != nil {
		t.Fatalf("建任务失败: %v", err)
	}

	seen := map[uint]bool{}
	for i := 0; i < 2; i++ {
		got, err := fx.repos.ImportTasks.ClaimPending(ctx, time.Now())
		if err != nil {
			t.Fatalf("认领失败: %v", err)
		}
		if got == nil {
			break
		}
		if seen[got.ID] {
			t.Fatalf("任务 %d 被认领了两次", got.ID)
		}
		seen[got.ID] = true
		if got.Attempt != 1 {
			t.Fatalf("认领时 attempt 应加一，实际 %d", got.Attempt)
		}
	}
	if len(seen) != 2 || !seen[first.ID] || !seen[second.ID] {
		t.Fatalf("两个任务都应被认领一次，实际 %v", seen)
	}

	// 队列空了：认领必须返回 nil 而不是报错。
	got, err := fx.repos.ImportTasks.ClaimPending(ctx, time.Now())
	if err != nil {
		t.Fatalf("空队列认领不应报错: %v", err)
	}
	if got != nil {
		t.Fatalf("空队列时不应认领到任务，实际 %+v", got)
	}
}

// 进程在"处理中"被杀，任务会永远卡住。启动时必须回收，
// 否则队列就此少一个人且没有任何迹象。
func TestRequeueStaleUnsticksInterruptedTasks(t *testing.T) {
	fx := newQueueFixture(t)
	ctx := context.Background()

	stale := &model.ImportTask{
		SubjectID: &fx.subject.ID, UserID: fx.owner.ID,
		Source: model.ImportSourceUpload, Status: model.ImportStatusProcessing,
		OriginalFilename: "stuck.md", FileType: "markdown",
	}
	if err := fx.repos.ImportTasks.Create(ctx, stale); err != nil {
		t.Fatalf("建任务失败: %v", err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := fx.repos.ImportTasks.UpdateFields(ctx, stale.ID, map[string]any{"started_at": &old}); err != nil {
		t.Fatalf("设置旧时间失败: %v", err)
	}

	// 另一个刚处理中的任务不该被动。
	fresh := &model.ImportTask{
		SubjectID: &fx.subject.ID, UserID: fx.owner.ID,
		Source: model.ImportSourceUpload, Status: model.ImportStatusProcessing,
		OriginalFilename: "running.md", FileType: "markdown",
	}
	if err := fx.repos.ImportTasks.Create(ctx, fresh); err != nil {
		t.Fatalf("建任务失败: %v", err)
	}
	now := time.Now()
	fx.repos.ImportTasks.UpdateFields(ctx, fresh.ID, map[string]any{"started_at": &now})

	n, err := fx.repos.ImportTasks.RequeueStale(ctx, time.Now().Add(-30*time.Minute))
	if err != nil {
		t.Fatalf("回收失败: %v", err)
	}
	if n != 1 {
		t.Fatalf("应只回收 1 个僵死任务，实际 %d", n)
	}
	if got, _ := fx.repos.ImportTasks.Get(ctx, stale.ID); got.Status != model.ImportStatusPending {
		t.Fatalf("僵死任务应回到 pending，实际 %s", got.Status)
	}
	if got, _ := fx.repos.ImportTasks.Get(ctx, fresh.ID); got.Status != model.ImportStatusProcessing {
		t.Fatalf("正在处理的任务不该被动，实际 %s", got.Status)
	}
}

// 队列状态必须如实反映 worker 在不在跑。
// 以前这里恒为 false，界面如实地说"不会自动开始"；接上 worker 后必须跟着变。
func TestQueueStatusReflectsWorkerOnline(t *testing.T) {
	fx := newQueueFixture(t)
	ctx := context.Background()

	res, err := fx.svc.QueueStatus(ctx, fx.owner, fx.subject.ID)
	if err != nil {
		t.Fatalf("读取队列状态失败: %v", err)
	}
	if res.WorkerOnline {
		t.Fatal("worker 还没启动，不该报告在线")
	}

	fx.worker.Start(ctx)
	defer fx.worker.Stop()

	res, err = fx.svc.QueueStatus(ctx, fx.owner, fx.subject.ID)
	if err != nil {
		t.Fatalf("读取队列状态失败: %v", err)
	}
	if !res.WorkerOnline {
		t.Fatal("worker 已启动，应报告在线")
	}
	if res.Hint == "" {
		t.Fatal("队列面板必须给一句人话提示")
	}
}

// 任务记录里带着用户身份（谁导的、报的什么错），不能被别人读走。
func TestTaskDraftsAreScopedToOwner(t *testing.T) {
	fx := newQueueFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fx.worker.Start(ctx)
	defer fx.worker.Stop()

	task, err := fx.svc.EnqueueFile(ctx, fx.owner, fx.subject.ID, "a.md",
		[]byte(queuePaper), string(importing.MethodTemplate), "")
	if err != nil {
		t.Fatalf("入队失败: %v", err)
	}
	fx.waitFor(t, task.ID, model.ImportStatusCompleted)

	if _, err := fx.svc.TaskDrafts(ctx, fx.owner, fx.subject.ID, task.ID); err != nil {
		t.Fatalf("本人应能读自己的草稿: %v", err)
	}
	if _, err := fx.svc.TaskDrafts(ctx, fx.other, fx.subject.ID, task.ID); err == nil {
		t.Fatal("他人不应读到别人的导入草稿")
	}
}

// 未完成的任务不该返回草稿：用户点开一个还在排队的任务，
// 给他一份空列表会以为"这卷子一道题都没有"。
func TestTaskDraftsRejectsUnfinishedTask(t *testing.T) {
	fx := newQueueFixture(t)
	ctx := context.Background()

	task, err := fx.svc.EnqueueFile(ctx, fx.owner, fx.subject.ID, "b.md",
		[]byte(queuePaper), string(importing.MethodTemplate), "")
	if err != nil {
		t.Fatalf("入队失败: %v", err)
	}
	if _, err := fx.svc.TaskDrafts(ctx, fx.owner, fx.subject.ID, task.ID); err == nil {
		t.Fatal("未完成的任务不应返回草稿")
	}
}

// 不支持的类型必须在入队前就拒，而不是让 worker 跑完才发现。
func TestEnqueueRejectsUnsupportedAndOversized(t *testing.T) {
	fx := newQueueFixture(t)
	ctx := context.Background()

	if _, err := fx.svc.EnqueueFile(ctx, fx.owner, fx.subject.ID, "a.doc", []byte("\xd0\xcf"), "ai", ""); err == nil {
		t.Fatal(".doc 不受支持，必须在入队时拒绝")
	}
	tooBig := bytes.Repeat([]byte("x"), importing.MaxDocFileBytes+1)
	if _, err := fx.svc.EnqueueFile(ctx, fx.owner, fx.subject.ID, "a.md", tooBig, "ai", ""); err == nil {
		t.Fatal("超大文件必须在入队时拒绝")
	}
}

// PDF 现在受支持了，队列也必须照样收下——
// 队列入队时不解析内容，真正解析在 worker 手里，所以这里只校验扩展名。
func TestEnqueueAcceptsPDF(t *testing.T) {
	fx := newQueueFixture(t)
	if _, err := fx.svc.EnqueueFile(context.Background(), fx.owner, fx.subject.ID,
		"paper.pdf", []byte("%PDF-1.7"), "ai", ""); err != nil {
		t.Fatalf("PDF 应当可以入队: %v", err)
	}
}

// 确认端点契约没被改坏：响应必须是标准信封。
func TestExtractResponseStillCarriesMethodForQueuedTasks(t *testing.T) {
	// 排队任务存的 Result 必须能被读回成 ExtractResponse：
	// 这是审核界面唯一的数据来源，形状变了界面就空了。
	res := &schema.ExtractResponse{
		TaskID:      7,
		Drafts:      []importing.Draft{{SourceFile: "a.md"}},
		Method:      string(importing.MethodTemplate),
		SourceFiles: []string{"a.md"},
	}
	raw, err := model.JSONOf(res)
	if err != nil {
		t.Fatalf("编码失败: %v", err)
	}
	var back schema.ExtractResponse
	if err := raw.Into(&back); err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	if back.Method != res.Method || len(back.Drafts) != 1 || back.Drafts[0].SourceFile != "a.md" {
		t.Fatalf("往返后不一致：%+v", back)
	}
}
