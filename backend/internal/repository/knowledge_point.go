package repository

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
)

// KnowledgePointRepo 知识点数据访问。
type KnowledgePointRepo struct {
	db *gorm.DB
}

func NewKnowledgePointRepo(db *gorm.DB) *KnowledgePointRepo {
	return &KnowledgePointRepo{db: db}
}

func (r *KnowledgePointRepo) Get(ctx context.Context, id uint) (*model.KnowledgePoint, error) {
	var k model.KnowledgePoint
	if err := conn(ctx, r.db).First(&k, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperr.ErrNotFound.WithDetail("知识点 %d 不存在", id)
		}
		return nil, err
	}
	return &k, nil
}

func (r *KnowledgePointRepo) ListBySubject(ctx context.Context, subjectID uint) ([]model.KnowledgePoint, error) {
	var out []model.KnowledgePoint
	err := conn(ctx, r.db).
		Where("subject_id = ?", subjectID).
		Order("sort_order ASC, id ASC").
		Find(&out).Error
	return out, err
}

func (r *KnowledgePointRepo) Create(ctx context.Context, k *model.KnowledgePoint) error {
	if err := conn(ctx, r.db).Create(k).Error; err != nil {
		return fmt.Errorf("创建知识点失败: %w", err)
	}
	return nil
}

func (r *KnowledgePointRepo) UpdateFields(ctx context.Context, id uint, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	res := conn(ctx, r.db).Model(&model.KnowledgePoint{}).Where("id = ?", id).Updates(fields)
	if res.Error != nil {
		return fmt.Errorf("更新知识点失败: %w", res.Error)
	}
	return nil
}

// ExistsName 同一父节点下知识点名是否重复。
func (r *KnowledgePointRepo) ExistsName(ctx context.Context, subjectID uint, parentID *uint, name string, excludeID uint) (bool, error) {
	q := conn(ctx, r.db).Model(&model.KnowledgePoint{}).
		Where("subject_id = ? AND name = ?", subjectID, name)
	if parentID == nil {
		q = q.Where("parent_id IS NULL")
	} else {
		q = q.Where("parent_id = ?", *parentID)
	}
	if excludeID > 0 {
		q = q.Where("id <> ?", excludeID)
	}
	var n int64
	if err := q.Count(&n).Error; err != nil {
		return false, err
	}
	return n > 0, nil
}

// CountChildren 直接子节点数量。
func (r *KnowledgePointRepo) CountChildren(ctx context.Context, id uint) (int64, error) {
	var n int64
	err := conn(ctx, r.db).Model(&model.KnowledgePoint{}).Where("parent_id = ?", id).Count(&n).Error
	return n, err
}

// Subtree 返回以 path 为前缀的全部节点（含自身）。
func (r *KnowledgePointRepo) Subtree(ctx context.Context, subjectID uint, path string) ([]model.KnowledgePoint, error) {
	var out []model.KnowledgePoint
	err := conn(ctx, r.db).
		Where("subject_id = ? AND path LIKE ?", subjectID, path+"%").
		Find(&out).Error
	return out, err
}

// SoftDeleteByIDs 批量软删除。
func (r *KnowledgePointRepo) SoftDeleteByIDs(ctx context.Context, ids []uint) error {
	if len(ids) == 0 {
		return nil
	}
	return conn(ctx, r.db).Where("id IN ?", ids).Delete(&model.KnowledgePoint{}).Error
}

// UnlinkQuestions 解除这些知识点与题目的关联。
//
// 关联表是硬删除：知识点都没了，关联记录没有保留价值，
// 留着反而会让统计数字对不上。
func (r *KnowledgePointRepo) UnlinkQuestions(ctx context.Context, ids []uint) error {
	if len(ids) == 0 {
		return nil
	}
	return conn(ctx, r.db).
		Where("knowledge_point_id IN ?", ids).
		Delete(&model.QuestionKnowledgePoint{}).Error
}

