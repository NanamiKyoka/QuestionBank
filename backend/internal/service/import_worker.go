package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/NanamiKyoka/questionmind/internal/importing"
	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
	"github.com/NanamiKyoka/questionmind/internal/schema"
)

// 导入队列的调参。
const (
	// importPollInterval 空队列时的轮询间隔。
	//
	// 不做"有任务时事件唤醒"：导入是分钟级的低频操作，
	// 一秒一次的空转相对于一次模型调用的开销可以忽略，
	// 而省掉一套唤醒机制换来的是少一类并发 bug。
	importPollInterval = time.Second

	// importConcurrency 同时处理的任务数。
	//
	// 默认 1：一次导入就是一次模型调用，并发跑只会让每个都更慢，
	// 还可能撞上供应商的速率限制。需要时按自己的额度调。
	importConcurrency = 1

	// importStaleAfter 处理中状态超过这么久仍未完成，视为僵死。
	// 与单次抽取的超时预算（3 分钟）对齐：超过它说明任务不会再推进了。
	importStaleAfter = 30 * time.Minute
)

// ImportWorker 后台导入队列消费者。
//
// 存在的理由：一次只能传一个文件、同步跑完才返回，意味着
// "把一整个文件夹的卷子导进去"这件事没法做——20 个文件就是 20 次
// 串行等待，而且中途断网就全白跑。排队之后，用户提交完就能去干别的。
type ImportWorker struct {
	svc *ImportService

	stop chan struct{}
	wg   sync.WaitGroup

	// online 供 QueueStatus 回显。界面据此决定要不要显示进度动画。
	online atomicBool
}

// atomicBool 只有一个写者与若干读者，用 bool 会被 race detector 报出来。
type atomicBool struct {
	mu sync.RWMutex
	v  bool
}

func (b *atomicBool) set(v bool) { b.mu.Lock(); b.v = v; b.mu.Unlock() }
func (b *atomicBool) get() bool  { b.mu.RLock(); defer b.mu.RUnlock(); return b.v }

// NewImportWorker 构造 worker（尚未启动）。
func NewImportWorker(svc *ImportService) *ImportWorker {
	return &ImportWorker{svc: svc, stop: make(chan struct{})}
}

// Start 启动 worker，并在启动时回收一次僵死任务。
func (w *ImportWorker) Start(ctx context.Context) {
	// 上一进程如果在"处理中"被杀，任务会永远卡住；启动时先拉回队列。
	if n, err := w.svc.repos.ImportTasks.RequeueStale(ctx, time.Now().Add(-importStaleAfter)); err != nil {
		slog.Warn("回收僵死导入任务失败", "error", err)
	} else if n > 0 {
		slog.Info("已把中断的导入任务重新排队", "count", n)
	}

	w.online.set(true)
	for i := 0; i < importConcurrency; i++ {
		w.wg.Add(1)
		go w.loop(ctx)
	}
	slog.Info("导入队列已启动", "concurrency", importConcurrency)
}

// Stop 停止 worker 并等待在途任务结束。
func (w *ImportWorker) Stop() {
	w.online.set(false)
	close(w.stop)
	w.wg.Wait()
	slog.Info("导入队列已停止")
}

// Online worker 是否在运行。
func (w *ImportWorker) Online() bool { return w.online.get() }

func (w *ImportWorker) loop(ctx context.Context) {
	defer w.wg.Done()
	ticker := time.NewTicker(importPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stop:
			return
		case <-ticker.C:
		}

		// 一轮只取一个：取完立刻处理，处理完再取下一个。
		// 这样并发度天然等于 worker 个数，不需要额外的信号量。
		task, err := w.svc.repos.ImportTasks.ClaimPending(ctx, time.Now())
		if err != nil {
			slog.Warn("认领导入任务失败", "error", err)
			continue
		}
		if task == nil {
			continue
		}
		w.process(ctx, task)
	}
}

// process 执行一个任务。
//
// 三条纪律：
//  1. 一定要写终态。任务停在中途等于队列少一个人且无人知晓，
//     所以连"处理函数自己 panic"都要兜住。
//  2. 用户随时可以放弃任务（cancelled）。放弃是**放弃等待**，
//     不是打断已经发出去的模型请求——打断不了，也假装不了。
//  3. 结果要落库。草稿存在 task.Result 里，审核界面随后按任务 id 取；
//     不落库的话 worker 抽出来的题就永远没人看得见。
func (w *ImportWorker) process(ctx context.Context, task *model.ImportTask) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("导入任务处理时 panic", "task_id", task.ID, "panic", r)
			_ = w.svc.failTask(ctx, task.ID, "服务内部错误，任务已终止")
		}
	}()

	data, err := w.readSource(task)
	if err != nil {
		_ = w.svc.failTask(ctx, task.ID, err.Error())
		return
	}

	// 以**真实的任务所有者**身份执行，而不是一个只有 id 的空壳 User：
	// 权限判定要看学科成员关系，伪造的用户会被 authorize 拒掉，
	// 表现为每个任务都"失败"，而真实原因（用户已被移出学科）被掩盖。
	owner, err := w.svc.repos.Users.GetWithMemberships(ctx, task.UserID)
	if err != nil {
		_ = w.svc.failTask(ctx, task.ID, "无法读取任务所属用户（可能已被删除）")
		return
	}
	if !owner.IsActive {
		_ = w.svc.failTask(ctx, task.ID, "任务所属用户已被停用")
		return
	}

	res, err := w.svc.Upload(ctx, owner,
		derefUint(task.SubjectID), task.OriginalFilename, data,
		methodOfTask(task), task.Mode)
	if err != nil {
		_ = w.svc.failTask(ctx, task.ID, err.Error())
		return
	}
	_ = w.svc.completeTaskWithDrafts(ctx, task.ID, res)
}

