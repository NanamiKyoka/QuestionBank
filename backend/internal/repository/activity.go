package repository

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/NanamiKyoka/questionmind/internal/model"
)

// ActivityRepo 审计日志数据访问。
type ActivityRepo struct {
	db *gorm.DB
}

func NewActivityRepo(db *gorm.DB) *ActivityRepo { return &ActivityRepo{db: db} }

// Create 写入一条审计记录。
func (r *ActivityRepo) Create(ctx context.Context, log *model.ActivityLog) error {
	return conn(ctx, r.db).Create(log).Error
}

// ActivityFilter 审计日志筛选。
type ActivityFilter struct {
	UserID   *uint
	Action   string
	Resource string
	// Keyword 匹配用户名或路径。
	Keyword string
	// Success 为 nil 表示不按结果过滤。
	Success *bool
	Since   *time.Time
	Until   *time.Time
}

func (r *ActivityRepo) List(ctx context.Context, f ActivityFilter, limit, offset int) ([]model.ActivityLog, int64, error) {
	q := conn(ctx, r.db).Model(&model.ActivityLog{})

	if f.UserID != nil {
		q = q.Where("user_id = ?", *f.UserID)
	}
	if f.Action != "" {
		q = q.Where("action = ?", f.Action)
	}
	if f.Resource != "" {
		q = q.Where("resource = ?", f.Resource)
	}
	if f.Keyword != "" {
		like := "%" + escapeLike(f.Keyword) + "%"
		q = q.Where("username LIKE ? OR path LIKE ?", like, like)
	}
	if f.Success != nil {
		q = q.Where("success = ?", *f.Success)
	}
	if f.Since != nil {
		q = q.Where("created_at >= ?", *f.Since)
	}
	if f.Until != nil {
		q = q.Where("created_at <= ?", *f.Until)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var out []model.ActivityLog
	err := q.Order("id DESC").Limit(limit).Offset(offset).Find(&out).Error
	return out, total, err
}

// DistinctActions 返回出现过的动作名，供筛选下拉框使用。
func (r *ActivityRepo) DistinctActions(ctx context.Context, limit int) ([]string, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var actions []string
	err := conn(ctx, r.db).Model(&model.ActivityLog{}).
		Distinct("action").
		Order("action ASC").
		Limit(limit).
		Pluck("action", &actions).Error
	return actions, err
}
