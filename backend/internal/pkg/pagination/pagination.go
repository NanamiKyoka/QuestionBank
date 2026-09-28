package pagination

import "github.com/gin-gonic/gin"

const (
	defaultPage     = 1
	defaultPageSize = 20
	maxPageSize     = 200
	// allPageSize 表示"不分页，取全部"，但仍受 maxAll 上限保护。
	allPageSize = -1
	maxAll      = 5000
)

// Query 分页参数。page_size 传 0 用默认值，传负数表示取全部。
type Query struct {
	Page     int `form:"page"`
	PageSize int `form:"page_size"`
}

// Parse 解析查询参数并规整到合法范围。
func Parse(c *gin.Context) Query {
	var q Query
	_ = c.ShouldBindQuery(&q)
	q.normalize()
	return q
}

func (q *Query) normalize() {
	if q.Page < 1 {
		q.Page = defaultPage
	}
	switch {
	case q.PageSize == 0:
		q.PageSize = defaultPageSize
	case q.PageSize > maxPageSize:
		q.PageSize = maxPageSize
	case q.PageSize < 0:
		q.PageSize = maxAll
	}
}

func (q Query) Limit() int { return q.PageSize }

func (q Query) Offset() int {
	if q.PageSize == maxAll {
		return 0
	}
	return (q.Page - 1) * q.PageSize
}

func (q Query) IsAll() bool { return q.PageSize == allPageSize || q.PageSize == maxAll }
