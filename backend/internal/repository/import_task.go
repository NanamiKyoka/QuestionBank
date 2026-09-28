package repository

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"github.com/NanamiKyoka/questionmind/internal/model"
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

func (r *ImportTaskRepo) Get(ctx context.Context, id uint) (*model.ImportTask, error) {
	var t model.ImportTask
	if err := conn(ctx, r.db).First(&t, id).Error; err != nil {
		return nil, err
	}
	return &t, nil
}

func (r *ImportTaskRepo) UpdateFields(ctx context.Context, id uint, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	return conn(ctx, r.db).Model(&model.ImportTask{}).Where("id = ?", id).Updates(fields).Error
}

// ListFilter 导入任务筛选。
type ImportTaskListFilter struct {
	SubjectID uint
	UserID    uint
	Status    model.ImportTaskStatus
}

func (r *ImportTaskRepo) List(ctx context.Context, f ImportTaskListFilter, limit, offset int) ([]model.ImportTask, int64, error) {
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

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var out []model.ImportTask
	err := q.Order("id DESC").Limit(limit).Offset(offset).Find(&out).Error
	return out, total, err
}
