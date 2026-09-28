package repository

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
)

// AIProviderRepo AI 供应商数据访问。
type AIProviderRepo struct {
	db *gorm.DB
}

func NewAIProviderRepo(db *gorm.DB) *AIProviderRepo { return &AIProviderRepo{db: db} }

// List 返回全部供应商，附带模型列表。
func (r *AIProviderRepo) List(ctx context.Context) ([]model.AIProvider, error) {
	var out []model.AIProvider
	err := conn(ctx, r.db).
		Preload("Models", func(db *gorm.DB) *gorm.DB {
			return db.Order("sort_order ASC, id ASC")
		}).
		Order("sort_order ASC, id ASC").
		Find(&out).Error
	return out, err
}

func (r *AIProviderRepo) Get(ctx context.Context, id uint) (*model.AIProvider, error) {
	var p model.AIProvider
	if err := conn(ctx, r.db).First(&p, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperr.ErrNotFound.WithDetail("AI 供应商 %d 不存在", id)
		}
		return nil, err
	}
	return &p, nil
}

func (r *AIProviderRepo) Create(ctx context.Context, p *model.AIProvider) error {
	if err := conn(ctx, r.db).Create(p).Error; err != nil {
		return fmt.Errorf("创建 AI 供应商失败: %w", err)
	}
	return nil
}

func (r *AIProviderRepo) UpdateFields(ctx context.Context, id uint, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	return conn(ctx, r.db).Model(&model.AIProvider{}).Where("id = ?", id).Updates(fields).Error
}

func (r *AIProviderRepo) SoftDelete(ctx context.Context, id uint) error {
	return conn(ctx, r.db).Delete(&model.AIProvider{}, id).Error
}

func (r *AIProviderRepo) ExistsName(ctx context.Context, name string, excludeID uint) (bool, error) {
	q := conn(ctx, r.db).Model(&model.AIProvider{}).Where("name = ?", name)
	if excludeID > 0 {
		q = q.Where("id <> ?", excludeID)
	}
	var n int64
	if err := q.Count(&n).Error; err != nil {
		return false, err
	}
	return n > 0, nil
}

// CountModels 统计供应商下的模型数量，删除前用于提示。
func (r *AIProviderRepo) CountModels(ctx context.Context, providerID uint) (int64, error) {
	var n int64
	err := conn(ctx, r.db).Model(&model.AIModel{}).Where("provider_id = ?", providerID).Count(&n).Error
	return n, err
}

// SoftDeleteModels 删除供应商时一并删除其模型。
func (r *AIProviderRepo) SoftDeleteModels(ctx context.Context, providerID uint) error {
	return conn(ctx, r.db).Where("provider_id = ?", providerID).Delete(&model.AIModel{}).Error
}

// AIModelRepo AI 模型数据访问。
type AIModelRepo struct {
	db *gorm.DB
}

func NewAIModelRepo(db *gorm.DB) *AIModelRepo { return &AIModelRepo{db: db} }

// ListByType 按用途筛选启用的模型，用于解析"当前生效的模型"。
func (r *AIModelRepo) ListByType(ctx context.Context, modelType model.AIModelType) ([]model.AIModel, error) {
	var out []model.AIModel
	err := conn(ctx, r.db).
		Preload("Provider").
		Where("model_type = ? AND is_active = ?", modelType, true).
		Order("sort_order ASC, id ASC").
		Find(&out).Error
	return out, err
}

// ListByProvider 返回某供应商下的全部模型。
func (r *AIModelRepo) ListByProvider(ctx context.Context, providerID uint) ([]model.AIModel, error) {
	var out []model.AIModel
	err := conn(ctx, r.db).
		Where("provider_id = ?", providerID).
		Order("sort_order ASC, id ASC").
		Find(&out).Error
	return out, err
}

func (r *AIModelRepo) Get(ctx context.Context, id uint) (*model.AIModel, error) {
	var m model.AIModel
	if err := conn(ctx, r.db).Preload("Provider").First(&m, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperr.ErrNotFound.WithDetail("AI 模型 %d 不存在", id)
		}
		return nil, err
	}
	return &m, nil
}

func (r *AIModelRepo) Create(ctx context.Context, m *model.AIModel) error {
	if err := conn(ctx, r.db).Create(m).Error; err != nil {
		return fmt.Errorf("创建 AI 模型失败: %w", err)
	}
	return nil
}

func (r *AIModelRepo) UpdateFields(ctx context.Context, id uint, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	return conn(ctx, r.db).Model(&model.AIModel{}).Where("id = ?", id).Updates(fields).Error
}

func (r *AIModelRepo) SoftDelete(ctx context.Context, id uint) error {
	return conn(ctx, r.db).Delete(&model.AIModel{}, id).Error
}

func (r *AIModelRepo) ExistsName(ctx context.Context, providerID uint, name string, excludeID uint) (bool, error) {
	q := conn(ctx, r.db).Model(&model.AIModel{}).
		Where("provider_id = ? AND name = ?", providerID, name)
	if excludeID > 0 {
		q = q.Where("id <> ?", excludeID)
	}
	var n int64
	if err := q.Count(&n).Error; err != nil {
		return false, err
	}
	return n > 0, nil
}

// CustomModelNames 题目自定义的模型名校验：同一供应商下不允许重名。
func (r *AIModelRepo) CountByType(ctx context.Context, modelType model.AIModelType) (int64, error) {
	var n int64
	err := conn(ctx, r.db).Model(&model.AIModel{}).
		Where("model_type = ?", modelType).Count(&n).Error
	return n, err
}
