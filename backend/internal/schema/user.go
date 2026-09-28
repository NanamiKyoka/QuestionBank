package schema

import (
	"github.com/NanamiKyoka/questionmind/internal/model"
)

// UserCreateRequest 管理员新建用户。
//
// 与 UpdateMeRequest 分开是刻意的：只有管理员接口才能设置 is_superuser / is_active。
// 「更新自己」的接口用另一份不含这些字段的 DTO，从类型层面杜绝越权提权。
type UserCreateRequest struct {
	Username string `json:"username" binding:"required,min=3,max=64,alphanum"`
	Password string `json:"password" binding:"required,min=8,max=72"`
	Nickname string `json:"nickname" binding:"omitempty,max=64"`
	Email    string `json:"email" binding:"omitempty,email,max=128"`

	IsSuperuser bool  `json:"is_superuser"`
	IsActive    *bool `json:"is_active"`

	// 可选：创建的同时把用户加入某个学科并赋予角色。
	SubjectID   *uint              `json:"subject_id"`
	SubjectRole *model.SubjectRole `json:"subject_role"`
}

// UserAdminUpdateRequest 管理员修改他人。
type UserAdminUpdateRequest struct {
	Nickname    *string `json:"nickname" binding:"omitempty,max=64"`
	Email       *string `json:"email" binding:"omitempty,email,max=128"`
	Avatar      *string `json:"avatar" binding:"omitempty,max=255"`
	IsActive    *bool   `json:"is_active"`
	IsSuperuser *bool   `json:"is_superuser"`
}

// ResetPasswordRequest 管理员重置他人密码。
type ResetPasswordRequest struct {
	NewPassword string `json:"new_password" binding:"required,min=8,max=72"`
}

// UserListQuery 用户列表筛选。
type UserListQuery struct {
	Keyword   string `form:"keyword"`
	IsActive  *bool  `form:"is_active"`
	SubjectID uint   `form:"subject_id"`
}

// MyPermissionsResponse 当前用户的完整权限视图。
//
// 前端把它当作权限判定的**唯一真相源**：不在客户端重新推导角色，
// 避免前后端各算一套导致按钮显隐与后端门禁不一致。
type MyPermissionsResponse struct {
	IsSuperuser bool `json:"is_superuser"`

	// AccessibleSubjectIDs 可访问的学科；为 null 表示不受限（管理员）。
	AccessibleSubjectIDs []uint `json:"accessible_subject_ids"`

	// Memberships 各学科的角色与生效权限。
	Memberships []MembershipResponse `json:"memberships"`

	// Capabilities 与学科无关的全局能力，如 manage_users、manage_ai。
	Capabilities []string `json:"capabilities"`
}

// UserDetailResponse 管理视角的用户信息，附带学科角色。
type UserDetailResponse struct {
	UserResponse
	Memberships []MembershipResponse `json:"memberships"`
}

// NewUserDetailResponse 组装管理视角的用户信息。
func NewUserDetailResponse(u *model.User) UserDetailResponse {
	base := NewUserResponse(u, false)
	items := make([]MembershipResponse, 0, len(u.Memberships))
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
		items = append(items, item)
	}
	return UserDetailResponse{UserResponse: base, Memberships: items}
}

// 全局能力常量。与学科无关的管理动作在此登记，前端据此显隐入口。
const (
	CapManageUsers = "manage_users"
	CapManageAI    = "manage_ai"
	CapViewAudit   = "view_audit"
)

// GlobalCapabilities 返回用户的全局能力列表。
func GlobalCapabilities(u *model.User) []string {
	if u == nil || !u.IsSuperuser {
		return []string{}
	}
	return []string{CapManageUsers, CapManageAI, CapViewAudit}
}
