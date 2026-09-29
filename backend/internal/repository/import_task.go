package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
)

// ImportTaskRepo 导入任务数据访问。
type ImportTaskRepo struct {
	db *gorm.DB
}

func NewImportTaskRepo(db *gorm.DB) *ImportTaskRepo { return &ImportTaskRepo{db: db} }

func (r *ImportTaskRepo) Create(ctx context.Context, t *model.ImportTask) error {
	if err := conn(ctx, r.db).Create(t).Error; err != nil {
		return fmt.Errorf("创建导入任务失败: %w", err)
	}
	return nil
}

// Get 读取导入任务。
//
// 不存在时翻译成 404 领域错误：service 层不该为了判断"记录在不在"而 import gorm，
// 那会让"service 不感知 ORM"这条规矩形同虚设。
func (r *ImportTaskRepo) Get(ctx context.Context, id uint) (*model.ImportTask, error) {
	var t model.ImportTask
	if err := conn(ctx, r.db).First(&t, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperr.ErrNotFound.WithDetail("导入任务 %d 不存在", id)
		}
		return nil, fmt.Errorf("读取导入任务失败: %w", err)
	}
	return &t, nil
}

func (r *ImportTaskRepo) UpdateFields(ctx context.Context, id uint, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	return conn(ctx, r.db).Model(&model.ImportTask{}).Where("id = ?", id).Updates(fields).Error
}

// SwapStatus 带前置状态条件的更新（CAS）。
//
// 导入任务是并发对象：用户完全可能在两个标签页各点一次"取消"，
// 或者点取消的同时抽取刚好把结果写回来。先读后写会让第二次取消
// "成功"地覆盖掉第一次，状态机从此错乱，而且这种错乱不会报错，
// 只表现为历史里出现一条既不是失败也不是完成的可疑记录。
//
// 把"当前状态必须是什么"放进 WHERE，让数据库判胜负：
// 受影响行数为 0 就说明状态已经被别人改掉了，调用方据此返回 409。
func (r *ImportTaskRepo) SwapStatus(ctx context.Context, id uint, from []model.ImportTaskStatus, fields map[string]any) (bool, error) {
	if len(fields) == 0 {
		return false, nil
	}
	if len(from) == 0 {
		return false, fmt.Errorf("SwapStatus 必须给出前置状态，否则任何状态都会被覆盖")
	}
	res := conn(ctx, r.db).Model(&model.ImportTask{}).
		Where("id = ? AND status IN ?", id, from).
		Updates(fields)
	if res.Error != nil {
		return false, fmt.Errorf("更新导入任务状态失败: %w", res.Error)
	}
	return res.RowsAffected > 0, nil
}

// ClaimPending 原子地认领一个待处理任务。
//
// 返回 nil 表示当前没有可认领的任务。
//
// 认领必须是**一条带条件的 UPDATE**，不能是"先查出来再改状态"：
// 两个 worker 同时轮询时，后者会读到同一条任务并跑两遍——
// 一次导入要调模型、跑几分钟，重复执行等于白烧额度。
// 条件更新由数据库保证只有一个赢家（受影响行数为 1），这在
// MySQL 与 SQLite 上都成立，不需要额外的锁。
func (r *ImportTaskRepo) ClaimPending(ctx context.Context, now time.Time) (*model.ImportTask, error) {
	// 先按 id 升序取一个候选：FIFO，让等待最久的先跑，
	// 否则先到的任务可能被源源不断的新任务饿死。
	var candidates []model.ImportTask
	if err := conn(ctx, r.db).
		Where("status = ?", model.ImportStatusPending).
		Order("id ASC").
		Limit(8).
		Find(&candidates).Error; err != nil {
		return nil, fmt.Errorf("查找待处理任务失败: %w", err)
	}

	for i := range candidates {
		ok, err := r.SwapStatus(ctx, candidates[i].ID,
			[]model.ImportTaskStatus{model.ImportStatusPending},
			map[string]any{
				"status":     model.ImportStatusProcessing,
				"started_at": &now,
				// attempt 在认领时就 +1：万一进程在这里被 kill，
				// 任务的尝试次数已经记上了，不会被无限重试。
				"attempt": gorm.Expr("attempt + 1"),
			})
		if err != nil {
			return nil, err
		}
		if !ok {
			// 被别人抢先认领了，试下一个。
			continue
		}
		claimed := candidates[i]
		claimed.Status = model.ImportStatusProcessing
		claimed.StartedAt = &now
		claimed.Attempt++
		return &claimed, nil
	}
	return nil, nil
}