// CountAffectedQuestions 统计与该学科知识点关联的去重题目数。
func (r *KnowledgePointRepo) CountAffectedQuestions(ctx context.Context, subjectID uint) (int64, error) {
	var total int64
	err := conn(ctx, r.db).
		Table("question_knowledge_points AS qkp").
		Joins("JOIN knowledge_points AS kp ON kp.id = qkp.knowledge_point_id AND kp.deleted_at IS NULL").
		Joins("JOIN questions AS q ON q.id = qkp.question_id AND q.deleted_at IS NULL").
		Where("kp.subject_id = ? AND q.subject_id = ?", subjectID, subjectID).
		Distinct("qkp.question_id").
		Count(&total).Error
	return total, err
}

// CountQuestionsByKP 统计每个知识点关联的题目数（已软删除的题目不计）。
func (r *KnowledgePointRepo) CountQuestionsByKP(ctx context.Context, subjectID uint) (map[uint]int64, error) {
	type row struct {
		KPID  uint
		Total int64
	}
	var rows []row

	err := conn(ctx, r.db).
		Table("question_knowledge_points AS qkp").
		Select("qkp.knowledge_point_id AS kp_id, COUNT(*) AS total").
		Joins("JOIN questions AS q ON q.id = qkp.question_id AND q.deleted_at IS NULL").
		Where("q.subject_id = ?", subjectID).
		Group("qkp.knowledge_point_id").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	out := make(map[uint]int64, len(rows))
	for _, r := range rows {
		out[r.KPID] = r.Total
	}
	return out, nil
}

// LinkQuestions 建立题目-知识点关联。
func (r *KnowledgePointRepo) LinkQuestions(ctx context.Context, questionID uint, kpIDs []uint) error {
	if len(kpIDs) == 0 {
		return nil
	}
	links := make([]model.QuestionKnowledgePoint, 0, len(kpIDs))
	for _, id := range kpIDs {
		links = append(links, model.QuestionKnowledgePoint{QuestionID: questionID, KnowledgePointID: id})
	}
	return conn(ctx, r.db).Create(&links).Error
}

// RewriteSubtreePath 重写子树的物化路径与层级（移动节点时使用）。
//
// 用 SQL 的 REPLACE + level 自增一次更新整棵子树，避免在 Go 里逐节点计算。
// oldPrefix 只会出现在 path 的开头，因此 REPLACE 不会误伤中间片段。
func (r *KnowledgePointRepo) RewriteSubtreePath(ctx context.Context, subjectID uint, oldPrefix, newPrefix string, levelDelta int) error {
	return conn(ctx, r.db).Model(&model.KnowledgePoint{}).
		Where("subject_id = ? AND path LIKE ?", subjectID, oldPrefix+"%").
		Updates(map[string]any{
			"path":  gorm.Expr("REPLACE(path, ?, ?)", oldPrefix, newPrefix),
			"level": gorm.Expr("level + ?", levelDelta),
		}).Error
}

// IDsBySubject 返回某学科下全部知识点 ID，供批量校验引用合法性。
func (r *KnowledgePointRepo) IDsBySubject(ctx context.Context, subjectID uint) (map[uint]bool, error) {
	var ids []uint
	err := conn(ctx, r.db).
		Model(&model.KnowledgePoint{}).
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

// NamesBySubject 返回学科下全部知识点名称，供拼进 AI 提示词。
func (r *KnowledgePointRepo) NamesBySubject(ctx context.Context, subjectID uint) ([]string, error) {
	var names []string
	err := conn(ctx, r.db).
		Model(&model.KnowledgePoint{}).
		Where("subject_id = ?", subjectID).
		Order("path ASC").
		Pluck("name", &names).Error
	return names, err
}

// IDsByNames 按名称批量查 id，返回 名称→id 映射。
//
// 名称匹配是**精确匹配**（区分大小写与否取决于数据库排序规则）。
// 不做模糊匹配是刻意的：模糊匹配会把"单调性"错配到"函数单调性"上，
// 这类错误在导入时很难被发现，却会长期污染题目的知识点归属。
func (r *KnowledgePointRepo) IDsByNames(ctx context.Context, subjectID uint, names []string) (map[string]uint, error) {
	out := make(map[string]uint, len(names))
	if len(names) == 0 {
		return out, nil
	}
	type row struct {
		ID   uint
		Name string
	}
	var rows []row
	err := conn(ctx, r.db).Model(&model.KnowledgePoint{}).
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
