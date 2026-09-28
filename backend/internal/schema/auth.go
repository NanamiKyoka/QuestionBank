// Package schema 定义 HTTP 请求与响应结构（DTO）。
//
// 与 model 分开的意义：
//  1. 请求结构可以做**字段白名单**——这是防止越权提权的第一道防线；
//  2. 响应结构决定对外暴露什么，密码哈希等字段天然无法被带出去；
//  3. 数据模型可以随业务演进，而不破坏对外契约。
package schema

import (
	"time"

	"github.com/NanamiKyoka/questionmind/internal/model"
)

// ---- 登录 ----

// LoginRequest 登录请求。
// 同时支持 JSON 与表单（form tag），便于 curl / 前端 / 测试脚本调用。
type LoginRequest struct {
	Username string `json:"username" form:"username" binding:"required,min=1,max=64"`
	Password string `json:"password" form:"password" binding:"required,min=1,max=72"`
}

// LoginResponse 登录响应。
type LoginResponse struct {
	AccessToken string       `json:"access_token"`
	TokenType   string       `json:"token_type"`
	ExpiresAt   time.Time    `json:"expires_at"`
	User        UserResponse `json:"user"`
}

// ---- 用户 ----

// UserResponse 对外暴露的用户信息。
// 注意这里没有 password_hash：DTO 层面就不可能泄露。
type UserResponse struct {
	ID          uint       `json:"id"`
	Username    string     `json:"username"`
	Nickname    string     `json:"nickname"`
	Email       string     `json:"email"`
	Avatar      string     `json:"avatar"`
	IsSuperuser bool       `json:"is_superuser"`
	IsActive    bool       `json:"is_active"`
	LastLoginAt *time.Time `json:"last_login_at"`
	LoginCount  int        `json:"login_count"`

	LastSubjectID *uint `json:"last_subject_id"`

	CreatedAt time.Time `json:"created_at"`

	// Memberships 当前用户在各学科的角色，前端据此决定按钮显隐。
	// 仅在需要时填充（列表接口不填，避免响应膨胀）。
	Memberships []MembershipResponse `json:"memberships,omitempty"`
}

// MembershipResponse 学科成员关系。
type MembershipResponse struct {
	SubjectID   uint              `json:"subject_id"`
	SubjectName string            `json:"subject_name"`
	SubjectCode string            `json:"subject_code"`
	Role        model.SubjectRole `json:"role"`
	RoleLabel   string            `json:"role_label"`
	// Permissions 该学科下生效的动作权限列表，前端据此控制界面。
	Permissions []model.Permission `json:"permissions"`
}

// NewUserResponse 从模型构造响应。withMemberships 决定是否带出学科角色。
func NewUserResponse(u *model.User, withMemberships bool) UserResponse {
	r := UserResponse{
		ID:            u.ID,
		Username:      u.Username,
		Nickname:      u.Nickname,
		Email:         u.Email,
		Avatar:        u.Avatar,
		IsSuperuser:   u.IsSuperuser,
		IsActive:      u.IsActive,
		LastLoginAt:   u.LastLoginAt,
		LoginCount:    u.LoginCount,
		LastSubjectID: u.LastSubjectID,
		CreatedAt:     u.CreatedAt,
	}
	if withMemberships {
		r.Memberships = make([]MembershipResponse, 0, len(u.Memberships))
		for _, m := range u.Memberships {
			perms := make([]model.Permission, 0, 6)
			for p, ok := range model.PermissionsOf(m.Role) {
				if ok {
					perms = append(perms, p)
				}
			}
			item := MembershipResponse{
				SubjectID:   m.SubjectID,
				Role:        m.Role,
				RoleLabel:   m.Role.Label(),
				Permissions: perms,
			}
			if m.Subject != nil {
				item.SubjectName = m.Subject.Name
				item.SubjectCode = m.Subject.Code
			}
			r.Memberships = append(r.Memberships, item)
		}
	}
	return r
}

// UpdateMeRequest 普通用户修改自己的字段白名单。
//
// ⚠️ 安全设计要点：这里**刻意不包含** username / is_superuser / is_active。
// 参考实现中曾出现"更新自己"的接口直接接收完整用户对象、导致任意用户
// 可以把 is_superuser 置为 true 的越权漏洞。用独立 DTO + 指针字段
// （区分"没传"与"传了空值"）可以从类型层面杜绝这类问题。
type UpdateMeRequest struct {
	Nickname *string `json:"nickname" binding:"omitempty,max=64"`
	Email    *string `json:"email" binding:"omitempty,email,max=128"`
	Avatar   *string `json:"avatar" binding:"omitempty,max=255"`
}

// Fields 转换为更新字段映射，只包含请求中显式出现的字段。
func (r *UpdateMeRequest) Fields() map[string]any {
	f := make(map[string]any, 3)
	if r.Nickname != nil {
		f["nickname"] = *r.Nickname
	}
	if r.Email != nil {
		f["email"] = *r.Email
	}
	if r.Avatar != nil {
		f["avatar"] = *r.Avatar
	}
	return f
}

// UpdateLastSubjectRequest 记录用户当前工作的学科。
type UpdateLastSubjectRequest struct {
	// SubjectID 为 0 表示清除。
	SubjectID uint `json:"subject_id"`
}

// ChangePasswordRequest 修改密码。
type ChangePasswordRequest struct {
	CurrentPassword string `json:"current_password" binding:"required"`
	// 上限 72 是 bcrypt 的输入长度限制：超过部分会被静默截断，
	// 与其让用户以为设了长密码，不如直接拒绝。
	NewPassword string `json:"new_password" binding:"required,min=8,max=72"`
}

// ---- 初始化向导 ----

// SetupStatusResponse 首次运行状态。前端据此决定是否跳转到初始化向导。
type SetupStatusResponse struct {
	// NeedsSetup 为 true 表示系统还没有任何用户，必须先初始化。
	NeedsSetup bool `json:"needs_setup"`
	// RegistrationEnabled 是否开放自助注册。
	RegistrationEnabled bool   `json:"registration_enabled"`
	AppName             string `json:"app_name"`
	Version             string `json:"version"`
}

// SetupRequest 初始化请求：创建第一个管理员账号。
type SetupRequest struct {
	Username string `json:"username" binding:"required,min=3,max=64,alphanum"`
	Password string `json:"password" binding:"required,min=8,max=72"`
	Nickname string `json:"nickname" binding:"omitempty,max=64"`
	Email    string `json:"email" binding:"omitempty,email,max=128"`

	// FirstSubjectName 可选：同时创建一个默认学科，省去后续手工建。
	FirstSubjectName string `json:"first_subject_name" binding:"omitempty,max=64"`
}
