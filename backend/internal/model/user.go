package model

import "time"

// User 用户。
//
// 安全要点：PasswordHash 用 json:"-" 保证任何情况下都不会被序列化出去。
// 角色的修改只能由管理员通过专门接口进行，普通用户的"更新自己"接口
// 使用独立的 DTO 白名单，从设计上杜绝越权提权。
type User struct {
	SoftBase

	Username     string `gorm:"size:64;uniqueIndex;not null" json:"username"`
	PasswordHash string `gorm:"size:255;not null" json:"-"`

	Nickname string `gorm:"size:64" json:"nickname"`
	Email    string `gorm:"size:128" json:"email"`
	Avatar   string `gorm:"size:255" json:"avatar"`

	IsSuperuser bool `gorm:"not null;default:false" json:"is_superuser"`
	IsActive    bool `gorm:"not null;default:true" json:"is_active"`

	LastLoginAt *time.Time `json:"last_login_at"`
	LoginCount  int        `gorm:"not null;default:0" json:"login_count"`

	// LastSubjectID 记住用户上次工作的学科，登录后直接进入该学科。
	LastSubjectID *uint `json:"last_subject_id"`

	// Memberships 学科成员关系。鉴权是纯内存计算，因此查询用户时
	// 必须 Preload 本字段，避免在权限判定里触发 N+1 查询。
	Memberships []SubjectMember `gorm:"foreignKey:UserID" json:"memberships,omitempty"`
}

func (User) TableName() string { return "users" }

// DisplayName 返回展示名，昵称为空时回落用户名。
func (u *User) DisplayName() string {
	if u.Nickname != "" {
		return u.Nickname
	}
	return u.Username
}
