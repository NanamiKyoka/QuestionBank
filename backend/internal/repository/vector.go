package repository

import (
	"context"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/NanamiKyoka/questionmind/internal/model"
)

// VectorRepo 知识点向量数据访问。
type VectorRepo struct {
	db *gorm.DB
}

func NewVectorRepo(db *gorm.DB) *VectorRepo { return &VectorRepo{db: db} }

// Upsert 写入或更新一个知识点的向量。
//
// 用 SQL 层的 ON CONFLICT 而不是 GORM 的 FirstOrCreate+Assign：
// 后者在这里表现出"元数据更新了、embedding 没更新"的行为——
// 哈希变了、向量还是旧的，而且不报任何错。
// 这种静默失效最危险：知识点改名后向量永远停在旧值上，
// 检索结果看起来正常，只是永远匹配不准。
//
// ON CONFLICT 依赖 knowledge_point_id 上的唯一索引（见 model.VectorDoc）。
func (r *VectorRepo) Upsert(ctx context.Context, doc *model.VectorDoc) error {
	return conn(ctx, r.db).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "knowledge_point_id"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"subject_id", "model_id", "content_hash", "dim", "embedding", "updated_at",
			}),
		}).
		Create(doc).Error
}

// ListBySubject 取出某学科的全部向量，供进程内检索使用。
//
// 一次性全量载入而不是流式：几千条向量只有几 MB，
// 检索要反复扫描，放内存里比每次查库划算得多。
func (r *VectorRepo) ListBySubject(ctx context.Context, subjectID uint) ([]model.VectorDoc, error) {
	var out []model.VectorDoc
	err := conn(ctx, r.db).
		Where("subject_id = ?", subjectID).
		Find(&out).Error
	return out, err
}

// Get 按知识点取向量。
func (r *VectorRepo) Get(ctx context.Context, kpID uint) (*model.VectorDoc, error) {
	var doc model.VectorDoc
	err := conn(ctx, r.db).Where("knowledge_point_id = ?", kpID).First(&doc).Error
	if err != nil {
		return nil, err
	}
	return &doc, nil
}

// DeleteByKP 删除单个知识点的向量。
func (r *VectorRepo) DeleteByKP(ctx context.Context, kpID uint) error {
	return conn(ctx, r.db).Where("knowledge_point_id = ?", kpID).Delete(&model.VectorDoc{}).Error
}

// DeleteByKPs 批量删除。
func (r *VectorRepo) DeleteByKPs(ctx context.Context, ids []uint) error {
	if len(ids) == 0 {
		return nil
	}
	return conn(ctx, r.db).Where("knowledge_point_id IN ?", ids).Delete(&model.VectorDoc{}).Error
}

// DeleteBySubject 清空某学科的索引，用于换模型后全量重建。
func (r *VectorRepo) DeleteBySubject(ctx context.Context, subjectID uint) error {
	return conn(ctx, r.db).Where("subject_id = ?", subjectID).Delete(&model.VectorDoc{}).Error
}

// DeleteByModel 清空使用过某模型的索引。
func (r *VectorRepo) DeleteByModel(ctx context.Context, modelID uint) error {
	return conn(ctx, r.db).Where("model_id = ?", modelID).Delete(&model.VectorDoc{}).Error
}

// Stat 某学科的索引状态。
type VectorStat struct {
	// Indexed 已索引的知识点数。
	Indexed int64
	// DistinctModels 涉及的 embedding 模型数量。
	// 大于 1 说明索引不干净（换过模型但没重建），检索结果会不可比。
	DistinctModels int64
	// Dims 出现过的维度集合。
	Dims []int
}

// StatBySubject 统计索引状态，用于界面提示"是否需要重建"。
func (r *VectorRepo) StatBySubject(ctx context.Context, subjectID uint) (VectorStat, error) {
	var stat VectorStat
	db := conn(ctx, r.db)

	if err := db.Model(&model.VectorDoc{}).Where("subject_id = ?", subjectID).
		Count(&stat.Indexed).Error; err != nil {
		return stat, err
	}

	db.Model(&model.VectorDoc{}).Where("subject_id = ?", subjectID).
		Distinct("model_id").Count(&stat.DistinctModels)

	var dims []int
	if err := db.Model(&model.VectorDoc{}).Where("subject_id = ?", subjectID).
		Distinct("dim").Pluck("dim", &dims).Error; err != nil {
		return stat, fmt.Errorf("统计向量维度失败: %w", err)
	}
	stat.Dims = dims
	return stat, nil
}
