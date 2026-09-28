package model

import "time"

// KnowledgePoint 知识点，按学科组织的树。
//
// 树形存储采用「自引用父指针 + 物化路径」：
//   - ParentID 表达层级关系，便于前端渲染树；
//   - Path 形如 "/1/7/23/"，用于一条 SQL 取出整棵子树
//     （WHERE path LIKE '/1/7/%'），避免递归查询。
//
// 两个字段在写入时由服务层统一维护，保证一致。
type KnowledgePoint struct {
	SoftBase

	SubjectID uint `gorm:"not null;index:idx_kp_subject_parent,priority:1" json:"subject_id"`
	// ParentID 为空表示根节点。
	ParentID *uint `gorm:"index:idx_kp_subject_parent,priority:2" json:"parent_id"`

	Name        string `gorm:"size:128;not null" json:"name"`
	Description string `gorm:"size:512" json:"description"`

	// Path 物化路径，形如 "/1/7/23/"。根节点为 "/{id}/"。
	Path string `gorm:"size:512;index" json:"path"`
	// Level 层级，根为 1。
	Level int `gorm:"not null;default:1" json:"level"`

	SortOrder int  `gorm:"not null;default:0" json:"sort_order"`
	CreatedBy uint `gorm:"not null" json:"created_by"`

	Children []KnowledgePoint `gorm:"foreignKey:ParentID" json:"children,omitempty"`
}

func (KnowledgePoint) TableName() string { return "knowledge_points" }

// QuestionKnowledgePoint 题目-知识点 多对多关联。
// 显式定义关联表（而非依赖 GORM 的 many2many 自动建表），
// 这样关联表也能带创建时间，便于统计。
//
// 注意：刻意不嵌入 Base——Base 带自增主键 id，会与这里的复合主键冲突。
type QuestionKnowledgePoint struct {
	QuestionID       uint      `gorm:"primaryKey" json:"question_id"`
	KnowledgePointID uint      `gorm:"primaryKey;index" json:"knowledge_point_id"`
	CreatedAt        time.Time `json:"created_at"`
}

func (QuestionKnowledgePoint) TableName() string { return "question_knowledge_points" }
