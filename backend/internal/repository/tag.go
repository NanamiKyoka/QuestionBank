package repository

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
)

// TagCategoryRepo 标签分类数据访问。
type TagCategoryRepo struct {
	db *gorm.DB
}

func NewTagCategoryRepo(db *gorm.DB) *TagCategoryRepo {
	return &TagCategoryRepo{db: db}
}

func (r *TagCategoryRepo) ListBySubject(ctx context.Context, subjectID uint) ([]model.TagCategory, error) {
	var out []model.TagCategory
	err := conn(ctx, r.db).
		Where("subject_id = ?", subjectID).
		Order("sort_order ASC, id ASC").
		Find(&out).Error
	return out, err
}

func (r *TagCategoryRepo) Get(ctx context.Context, id uint) (*model.TagCategory, error) {
	var c model.TagCategory
	if err := conn(ctx, r.db).First(&c, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperr.ErrNotFound.WithDetail("标签分类 %d 不存在", id)
		}
		return nil, err
	}
	return &c, nil
}

func (r *TagCategoryRepo) Create(ctx context.Context, c *model.TagCategory) error {
	if err := conn(ctx, r.db).Create(c).Error; err != nil {
		return fmt.Errorf("创建标签分类失败: %w", err)
	}
	return nil
}

func (r *TagCategoryRepo) UpdateFields(ctx context.Context, id uint, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	return conn(ctx, r.db).Model(&model.TagCategory{}).Where("id = ?", id).Updates(fields).Error
}

func (r *TagCategoryRepo) SoftDelete(ctx context.Context, id uint) error {
	return conn(ctx, r.db).Delete(&model.TagCategory{}, id).Error
}

// ClearCategory 把该分类下的标签改为"未分类"。
// 删除分类时使用，避免标签因为分类消失而变成悬空引用。
func (r *TagCategoryRepo) ClearCategory(ctx context.Context, categoryID uint) error {
	return conn(ctx, r.db).
		Model(&model.Tag{}).
		Where("category_id = ?", categoryID).
		Update("category_id", nil).Error
}

func (r *TagCategoryRepo) ExistsName(ctx context.Context, subjectID uint, name string, excludeID uint) (bool, error) {
	q := conn(ctx, r.db).Model(&model.TagCategory{}).
		Where("subject_id = ? AND name = ?", subjectID, name)
	if excludeID > 0 {
		q = q.Where("id <> ?", excludeID)
	}
	var n int64
	if err := q.Count(&n).Error; err != nil {
		return false, err
	}
	return n > 0, nil
}

// CountTags 统计每个分类下的标签数。
func (r *TagCategoryRepo) CountTags(ctx context.Context, subjectID uint) (map[uint]int64, error) {
	type row struct {
		CategoryID uint
		Total      int64
	}
	var rows []row
	err := conn(ctx, r.db).
		Model(&model.Tag{}).
		Select("category_id, COUNT(*) AS total").
		Where("subject_id = ? AND category_id IS NOT NULL", subjectID).
		Group("category_id").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make(map[uint]int64, len(rows))
	for _, r := range rows {
		out[r.CategoryID] = r.Total
	}
	return out, nil
}

// TagRepo 标签数据访问。
type TagRepo struct {
	db *gorm.DB
}

func NewTagRepo(db *gorm.DB) *TagRepo { return &TagRepo{db: db} }

func (r *TagRepo) ListBySubject(ctx context.Context, subjectID uint, categoryID *uint) ([]model.Tag, error) {
	q := conn(ctx, r.db).Preload("Category").Where("subject_id = ?", subjectID)
	if categoryID != nil {
		q = q.Where("category_id = ?", *categoryID)
	}
	var out []model.Tag
	err := q.Order("sort_order ASC, id ASC").Find(&out).Error
	return out, err
}

func (r *TagRepo) Get(ctx context.Context, id uint) (*model.Tag, error) {
	var t model.Tag
	if err := conn(ctx, r.db).Preload("Category").First(&t, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperr.ErrNotFound.WithDetail("标签 %d 不存在", id)
		}
		return nil, err
	}
	return &t, nil
}