// RequeueStale 把长时间停在 processing 的任务退回 pending。
//
// 处理中状态由 worker 写下的那一行来维持。如果进程在写"处理中"之后、
// 写终态之前被强杀（OOM、断电、部署替换），这条任务会永远卡在
// processing：既不失败也不重试，队列就此少一个成员，且没有任何迹象。
// 因此启动时做一次回收。
func (r *ImportTaskRepo) RequeueStale(ctx context.Context, before time.Time) (int64, error) {
	res := conn(ctx, r.db).Model(&model.ImportTask{}).
		Where("status = ? AND started_at IS NOT NULL AND started_at < ?",
			model.ImportStatusProcessing, before).
		Updates(map[string]any{
			"status":     model.ImportStatusPending,
			"started_at": nil,
			"error_msg":  "上次执行被中断（进程退出或超时），已自动重新排队",
		})
	if res.Error != nil {
		return 0, fmt.Errorf("回收僵死任务失败: %w", res.Error)
	}
	return res.RowsAffected, nil
}

// Delete 彻底删除任务记录。
//
// 刻意**不**走软删除：导入历史本身不需要回收站保护（真正需要保护的是它产出的题目，
// 题目有独立的软删除与回收站），而一条"已删除"的幽灵行会让按状态统计与
// 队列位置出现无法解释的偏差。用户点"删除"要的就是这条记录真的不见。
func (r *ImportTaskRepo) Delete(ctx context.Context, id uint) error {
	return conn(ctx, r.db).Unscoped().Where("id = ?", id).Delete(&model.ImportTask{}).Error
}

// ListFilter 导入任务筛选。
type ImportTaskListFilter struct {
	SubjectID uint
	UserID    uint
	Status    model.ImportTaskStatus
}

// List 分页查询导入任务，按 id 倒序（最新在前）。
func (r *ImportTaskRepo) List(ctx context.Context, f ImportTaskListFilter, limit, offset int) ([]model.ImportTask, int64, error) {
	q := r.scoped(ctx, f)

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var out []model.ImportTask
	err := q.Order("id DESC").Limit(limit).Offset(offset).Find(&out).Error
	return out, total, err
}

// StatusCounts 按状态统计数量，供队列面板使用。
//
// 返回的 map 只包含真实出现过的状态；缺省状态由 service 补零，
// 界面因此总能读到固定的五个键，不必写 `counts["pending"] ?? 0` 之类的防御代码。
func (r *ImportTaskRepo) StatusCounts(ctx context.Context, f ImportTaskListFilter) (map[model.ImportTaskStatus]int64, error) {
	type row struct {
		Status model.ImportTaskStatus
		Total  int64
	}
	var rows []row
	err := r.scoped(ctx, f).
		Select("status, COUNT(*) AS total").
		Group("status").
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("统计导入任务失败: %w", err)
	}
	out := make(map[model.ImportTaskStatus]int64, len(rows))
	for _, row := range rows {
		out[row.Status] = row.Total
	}
	return out, nil
}

// ListActive 取出仍在活动状态（等待处理 / 处理中）的任务，用于计算队列位置。
//
// 排序键用 updated_at 而不是 created_at：重试会刷新 updated_at，
// 于是被重试的任务自然排到队尾（它是最"新"的一次排队），
// 而 created_at 会让它插到队头，看起来像"插队成功"。
//
// limit 是给界面预览用的，完整计数走 StatusCounts。
func (r *ImportTaskRepo) ListActive(ctx context.Context, f ImportTaskListFilter, limit int) ([]model.ImportTask, error) {
	var out []model.ImportTask
	err := r.scoped(ctx, f).
		Where("status IN ?", []model.ImportTaskStatus{model.ImportStatusPending, model.ImportStatusProcessing}).
		Order("updated_at ASC").
		Order("id ASC").
		Limit(limit).
		Find(&out).Error
	if err != nil {
		return nil, fmt.Errorf("读取活动导入任务失败: %w", err)
	}
	return out, nil
}

// scoped 构造带筛选条件的查询。List / StatusCounts / ListActive 共用，
// 避免出现"列表过滤了、统计没过滤"这种对不上账的偏差。
func (r *ImportTaskRepo) scoped(ctx context.Context, f ImportTaskListFilter) *gorm.DB {
	q := conn(ctx, r.db).Model(&model.ImportTask{})
	if f.SubjectID > 0 {
		q = q.Where("subject_id = ?", f.SubjectID)
	}
	if f.UserID > 0 {
		q = q.Where("user_id = ?", f.UserID)
	}
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	return q
}
