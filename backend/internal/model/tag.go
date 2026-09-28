package model

import "time"

// TagCategory 标签分类，用于给标签分组（如"能力层级""教材章节"）。
// CategoryID 为空的标签视为"未分类"。
type TagCategory struct {
	SoftBase

	SubjectID uint   `gorm:"not null;uniqueIndex:uk_tagcat_subject_name,priority:1" json:"subject_id"`
	Name      string `gorm:"size:64;not null;uniqueIndex:uk_tagcat_subject_name,priority:2" json:"name"`

	SortOrder int `gorm:"not null;default:0" json:"sort_order"`
}

func (TagCategory) TableName() string { return "tag_categories" }

// Tag 标签。同一学科内标签名唯一。
type Tag struct {
	SoftBase

	SubjectID uint `gorm:"not null;uniqueIndex:uk_tag_subject_name,priority:1;index" json:"subject_id"`
	// CategoryID 为空表示未分类。
	CategoryID *uint `gorm:"index" json:"category_id"`

	Name string `gorm:"size:64;not null;uniqueIndex:uk_tag_subject_name,priority:2" json:"name"`
	// Color 十六进制色值，如 "#409EFF"。
	Color string `gorm:"size:16" json:"color"`

	SortOrder int `gorm:"not null;default:0" json:"sort_order"`

	Category *TagCategory `gorm:"foreignKey:CategoryID" json:"category,omitempty"`
}

func (Tag) TableName() string { return "tags" }

// QuestionTag 题目-标签 多对多关联。
// 同样不嵌入 Base，避免复合主键与自增 id 冲突。
type QuestionTag struct {
	QuestionID uint      `gorm:"primaryKey" json:"question_id"`
	TagID      uint      `gorm:"primaryKey;index" json:"tag_id"`
	CreatedAt  time.Time `json:"created_at"`
}

func (QuestionTag) TableName() string { return "question_tags" }
