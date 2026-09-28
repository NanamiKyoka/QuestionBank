package model

import "time"

// SystemSetting 系统设置：键值对形式的动态配置。
//
// 与 config.Config 的分工：
//   - config.Config 是**启动级**配置（监听端口、数据库地址），改了要重启；
//   - SystemSetting 是**业务级**配置（AI 提示词、是否开放注册），界面上即可改。
//
// 数据库连接串之所以不能放这里：它本身就是"要连哪个数据库"的前置条件。
type SystemSetting struct {
	Base

	// Key 列名显式指定为 setting_key：key 是 MySQL 保留字。
	Key   string `gorm:"column:setting_key;size:64;not null;uniqueIndex" json:"key"`
	Value string `gorm:"type:text" json:"value"`

	Description string `gorm:"size:255" json:"description"`
	UpdatedBy   uint   `json:"updated_by"`
}

func (SystemSetting) TableName() string { return "system_settings" }

// ActivityLog 操作审计日志。
type ActivityLog struct {
	Base

	UserID *uint `gorm:"index" json:"user_id"`
	// Username 冗余存储：用户被删除后日志依然可读。
	Username string `gorm:"size:64;index" json:"username"`

	// Action 语义化动作名，如 question.create、paper.export。
	Action string `gorm:"size:64;not null;index" json:"action"`
	// Resource + ResourceID 定位被操作的资源。
	Resource   string `gorm:"size:32;index" json:"resource"`
	ResourceID string `gorm:"size:64;index" json:"resource_id"`

	Method string `gorm:"size:8" json:"method"`
	Path   string `gorm:"size:255" json:"path"`

	IP        string `gorm:"size:64" json:"ip"`
	UserAgent string `gorm:"size:255" json:"user_agent"`

	StatusCode int  `json:"status_code"`
	DurationMs int  `json:"duration_ms"`
	Success    bool `gorm:"not null;default:true" json:"success"`

	// Detail 额外上下文（错误信息、变更字段名等）。
	Detail JSON `json:"detail,omitempty"`
}

func (ActivityLog) TableName() string { return "activity_logs" }

// ImportTask 导入任务。
//
// 有两条使用路径：
//   - 后台任务：Status 从 pending 开始，由 worker 认领并执行（智能录题的上传解析）；
//   - 同步审计：整卷导入等同步完成的操作，直接以 completed 落库，仅作记录。
//
// 用 Source 字段区分二者，避免 worker 误领同步记录。
type ImportTask struct {
	SoftBase

	SubjectID *uint `gorm:"index" json:"subject_id"`
	UserID    uint  `gorm:"not null;index" json:"user_id"`

	Source ImportSource     `gorm:"size:16;not null;index" json:"source"`
	Status ImportTaskStatus `gorm:"size:16;not null;index:idx_import_status,priority:1" json:"status"`

	OriginalFilename string `gorm:"size:255" json:"original_filename"`
	FilePath         string `gorm:"size:512" json:"file_path"`
	// FileType: docx | markdown | image | text | zip
	FileType string `gorm:"size:16" json:"file_type"`
	// Mode: extract（抽取新题） | solve（补写答案）
	Mode string `gorm:"size:16" json:"mode"`

	TotalCount  int `gorm:"not null;default:0" json:"total_count"`
	SavedCount  int `gorm:"not null;default:0" json:"saved_count"`
	FailedCount int `gorm:"not null;default:0" json:"failed_count"`

	ErrorMsg string `gorm:"type:text" json:"error_message,omitempty"`
	// Result 结构化结果（process_task_id、逐条失败原因等）。
	Result JSON `json:"result,omitempty"`

	// Attempt 已尝试次数，配合重试策略使用（避免无限重试）。
	Attempt int `gorm:"not null;default:0" json:"attempt"`

	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
}

func (ImportTask) TableName() string { return "import_tasks" }

// CanRetry 判断任务是否允许重试：失败或已取消，且未超过上限。
func (t *ImportTask) CanRetry(maxAttempt int) bool {
	if t.Status != ImportStatusFailed && t.Status != ImportStatusCancelled {
		return false
	}
	return t.Attempt < maxAttempt
}

// MarkProcessing 状态流转：→ processing。
func (t *ImportTask) MarkProcessing() {
	now := time.Now().UTC()
	t.Status = ImportStatusProcessing
	t.StartedAt = &now
	t.Attempt++
}

// MarkCompleted 状态流转：→ completed。
func (t *ImportTask) MarkCompleted() {
	now := time.Now().UTC()
	t.Status = ImportStatusCompleted
	t.FinishedAt = &now
}

// MarkFailed 状态流转：→ failed，并记录原因。
func (t *ImportTask) MarkFailed(reason string) {
	now := time.Now().UTC()
	t.Status = ImportStatusFailed
	t.ErrorMsg = reason
	t.FinishedAt = &now
}
