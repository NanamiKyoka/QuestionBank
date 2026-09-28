package schema

import (
	"time"

	"github.com/NanamiKyoka/questionmind/internal/model"
)

// ActivityLogQuery 审计日志筛选参数。
type ActivityLogQuery struct {
	UserID   *uint      `form:"user_id"`
	Action   string     `form:"action"`
	Resource string     `form:"resource"`
	Keyword  string     `form:"keyword"`
	Success  *bool      `form:"success"`
	Since    *time.Time `form:"since"`
	Until    *time.Time `form:"until"`
}

// ActivityLogResponse 审计日志条目。
type ActivityLogResponse struct {
	ID       uint   `json:"id"`
	UserID   *uint  `json:"user_id"`
	Username string `json:"username"`

	Action     string `json:"action"`
	Resource   string `json:"resource"`
	ResourceID string `json:"resource_id"`

	Method string `json:"method"`
	Path   string `json:"path"`

	IP        string `json:"ip"`
	UserAgent string `json:"user_agent"`

	StatusCode int  `json:"status_code"`
	DurationMs int  `json:"duration_ms"`
	Success    bool `json:"success"`

	Detail model.JSON `json:"detail,omitempty"`

	CreatedAt time.Time `json:"created_at"`
}

// NewActivityLogResponse 组装响应。
func NewActivityLogResponse(l *model.ActivityLog) ActivityLogResponse {
	return ActivityLogResponse{
		ID:         l.ID,
		UserID:     l.UserID,
		Username:   l.Username,
		Action:     l.Action,
		Resource:   l.Resource,
		ResourceID: l.ResourceID,
		Method:     l.Method,
		Path:       l.Path,
		IP:         l.IP,
		UserAgent:  l.UserAgent,
		StatusCode: l.StatusCode,
		DurationMs: l.DurationMs,
		Success:    l.Success,
		Detail:     l.Detail,
		CreatedAt:  l.CreatedAt,
	}
}

// ActivityActionsResponse 可用的动作名列表。
type ActivityActionsResponse struct {
	Actions []string `json:"actions"`
}
