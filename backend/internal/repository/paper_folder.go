package repository

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
)

// PaperFolderRepo 试卷目录数据访问。
type PaperFolderRepo struct {
	db *gorm.DB
}

func NewPaperFolderRepo(db *gorm.DB) *PaperFolderRepo { return &PaperFolderRepo{db: db} }

// List 返回某空间下的全部目录。
//
// 空间由 (subject_id, scope, owner_id) 三元组确定，没有独立的"空间表"：
// shared 空间 owner_id 为 NULL，personal 空间 owner_id 为具体用户。
func (r *PaperFolderRepo) List(ctx context.Context, subjectID uint, scope model.PaperScope, ownerID *uint) ([]model.PaperFolder, error) {
	q := conn(ctx, r.db).Where("subject_id = ? AND scope = ?", subjectID, scope)
	if ownerID == nil {
		q = q.Where("owner_id IS NULL")
	} else {
		q = q.Where("owner_id = ?", *ownerID)
	}

	var out []model.PaperFolder
	err := q.Order("sort_order ASC, id ASC").Find(&out).Error
	return out, err
}

func (r *PaperFolderRepo) Get(ctx context.Context, id uint) (*model.PaperFolder, error) {
	var f model.PaperFolder
	if err := conn(ctx, r.db).First(&f, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperr.ErrNotFound.WithDetail("目录 %d 不存在", id)
		}
		return nil, err
	}
	return &f, nil
}

func (r *PaperFolderRepo) Create(ctx context.Context, f *model.PaperFolder) error {
	if err := conn(ctx, r.db).Create(f).Error; err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}
	return nil
}

func (r *PaperFolderRepo) UpdateFields(ctx context.Context, id uint, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	return conn(ctx, r.db).Model(&model.PaperFolder{}).Where("id = ?", id).Updates(fields).Error
}

func (r *PaperFolderRepo) SoftDelete(ctx context.Context, id uint) error {
	return conn(ctx, r.db).Delete(&model.PaperFolder{}, id).Error
}

// CountChildren 直接子目录数量，删除前用于判断目录是否非空。
func (r *PaperFolderRepo) CountChildren(ctx context.Context, id uint) (int64, error) {
	var n int64
	err := conn(ctx, r.db).Model(&model.PaperFolder{}).Where("parent_id = ?", id).Count(&n).Error
	return n, err
}

// CountPapers 目录下的试卷数量。
func (r *PaperFolderRepo) CountPapers(ctx context.Context, id uint) (int64, error) {
	var n int64
	err := conn(ctx, r.db).Model(&model.Paper{}).Where("folder_id = ?", id).Count(&n).Error
	return n, err
}

// PaperCountsByFolder 批量统计各目录下的试卷数。
func (r *PaperFolderRepo) PaperCountsByFolder(ctx context.Context, subjectID uint) (map[uint]int64, error) {
	type row struct {
		FolderID uint
		Total    int64
	}
	var rows []row
	err := conn(ctx, r.db).Model(&model.Paper{}).
		Select("folder_id, COUNT(*) AS total").
		Where("subject_id = ? AND folder_id IS NOT NULL", subjectID).
		Group("folder_id").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make(map[uint]int64, len(rows))
	for _, r := range rows {
		out[r.FolderID] = r.Total
	}
	return out, nil
}

// ExistsName 同一父目录下是否同名。
func (r *PaperFolderRepo) ExistsName(ctx context.Context, subjectID uint, scope model.PaperScope, ownerID *uint, parentID *uint, name string, excludeID uint) (bool, error) {
	q := conn(ctx, r.db).Model(&model.PaperFolder{}).
		Where("subject_id = ? AND scope = ? AND name = ?", subjectID, scope, name)
	if ownerID == nil {
		q = q.Where("owner_id IS NULL")
	} else {
		q = q.Where("owner_id = ?", *ownerID)
	}
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

// IsDescendant 判断 candidateID 是否位于 ancestorID 的子树中。
// 用于阻止把目录移动到自己的子目录下形成环。
func (r *PaperFolderRepo) IsDescendant(ctx context.Context, subjectID uint, ancestorID, candidateID uint) (bool, error) {
	if ancestorID == candidateID {
		return true, nil
	}
	cur := candidateID
	// 逐层上溯，最多 32 层，避免数据异常时死循环。
	for depth := 0; depth < 32; depth++ {
		f, err := r.Get(ctx, cur)
		if err != nil {
			if apperr.Is(err, apperr.CodeNotFound) {
				return false, nil
			}
			return false, err
		}
		if f.ParentID == nil {
			return false, nil
		}
		if *f.ParentID == ancestorID {
			return true, nil
		}
		cur = *f.ParentID
	}
	return false, nil
}
