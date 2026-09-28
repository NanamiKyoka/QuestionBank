// Package repository 是唯一直接接触 ORM 的层。
//
// 约定：
//   - service 层不允许出现 gorm.DB，只调用 repository 方法；
//   - 任何把用户输入拼进 SQL 的行为都在这里被杜绝（全程参数化）。
package repository

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
)

// UserRepo 用户数据访问。
type UserRepo struct {
	db *gorm.DB
}

// NewUserRepo 构造。
func NewUserRepo(db *gorm.DB) *UserRepo { return &UserRepo{db: db} }

// GetWithMemberships 按 ID 取用户，并预加载学科成员关系。
//
// 预加载是硬性要求：权限判定是纯内存函数，读取 user.Memberships。
// 如果这里不加载，每次鉴权都会退化成一次额外查询。
func (r *UserRepo) GetWithMemberships(ctx context.Context, id uint) (*model.User, error) {
	var u model.User
	err := conn(ctx, r.db).
		Preload("Memberships").
		Preload("Memberships.Subject").
		First(&u, id).Error
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// GetByUsername 按用户名取用户（登录用，不预加载成员关系）。
func (r *UserRepo) GetByUsername(ctx context.Context, username string) (*model.User, error) {
	var u model.User
	err := conn(ctx, r.db).
		Where("username = ?", username).
		First(&u).Error
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// ExistsUsername 判断用户名是否已被占用（排除指定 ID，用于改名场景）。
func (r *UserRepo) ExistsUsername(ctx context.Context, username string, excludeID uint) (bool, error) {
	q := conn(ctx, r.db).Model(&model.User{}).Where("username = ?", username)
	if excludeID > 0 {
		q = q.Where("id <> ?", excludeID)
	}
	var count int64
	if err := q.Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func (r *UserRepo) Create(ctx context.Context, u *model.User) error {
	if err := conn(ctx, r.db).Create(u).Error; err != nil {
		return fmt.Errorf("创建用户失败: %w", err)
	}
	return nil
}

// UpdateFields 按字段白名单更新用户。
//
// 之所以用 map[string]any 而不是直接 Updates(struct)：
// GORM 的 Updates 传结构体时只更新非零字段，传 map 则精确更新给定字段。
// 更重要的是——字段名在这里是被调用方显式列出的**白名单**，
// 像 is_superuser 这种字段永远不会因为"请求体里带了"就被写进去。
func (r *UserRepo) UpdateFields(ctx context.Context, id uint, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	res := conn(ctx, r.db).
		Model(&model.User{}).
		Where("id = ?", id).
		Updates(fields)
	if res.Error != nil {
		return fmt.Errorf("更新用户失败: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return apperr.ErrNotFound.WithDetail("用户 %d 不存在", id)
	}
	return nil
}

// TouchLogin 记录一次成功登录。使用 SQL 表达式自增，避免读改写竞态。
func (r *UserRepo) TouchLogin(ctx context.Context, id uint, at any) error {
	return conn(ctx, r.db).
		Model(&model.User{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"last_login_at": at,
			"login_count":   gorm.Expr("login_count + 1"),
		}).Error
}

// Count 统计用户总数，用于判断系统是否已完成初始化。
func (r *UserRepo) Count(ctx context.Context) (int64, error) {
	var n int64
	if err := conn(ctx, r.db).Model(&model.User{}).Count(&n).Error; err != nil {
		return 0, err
	}
	return n, nil
}

// CountSuperusers 统计启用状态的管理员数量。
// 用于阻止"把最后一个管理员降级/禁用"导致系统再也无法管理。
func (r *UserRepo) CountSuperusers(ctx context.Context) (int64, error) {
	var n int64
	err := conn(ctx, r.db).
		Model(&model.User{}).
		Where("is_superuser = ? AND is_active = ?", true, true).
		Count(&n).Error
	return n, err
}

// ListFilter 用户列表筛选条件。
type ListFilter struct {
	Keyword string
	// IsActive 为 nil 表示不过滤。
	IsActive *bool
	// SubjectID 大于 0 时只返回该学科的成员。
	SubjectID uint
}

// List 分页查询用户。
func (r *UserRepo) List(ctx context.Context, f ListFilter, limit, offset int) ([]model.User, int64, error) {
	q := conn(ctx, r.db).Model(&model.User{})

	if f.Keyword != "" {
		like := "%" + escapeLike(f.Keyword) + "%"
		q = q.Where("username LIKE ? OR nickname LIKE ?", like, like)
	}
	if f.IsActive != nil {
		q = q.Where("is_active = ?", *f.IsActive)
	}
	if f.SubjectID > 0 {
		q = q.Where("id IN (?)",
			r.db.Model(&model.SubjectMember{}).
				Select("user_id").
				Where("subject_id = ?", f.SubjectID),
		)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var list []model.User
	err := q.Order("id ASC").Limit(limit).Offset(offset).Find(&list).Error
	if err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

// Get 按 ID 取用户（不预加载关联），用于管理场景。
func (r *UserRepo) Get(ctx context.Context, id uint) (*model.User, error) {
	var u model.User
	if err := conn(ctx, r.db).First(&u, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperr.ErrNotFound.WithDetail("用户 %d 不存在", id)
		}
		return nil, err
	}
	return &u, nil
}
