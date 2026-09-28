package repository

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
)

// PaperRepo 试卷数据访问。
type PaperRepo struct {
	db *gorm.DB
}

func NewPaperRepo(db *gorm.DB) *PaperRepo { return &PaperRepo{db: db} }

// PaperFilter 试卷列表筛选。
type PaperFilter struct {
	SubjectID uint
	// Scope 为空表示不限。
	Scope model.PaperScope
	// OwnerID 配合 Scope=personal 使用；Scope=shared 时按 owner_id IS NULL 过滤。
	OwnerID *uint
	// FolderID 指定目录；Unfiled 为真时只看未归档到任何目录的。
	FolderID *uint
	Unfiled  bool

	Keyword string
	Status  model.PaperStatus
}

func (r *PaperRepo) buildQuery(ctx context.Context, f PaperFilter) *gorm.DB {
	q := conn(ctx, r.db).Model(&model.Paper{})

	if f.SubjectID > 0 {
		q = q.Where("subject_id = ?", f.SubjectID)
	}
	if f.Scope != "" {
		q = q.Where("scope = ?", f.Scope)
		if f.Scope == model.PaperScopeShared {
			// 共享空间的归属就是"没有个人所有者"。
			q = q.Where("owner_id IS NULL")
		} else if f.OwnerID != nil {
			q = q.Where("owner_id = ?", *f.OwnerID)
		}
	}
	switch {
	case f.Unfiled:
		q = q.Where("folder_id IS NULL")
	case f.FolderID != nil:
		q = q.Where("folder_id = ?", *f.FolderID)
	}
	if f.Keyword != "" {
		like := "%" + escapeLike(f.Keyword) + "%"
		q = q.Where("title LIKE ? OR description LIKE ?", like, like)
	}
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	return q
}

func (r *PaperRepo) List(ctx context.Context, f PaperFilter, limit, offset int) ([]model.Paper, int64, error) {
	q := r.buildQuery(ctx, f)

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("统计试卷数量失败: %w", err)
	}
	if total == 0 {
		return []model.Paper{}, 0, nil
	}

	var list []model.Paper
	err := q.Order("updated_at DESC, id DESC").Limit(limit).Offset(offset).Find(&list).Error
	if err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

func (r *PaperRepo) Get(ctx context.Context, id uint) (*model.Paper, error) {
	var p model.Paper
	if err := conn(ctx, r.db).First(&p, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperr.ErrNotFound.WithDetail("试卷 %d 不存在", id)
		}
		return nil, err
	}
	return &p, nil
}

func (r *PaperRepo) Create(ctx context.Context, p *model.Paper) error {
	if err := conn(ctx, r.db).Create(p).Error; err != nil {
		return fmt.Errorf("创建试卷失败: %w", err)
	}
	return nil
}

// UpdateFields 无条件更新（用于元信息修改，不涉及并发冲突的字段）。
func (r *PaperRepo) UpdateFields(ctx context.Context, id uint, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	return conn(ctx, r.db).Model(&model.Paper{}).Where("id = ?", id).Updates(fields).Error
}

// UpdateWithRevision 带乐观锁的更新。
//
// UPDATE ... WHERE id = ? AND revision = ?
// 影响行数为 0 即表示期间有人改过，调用方返回 409。
// 用条件更新而不是"先查再写"，是因为后者在两个请求之间仍有窗口，
// 而条件更新把判断交给数据库，天然原子。
func (r *PaperRepo) UpdateWithRevision(ctx context.Context, id uint, expectedRevision int64, fields map[string]any) (bool, error) {
	update := make(map[string]any, len(fields)+1)
	for k, v := range fields {
		update[k] = v
	}
	update["revision"] = gorm.Expr("revision + 1")

	res := conn(ctx, r.db).Model(&model.Paper{}).
		Where("id = ? AND revision = ?", id, expectedRevision).
		Updates(update)
	if res.Error != nil {
		return false, fmt.Errorf("更新试卷失败: %w", res.Error)
	}
	return res.RowsAffected > 0, nil
}

func (r *PaperRepo) SoftDelete(ctx context.Context, id uint) error {
	return conn(ctx, r.db).Delete(&model.Paper{}, id).Error
}

func (r *PaperRepo) Restore(ctx context.Context, id uint) error {
	return conn(ctx, r.db).Unscoped().Model(&model.Paper{}).
		Where("id = ?", id).Update("deleted_at", nil).Error
}

// ---- 节点 ----

