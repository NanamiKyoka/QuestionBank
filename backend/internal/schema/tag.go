package schema

import (
	"time"

	"github.com/NanamiKyoka/questionmind/internal/model"
)

// ---- 标签分类 ----

type TagCategoryCreateRequest struct {
	Name      string `json:"name" binding:"required,min=1,max=64"`
	SortOrder *int   `json:"sort_order"`
}

type TagCategoryUpdateRequest struct {
	Name      *string `json:"name" binding:"omitempty,min=1,max=64"`
	SortOrder *int    `json:"sort_order"`
}

type TagCategoryResponse struct {
	ID        uint   `json:"id"`
	SubjectID uint   `json:"subject_id"`
	Name      string `json:"name"`
	SortOrder int    `json:"sort_order"`

	TagCount int64 `json:"tag_count"`

	CreatedAt time.Time `json:"created_at"`
}

func NewTagCategoryResponse(c *model.TagCategory) TagCategoryResponse {
	return TagCategoryResponse{
		ID:        c.ID,
		SubjectID: c.SubjectID,
		Name:      c.Name,
		SortOrder: c.SortOrder,
		CreatedAt: c.CreatedAt,
	}
}

// ---- 标签 ----

type TagCreateRequest struct {
	// CategoryID 为空表示未分类。
	CategoryID *uint  `json:"category_id"`
	Name       string `json:"name" binding:"required,min=1,max=64"`
	// Color 十六进制色值，如 "#409EFF"。允许为空，前端会用默认色。
	Color     string `json:"color" binding:"omitempty,max=16"`
	SortOrder *int   `json:"sort_order"`
}

type TagUpdateRequest struct {
	Name      *string `json:"name" binding:"omitempty,min=1,max=64"`
	Color     *string `json:"color" binding:"omitempty,max=16"`
	SortOrder *int    `json:"sort_order"`
	// CategoryID 传值表示改到某分类；不传表示保持原分类。
	CategoryID *uint `json:"category_id"`
	// ClearCategory 显式清除分类。
	// 之所以需要这个额外字段：JSON 里 category_id=null 与"字段缺失"反序列化后
	// 都是 nil，无法区分"改成未分类"和"不改分类"。
	ClearCategory bool `json:"clear_category"`
}

type TagResponse struct {
	ID         uint   `json:"id"`
	SubjectID  uint   `json:"subject_id"`
	CategoryID *uint  `json:"category_id"`
	Name       string `json:"name"`
	Color      string `json:"color"`
	SortOrder  int    `json:"sort_order"`

	CategoryName  string `json:"category_name,omitempty"`
	QuestionCount int64  `json:"question_count"`

	CreatedAt time.Time `json:"created_at"`
}

func NewTagResponse(t *model.Tag) TagResponse {
	r := TagResponse{
		ID:         t.ID,
		SubjectID:  t.SubjectID,
		CategoryID: t.CategoryID,
		Name:       t.Name,
		Color:      t.Color,
		SortOrder:  t.SortOrder,
		CreatedAt:  t.CreatedAt,
	}
	if t.Category != nil {
		r.CategoryName = t.Category.Name
	}
	return r
}
