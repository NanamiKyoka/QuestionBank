package model

// Subject 学科。系统的一级业务边界：题目、知识点、标签、试卷都挂在学科下。
type Subject struct {
	SoftBase

	Name string `gorm:"size:64;uniqueIndex;not null" json:"name"`
	// Code 英文标识，用于 URL 与导出文件名。可为空。
	// 刻意用普通索引而非唯一索引：空字符串不是 NULL，唯一索引会导致
	// "第二个不填 code 的学科"插入失败。唯一性由 service 层校验（仅当非空时）。
	Code        string `gorm:"size:48;index" json:"code"`
	Description string `gorm:"size:255" json:"description"`
	// ReviewThreshold 该学科的题目需要审核通过几次才自动发布。
	ReviewThreshold int `gorm:"not null;default:1" json:"review_threshold"`
	SortOrder       int `gorm:"not null;default:0" json:"sort_order"`

	CreatedBy uint `gorm:"not null;index" json:"created_by"`

	Members []SubjectMember `gorm:"foreignKey:SubjectID" json:"-"`
}

func (Subject) TableName() string { return "subjects" }

// SubjectMember 学科成员关系：用户 × 学科 → 角色。
//
// (user_id, subject_id) 唯一，保证一个用户在一个学科里只有一个角色。
// 注意：这里没有"所有者"角色——学科负责人就是 role=manager。
// CreatedBy 只记录创建者，不隐含任何权限。
type SubjectMember struct {
	Base

	UserID    uint `gorm:"not null;uniqueIndex:uk_subject_member,priority:1;index" json:"user_id"`
	SubjectID uint `gorm:"not null;uniqueIndex:uk_subject_member,priority:2;index" json:"subject_id"`

	Role SubjectRole `gorm:"size:16;not null" json:"role"`

	User    *User    `gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE" json:"user,omitempty"`
	Subject *Subject `gorm:"foreignKey:SubjectID;constraint:OnDelete:CASCADE" json:"-"`
}

func (SubjectMember) TableName() string { return "subject_members" }

// RoleInSubject 在已加载的成员关系中查找某学科的角色。
// 纯内存操作，不触发查询——这是权限判定的性能前提。
func (u *User) RoleInSubject(subjectID uint) (SubjectRole, bool) {
	for _, m := range u.Memberships {
		if m.SubjectID == subjectID {
			return m.Role, true
		}
	}
	return "", false
}

// SubjectIDs 返回用户可访问的学科集合。
// 返回 (nil, true) 表示不受限（超级管理员）。
func (u *User) SubjectIDs() (map[uint]bool, bool) {
	if u.IsSuperuser {
		return nil, true
	}
	ids := make(map[uint]bool, len(u.Memberships))
	for _, m := range u.Memberships {
		ids[m.SubjectID] = true
	}
	return ids, false
}

// Can 权限判定唯一入口。
//
//	user.IsSuperuser         → 全部通过
//	subjectID == 0           → 与学科无关的动作（如系统设置），仅超管
//	否则                      → 查角色 → 查角色权限表
func (u *User) Can(perm Permission, subjectID uint) bool {
	if u == nil {
		return false
	}
	if u.IsSuperuser {
		return true
	}
	if subjectID == 0 {
		return false
	}
	role, ok := u.RoleInSubject(subjectID)
	if !ok {
		return false
	}
	return PermissionsOf(role)[perm]
}

// CanAny 判断用户在其中任一学科拥有该权限（用于"我能否创建题目"这类检查）。
func (u *User) CanAny(perm Permission) bool {
	if u == nil {
		return false
	}
	if u.IsSuperuser {
		return true
	}
	for _, m := range u.Memberships {
		if PermissionsOf(m.Role)[perm] {
			return true
		}
	}
	return false
}