// readSource 读回任务对应的原始内容。
//
// 队列任务的内容来自上传时落盘的文件；读不到就是文件被清掉了，
// 此时必须明确失败，而不是悄悄按空内容处理。
func (w *ImportWorker) readSource(task *model.ImportTask) ([]byte, error) {
	if task.FilePath == "" {
		return nil, apperr.New(apperr.CodeUnprocessable, "任务没有可用的原始文件，无法重跑")
	}
	data, err := os.ReadFile(task.FilePath)
	if err != nil {
		return nil, apperr.New(apperr.CodeUnprocessable, "原始文件已不存在，可能被清理过")
	}
	return data, nil
}

// methodOfTask 读回任务创建时选的解析方式。
//
// 没有单独的列，就从文件名后缀之外的约定里取——
// 保守起见解析不出时按 ai 处理：与手工提交的行为一致，
// 而 template 路径即使判断错了也只是少解析几道题。
func methodOfTask(task *model.ImportTask) string {
	if strings.HasPrefix(task.ResultString("method"), string(importing.MethodTemplate)) {
		return string(importing.MethodTemplate)
	}
	return string(importing.MethodAI)
}

func derefUint(p *uint) uint {
	if p == nil {
		return 0
	}
	return *p
}

// ---- service 侧的任务写入 ----

// EnqueueFile 把一个上传文件登记为排队中的任务。
//
// 与 Upload 的区别：Upload 同步跑完并把草稿直接返回，
// Enqueue 只建任务立刻返回。批量场景（一次传整个文件夹）用后者。
func (s *ImportService) EnqueueFile(ctx context.Context, user *model.User, subjectID uint,
	filename string, data []byte, method, mode string) (*schema.ImportTaskResponse, error) {
	name := sanitizeUploadName(filename)
	if name == "" {
		return nil, apperr.New(apperr.CodeInvalidParam, "缺少文件名")
	}
	if !importing.UploadSupported(name) {
		return nil, apperr.New(apperr.CodeInvalidParam, "暂不支持这种文件").
			WithDetail("filename=%s；目前支持 %s", name, importing.UploadExtHint())
	}
	if err := authorize(user, model.PermEditQuestion, subjectID); err != nil {
		return nil, err
	}
	if len(data) > importing.MaxDocFileBytes {
		return nil, apperr.New(apperr.CodeInvalidParam, "文件过大").
			WithDetail("单个文件上限 %d MB", importing.MaxDocFileBytes>>20)
	}

	// 先落盘：worker 是另一个 goroutine（甚至可能是另一个进程重启后），
	// 它只能通过文件拿到内容，存进库里不合适——题目正文动辄几十万字。
	path, err := s.saveUploadFile(ctx, name, data)
	if err != nil {
		return nil, err
	}

	task := &model.ImportTask{
		SubjectID:        &subjectID,
		UserID:           user.ID,
		Source:           model.ImportSourceUpload,
		Status:           model.ImportStatusPending,
		OriginalFilename: name,
		FilePath:         path,
		FileType:         fileTypeOfName(name),
		Mode:             mode,
	}
	// 解析方式没有独立的列，先记在 Result 里，worker 认领时读回。
	// 这样将来加列要走迁移（基线之后模型变更必须写迁移），
	// 而现在不改表结构也能把队列跑通。
	if res, err := model.JSONOf(map[string]any{"method": method}); err == nil {
		task.Result = res
	}
	if err := s.repos.ImportTasks.Create(ctx, task); err != nil {
		// 建行失败就把刚落盘的文件清掉，否则孤儿文件会一直堆积。
		_ = s.removeUploadFile(path)
		return nil, apperr.ErrInternal.WithCause(err)
	}
	resp := schema.NewImportTaskResponse(task)
	return &resp, nil
}