func (r *TagRepo) Create(ctx context.Context, t *model.Tag) error {
	if err := conn(ctx, r.db).Create(t).Error; err != nil {
		return fmt.Errorf("创建标签失败: %w", err)
	}
	return nil
}

func (r *TagRepo) UpdateFields(ctx context.Context, id uint, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	return conn(ctx, r.db).Model(&model.Tag{}).Where("id = ?", id).Updates(fields).Error
}

func (r *TagRepo) SoftDelete(ctx context.Context, id uint) error {
	return conn(ctx, r.db).Delete(&model.Tag{}, id).Error
}

func (r *TagRepo) ExistsName(ctx context.Context, subjectID uint, name string, excludeID uint) (bool, error) {
	q := conn(ctx, r.db).Model(&model.Tag{}).
		Where("subject_id = ? AND name = ?", subjectID, name)
	if excludeID > 0 {
		q = q.Where("id <> ?", excludeID)
	}
	var n int64
	if err := q.Count(&n).Error; err != nil {
		return false, err
	}
	return n > 0, nil
}

func (r *TagRepo) CountQuestionsByTag(ctx context.Context, subjectID uint) (map[uint]int64, error) {
	type row struct {
		TagID uint
		Total int64
	}
	var rows []row
	err := conn(ctx, r.db).
		Table("question_tags AS qt").
		Select("qt.tag_id AS tag_id, COUNT(*) AS total").
		Joins("JOIN questions AS q ON q.id = qt.question_id AND q.deleted_at IS NULL").
		Where("q.subject_id = ?", subjectID).
		Group("qt.tag_id").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make(map[uint]int64, len(rows))
	for _, r := range rows {
		out[r.TagID] = r.Total
	}
	return out, nil
}

// UnlinkQuestions 解除标签与题目的关联。
func (r *TagRepo) UnlinkQuestions(ctx context.Context, tagID uint) error {
	return conn(ctx, r.db).Where("tag_id = ?", tagID).Delete(&model.QuestionTag{}).Error
}

// LinkQuestions 建立题目-标签关联。
func (r *TagRepo) LinkQuestions(ctx context.Context, questionID uint, tagIDs []uint) error {
	if len(tagIDs) == 0 {
		return nil
	}
	links := make([]model.QuestionTag, 0, len(tagIDs))
	for _, id := range tagIDs {
		links = append(links, model.QuestionTag{QuestionID: questionID, TagID: id})
	}
	return conn(ctx, r.db).Create(&links).Error
}

// IDsBySubject 返回某学科下全部标签 ID。
func (r *TagRepo) IDsBySubject(ctx context.Context, subjectID uint) (map[uint]bool, error) {
	var ids []uint
	err := conn(ctx, r.db).
		Model(&model.Tag{}).
		Where("subject_id = ?", subjectID).
		Pluck("id", &ids).Error
	if err != nil {
		return nil, err
	}
	set := make(map[uint]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set, nil
}

// NamesBySubject 返回学科下全部标签名称，供拼进 AI 提示词。
func (r *TagRepo) NamesBySubject(ctx context.Context, subjectID uint) ([]string, error) {
	var names []string
	err := conn(ctx, r.db).
		Model(&model.Tag{}).
		Where("subject_id = ?", subjectID).
		Order("sort_order ASC, id ASC").
		Pluck("name", &names).Error
	return names, err
}

// IDsByNames 按名称批量查标签 id（精确匹配，理由同知识点）。
func (r *TagRepo) IDsByNames(ctx context.Context, subjectID uint, names []string) (map[string]uint, error) {
	out := make(map[string]uint, len(names))
	if len(names) == 0 {
		return out, nil
	}
	type row struct {
		ID   uint
		Name string
	}
	var rows []row
	err := conn(ctx, r.db).Model(&model.Tag{}).
		Select("id, name").
		Where("subject_id = ? AND name IN ?", subjectID, names).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.Name] = r.ID
	}
	return out, nil
}
