package schema

import (
	"time"

	"github.com/NanamiKyoka/questionmind/internal/model"
)

// SubjectCreateRequest 新建学科。
type SubjectCreateRequest struct {
	Name        string `json:"name" binding:"required,min=1,max=64"`
	Code        string `json:"code" binding:"omitempty,max=48,alphanum"`
	Description string `json:"description" binding:"omitempty,max=255"`
	// ReviewThreshold 审核通过多少次才自动发布，默认 1。
	ReviewThreshold *int `json:"review_threshold" binding:"omitempty,min=1,max=10"`
	SortOrder       *int `json:"sort_order"`
}

// SubjectUpdateRequest 更新学科。全部字段可选，未传的保持不变。
type SubjectUpdateRequest struct {
	Name            *string `json:"name" binding:"omitempty,min=1,max=64"`
	Code            *string `json:"code" binding:"omitempty,max=48,alphanum"`
	Description     *string `json:"description" binding:"omitempty,max=255"`
	ReviewThreshold *int    `json:"review_threshold" binding:"omitempty,min=1,max=10"`
	SortOrder       *int    `json:"sort_order"`
}

// subjectFields 把请求转换为字段映射，只包含显式传入的字段。
func (r *SubjectCreateRequest) Fields(createdBy uint) map[string]any {
	f := map[string]any{
		"name":        r.Name,
		"code":        r.Code,
		"description": r.Description,
		"created_by":  createdBy,
	}
	f["review_threshold"] = 1
	if r.ReviewThreshold != nil {
		f["review_threshold"] = *r.ReviewThreshold
	}
	if r.SortOrder != nil {
		f["sort_order"] = *r.SortOrder
	}
	return f
}

func (r *SubjectUpdateRequest) Fields() map[string]any {
	f := make(map[string]any, 5)
	if r.Name != nil {
		f["name"] = *r.Name
	}
	if r.Code != nil {
		f["code"] = *r.Code
	}
	if r.Description != nil {
		f["description"] = *r.Description
	}
	if r.ReviewThreshold != nil {
		f["review_threshold"] = *r.ReviewThreshold
	}
	if r.SortOrder != nil {
		f["sort_order"] = *r.SortOrder
	}
	return f
}

// SubjectResponse 学科响应。
type SubjectResponse struct {
	ID              uint   `json:"id"`
	Name            string `json:"name"`
	Code            string `json:"code"`
	Description     string `json:"description"`
	ReviewThreshold int    `json:"review_threshold"`
	SortOrder       int    `json:"sort_order"`
	CreatedBy       uint   `json:"created_by"`

	// Role 当前用户在该学科的角色；管理员为 manager。
	Role      model.SubjectRole `json:"role"`
	RoleLabel string            `json:"role_label"`

	CreatedAt time.Time `json:"created_at"`
}

func NewSubjectResponse(s *model.Subject, role model.SubjectRole) SubjectResponse {
	return SubjectResponse{
		ID:              s.ID,
		Name:            s.Name,
		Code:            s.Code,
		Description:     s.Description,
		ReviewThreshold: s.ReviewThreshold,
		SortOrder:       s.SortOrder,
		CreatedBy:       s.CreatedBy,
		Role:            role,
		RoleLabel:       role.Label(),
		CreatedAt:       s.CreatedAt,
	}
}

// MemberUpsertRequest 添加或修改学科成员。
// UserID 由路由路径提供，handler 在绑定后覆写，因此不参与请求体校验。
type MemberUpsertRequest struct {
	UserID uint              `json:"user_id"`
	Role   model.SubjectRole `json:"role" binding:"required,oneof=viewer editor manager"`
}

// MemberResponse 学科成员。
type MemberResponse struct {
	UserID   uint   `json:"user_id"`
	Username string `json:"username"`
	Nickname string `json:"nickname"`
	Avatar   string `json:"avatar"`

	Role      model.SubjectRole `json:"role"`
	RoleLabel string            `json:"role_label"`

	JoinedAt time.Time `json:"joined_at"`
}

func NewMemberResponse(m *model.SubjectMember) MemberResponse {
	r := MemberResponse{
		UserID:    m.UserID,
		Role:      m.Role,
		RoleLabel: m.Role.Label(),
		JoinedAt:  m.CreatedAt,
	}
	if m.User != nil {
		r.Username = m.User.Username
		r.Nickname = m.User.Nickname
		r.Avatar = m.User.Avatar
	}
	return r
}