// TaskDrafts 读回某个任务抽取出的草稿。
//
// 队列任务的草稿存在任务记录里（Result 字段）：worker 跑完之后
// 用户才来取，而 HTTP 响应早就返回了。
func (s *ImportService) TaskDrafts(ctx context.Context, user *model.User, subjectID, taskID uint) (*schema.ExtractResponse, error) {
	task, err := s.loadTask(ctx, subjectID, taskID)
	if err != nil {
		return nil, err
	}
	if err := s.authorizeTaskRead(user, task); err != nil {
		return nil, err
	}
	if task.Status != model.ImportStatusCompleted {
		return nil, apperr.New(apperr.CodeUnprocessable,
			"任务尚未完成，暂时没有可审核的草稿").WithDetail("status=%s", task.Status)
	}
	// Result 里现在存的是整份 ExtractResponse。
	if task.Result.IsEmpty() {
		return nil, apperr.New(apperr.CodeUnprocessable, "该任务没有保存草稿")
	}
	var stored schema.ExtractResponse
	if err := task.Result.Into(&stored); err != nil {
		return nil, apperr.New(apperr.CodeUnprocessable, "该任务的草稿无法读取")
	}
	warnings := 0
	for i := range stored.Drafts {
		if len(stored.Drafts[i].Warnings) > 0 {
			warnings++
		}
	}
	stored.TaskID = task.ID
	stored.WarningCount = warnings
	stored.Total = len(stored.Drafts)
	return &stored, nil
}

// authorizeTaskRead 判断当前用户能否读取这条任务。
//
// 任务记录里带着用户身份（谁导的、报的什么错），
// 与题库可见性同理：本人可见，管理员可见全部。
func (s *ImportService) authorizeTaskRead(user *model.User, task *model.ImportTask) error {
	if user.IsSuperuser {
		return nil
	}
	if task.UserID == user.ID {
		return nil
	}
	return apperr.ErrForbidden
}

// failTask 把任务标记为失败，并保留一个能看懂的��由。
func (s *ImportService) failTask(ctx context.Context, id uint, reason string) error {
	now := time.Now()
	ok, err := s.repos.ImportTasks.SwapStatus(ctx, id,
		[]model.ImportTaskStatus{model.ImportStatusPending, model.ImportStatusProcessing},
		map[string]any{
			"status":      model.ImportStatusFailed,
			"error_msg":   reason,
			"finished_at": &now,
		})
	if err != nil {
		return err
	}
	if !ok {
		slog.Info("任务已是终态，未覆盖", "task_id", id)
	}
	return nil
}

// completeTaskWithDrafts 写回抽取结果。
func (s *ImportService) completeTaskWithDrafts(ctx context.Context, id uint, res *schema.ExtractResponse) error {
	now := time.Now()
	saved, err := model.JSONOf(res)
	if err != nil {
		return apperr.ErrInternal.WithCause(err)
	}
	_, err = s.repos.ImportTasks.SwapStatus(ctx, id,
		[]model.ImportTaskStatus{model.ImportStatusProcessing},
		map[string]any{
			"status":      model.ImportStatusCompleted,
			"result":      saved,
			"total_count": res.Total,
			"finished_at": &now,
		})
	return err
}

// fileTypeOfName 由扩展名推断文件类型，落进 task.file_type 便于历史页展示。
func fileTypeOfName(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".docx":
		return "docx"
	case ".pdf":
		return "pdf"
	case ".zip":
		return "zip"
	case ".md", ".markdown":
		return "markdown"
	default:
		return "text"
	}
}

// saveUploadFile 落盘一个排队任务要用的原始文件，返回可读回的路径。
//
// 刻意**不走媒体存储**：它会按图片嗅探校验内容，.md/.docx 必然被拒；
// 而且媒体目录是通过 /media 对外静态服务的——原始试卷常常是尚未公开的
// 教研材料，放进去等于顺手发布。uploads 目录只在本机。
//
// 文件名用随机串而不是用户上传的名字：原始名可能带路径分隔符与控制字符，
// 而且任务记录里已经存了 OriginalFilename，不需要靠文件名还原。
func (s *ImportService) saveUploadFile(ctx context.Context, name string, data []byte) (string, error) {
	if strings.TrimSpace(s.uploadDir) == "" {
		return "", apperr.New(apperr.CodeInternal, "服务未配置上传目录，队列导入不可用")
	}
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", apperr.ErrInternal.WithCause(err)
	}
	// 保留扩展名：摄取层按扩展名决定解析方式。
	ext := strings.ToLower(filepath.Ext(name))
	if ext == "" {
		ext = ".bin"
	}
	full := filepath.Join(s.uploadDir, hex.EncodeToString(buf)+ext)
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		return "", apperr.ErrInternal.WithCause(err)
	}
	// 0600：未公开的教研材料，不该被同机其它用户读到。
	if err := os.WriteFile(full, data, 0o600); err != nil {
		return "", apperr.ErrInternal.WithCause(err)
	}
	return full, nil
}

// removeUploadFile 清掉落盘文件。
//
// 删不掉只记警告：磁盘残留由运维处理，不该让"建任务"整体失败。
func (s *ImportService) removeUploadFile(path string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		slog.Warn("删除导入原始文件失败", "path", path, "error", err)
	}
	return nil
}