func (r *PaperRepo) Nodes(ctx context.Context, paperID uint) ([]model.PaperNode, error) {
	var out []model.PaperNode
	// 读取顺序与 flatten 的写入顺序一致：(parent_id, position)。
	// 顶层节点 parent_id 为 NULL，用 id 兜底保证顺序稳定。
	err := conn(ctx, r.db).
		Where("paper_id = ?", paperID).
		Order("parent_id ASC, position ASC, created_at ASC").
		Find(&out).Error
	return out, err
}

// ReplaceNodes 整树替换：先清空再写入。
//
// 必须在事务中调用（service 负责），否则中途失败会留下半棵树。
func (r *PaperRepo) ReplaceNodes(ctx context.Context, paperID uint, nodes []model.PaperNode) error {
	db := conn(ctx, r.db)
	if err := db.Where("paper_id = ?", paperID).Delete(&model.PaperNode{}).Error; err != nil {
		return fmt.Errorf("清空旧节点失败: %w", err)
	}
	if len(nodes) == 0 {
		return nil
	}
	for i := range nodes {
		nodes[i].PaperID = paperID
	}
	// 分批插入：SQLite 对单条语句的变量个数有上限（默认 999），
	// 大批量一次插入会报 "too many SQL variables"。
	const batch = 40
	for start := 0; start < len(nodes); start += batch {
		end := start + batch
		if end > len(nodes) {
			end = len(nodes)
		}
		if err := db.Create(nodes[start:end]).Error; err != nil {
			return fmt.Errorf("写入节点失败: %w", err)
		}
	}
	return nil
}

// QuestionCountsByPaper 批量统计各试卷的题目块数量。
func (r *PaperRepo) QuestionCountsByPaper(ctx context.Context, paperIDs []uint) (map[uint]int, error) {
	out := make(map[uint]int)
	if len(paperIDs) == 0 {
		return out, nil
	}

	type row struct {
		PaperID uint
		Total   int
	}
	var rows []row
	err := conn(ctx, r.db).Model(&model.PaperNode{}).
		Select("paper_id, COUNT(*) AS total").
		Where("paper_id IN ? AND node_type = ?", paperIDs, model.NodeTypeQuestion).
		Group("paper_id").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.PaperID] = r.Total
	}
	return out, nil
}

// ---- 版本 ----

// NextVersionNo 计算下一个版本号。
func (r *PaperRepo) NextVersionNo(ctx context.Context, paperID uint) (int, error) {
	var maxNo int
	err := conn(ctx, r.db).Model(&model.PaperVersion{}).
		Where("paper_id = ?", paperID).
		Select("COALESCE(MAX(version_no), 0)").
		Scan(&maxNo).Error
	return maxNo + 1, err
}

func (r *PaperRepo) CreateVersion(ctx context.Context, v *model.PaperVersion) error {
	if err := conn(ctx, r.db).Create(v).Error; err != nil {
		// 唯一索引冲突：并发定稿时另一个请求已占用该版本号。
		return fmt.Errorf("创建版本快照失败: %w", err)
	}
	return nil
}

func (r *PaperRepo) ListVersions(ctx context.Context, paperID uint, limit int) ([]model.PaperVersion, error) {
	var out []model.PaperVersion
	err := conn(ctx, r.db).
		Where("paper_id = ?", paperID).
		Order("version_no DESC").
		Limit(limit).
		Find(&out).Error
	return out, err
}

func (r *PaperRepo) GetVersion(ctx context.Context, paperID uint, versionNo int) (*model.PaperVersion, error) {
	var v model.PaperVersion
	err := conn(ctx, r.db).
		Where("paper_id = ? AND version_no = ?", paperID, versionNo).
		First(&v).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperr.ErrNotFound.WithDetail("版本 %d 不存在", versionNo)
		}
		return nil, err
	}
	return &v, nil
}

// ---- 操作时间线 ----

func (r *PaperRepo) AddEvent(ctx context.Context, ev *model.PaperEvent) error {
	return conn(ctx, r.db).Create(ev).Error
}

// ListEvents 按 id 倒序返回时间线；beforeID 大于 0 时取其之前的记录（键集分页）。
func (r *PaperRepo) ListEvents(ctx context.Context, paperID uint, limit int, beforeID uint) ([]model.PaperEvent, error) {
	q := conn(ctx, r.db).Preload("Actor").Where("paper_id = ?", paperID)
	if beforeID > 0 {
		q = q.Where("id < ?", beforeID)
	}
	var out []model.PaperEvent
	err := q.Order("id DESC").Limit(limit).Find(&out).Error
	return out, err
}
