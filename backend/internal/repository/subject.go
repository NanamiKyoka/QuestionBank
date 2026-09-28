package repository

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
)

// SubjectRepo 学科与学科成员数据访问。
type SubjectRepo struct {
	db *gorm.DB
}

// NewSubjectRepo 构造。
func NewSubjectRepo(db *gorm.DB) *SubjectRepo { return &SubjectRepo{db: db} }

// Create 新建学科。
func (r *SubjectRepo) Create(ctx context.Context, s *model.Subject) error {
	if err := conn(ctx, r.db).Create(s).Error; err != nil {
		return fmt.Errorf("创建学科失败: %w", err)
	}
	return nil
}

// Get 按 ID 取学科。
func (r *SubjectRepo) Get(ctx context.Context, id uint) (*model.Subject, error) {
	var s model.Subject
	if err := conn(ctx, r.db).First(&s, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperr.ErrNotFound.WithDetail("学科 %d 不存在", id)
		}
		return nil, err
	}
	return &s, nil
}

// ExistsName 学科名是否已存在。
func (r *SubjectRepo) ExistsName(ctx context.Context, name string, excludeID uint) (bool, error) {
	q := conn(ctx, r.db).Model(&model.Subject{}).Where("name = ?", name)
	if excludeID > 0 {
		q = q.Where("id <> ?", excludeID)
	}
	var n int64
	if err := q.Count(&n).Error; err != nil {
		return false, err
	}
	return n > 0, nil
}

// ExistsCode 学科编码是否已存在（仅当 code 非空时有意义）。
func (r *SubjectRepo) ExistsCode(ctx context.Context, code string, excludeID uint) (bool, error) {
	if code == "" {
		return false, nil
	}
	q := conn(ctx, r.db).Model(&model.Subject{}).Where("code = ?", code)
	if excludeID > 0 {
		q = q.Where("id <> ?", excludeID)
	}
	var n int64
	if err := q.Count(&n).Error; err != nil {
		return false, err
	}
	return n > 0, nil
}

// List 返回指定 ID 集合内的学科；ids 为 nil 表示不限制（管理员）。
func (r *SubjectRepo) List(ctx context.Context, ids map[uint]bool) ([]model.Subject, error) {
	q := conn(ctx, r.db).Model(&model.Subject{})
	if ids != nil {
		if len(ids) == 0 {
			return []model.Subject{}, nil
		}
		list := make([]uint, 0, len(ids))
		for id := range ids {
			list = append(list, id)
		}
		q = q.Where("id IN ?", list)
	}
	var out []model.Subject
	if err := q.Order("sort_order ASC, id ASC").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// UpdateFields 按字段白名单更新学科。
func (r *SubjectRepo) UpdateFields(ctx context.Context, id uint, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	res := conn(ctx, r.db).Model(&model.Subject{}).Where("id = ?", id).Updates(fields)
	if res.Error != nil {
		return fmt.Errorf("更新学科失败: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		// 可能是"值未变化"，需要确认记录确实存在。
		if _, err := r.Get(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// ---- 成员 ----

// AddMember 添加学科成员；已存在则更新角色（幂等）。
func (r *SubjectRepo) AddMember(ctx context.Context, subjectID, userID uint, role model.SubjectRole) error {
	return conn(ctx, r.db).Transaction(func(tx *gorm.DB) error {
		var existing model.SubjectMember
		err := tx.Where("subject_id = ? AND user_id = ?", subjectID, userID).First(&existing).Error
		switch {
		case err == nil:
			if existing.Role == role {
				return nil
			}
			return tx.Model(&existing).Update("role", role).Error
		case errors.Is(err, gorm.ErrRecordNotFound):
			m := model.SubjectMember{UserID: userID, SubjectID: subjectID, Role: role}
			return tx.Create(&m).Error
		default:
			return err
		}
	})
}

// RemoveMember 移除学科成员。
func (r *SubjectRepo) RemoveMember(ctx context.Context, subjectID, userID uint) error {
	res := conn(ctx, r.db).
		Where("subject_id = ? AND user_id = ?", subjectID, userID).
		Delete(&model.SubjectMember{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return apperr.ErrNotFound.WithDetail("该用户不是本学科成员")
	}
	return nil
}

// ListMembers 列出学科成员（含用户信息）。
func (r *SubjectRepo) ListMembers(ctx context.Context, subjectID uint) ([]model.SubjectMember, error) {
	var out []model.SubjectMember
	err := conn(ctx, r.db).
		Preload("User").
		Where("subject_id = ?", subjectID).
		Order("role ASC, id ASC").
		Find(&out).Error
	return out, err
}

// CountManagers 统计学科的负责人数量，用于阻止移除最后一名负责人。
func (r *SubjectRepo) CountManagers(ctx context.Context, subjectID uint) (int64, error) {
	var n int64
	err := conn(ctx, r.db).
		Model(&model.SubjectMember{}).
		Where("subject_id = ? AND role = ?", subjectID, model.SubjectRoleManager).
		Count(&n).Error
	return n, err
}

// SetLastSubject 记录用户最后工作的学科。
func (r *SubjectRepo) SetLastSubject(ctx context.Context, userID uint, subjectID *uint) error {
	return conn(ctx, r.db).
		Model(&model.User{}).
		Where("id = ?", userID).
		Update("last_subject_id", subjectID).Error
}

// SubjectDependents 学科的从属数据统计。
type SubjectDependents struct {
	Questions       int64
	KnowledgePoints int64
	Papers          int64
	Members         int64
}

// CountDependents 统计学科下挂了多少数据，用于删除前的安全检查。
func (r *SubjectRepo) CountDependents(ctx context.Context, subjectID uint) (SubjectDependents, error) {
	var d SubjectDependents
	db := conn(ctx, r.db)

	if err := db.Model(&model.Question{}).Where("subject_id = ?", subjectID).Count(&d.Questions).Error; err != nil {
		return d, err
	}
	if err := db.Model(&model.KnowledgePoint{}).Where("subject_id = ?", subjectID).Count(&d.KnowledgePoints).Error; err != nil {
		return d, err
	}
	if err := db.Model(&model.Paper{}).Where("subject_id = ?", subjectID).Count(&d.Papers).Error; err != nil {
		return d, err
	}
	if err := db.Model(&model.SubjectMember{}).Where("subject_id = ?", subjectID).Count(&d.Members).Error; err != nil {
		return d, err
	}
	return d, nil
}

// SoftDelete 软删除学科。
func (r *SubjectRepo) SoftDelete(ctx context.Context, id uint) error {
	return conn(ctx, r.db).Delete(&model.Subject{}, id).Error
}
