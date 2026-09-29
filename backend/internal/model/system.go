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

	StatusCode int `json:"status_code"`
	DurationMs int `json:"duration_ms"`
	// Success 刻意**不设 default**。
	//
	// ⚠️ GORM 的坑：带 default 标签的字段在插入时会跳过零值，
	// 让数据库去填默认值。于是 success=false 会被丢掉、落库成 true——
	// 审计日志就会声称每一次失败的操作都成功了，而且完全静默。
	// 这个字段的唯一写入方（审计中间件）总是显式赋值，因此不需要默认值。
	Success bool `gorm:"not null" json:"success"`

	// Detail 额外上下文（错误信息、变更字段名等）。
	Detail JSON `json:"detail,omitempty"`
}

func (ActivityLog) TableName() string { return "activity_logs" }

// MaxImportAttempt 单个导入任务允许的最大执行次数（含首次）。
//
// 重试的代价是一次模型调用加几分钟等待，不设上限的话，
// 一个必定失败的输入（扫描件全是空白页之类）能把 AI 额度耗光。
const MaxImportAttempt = 3

// ImportTask 导入任务。
//
// **现状（没有后台 worker）**：抽取是同步的——HTTP 请求线程里跑完模型调用再返回。
// 因此任务的真实生命周期是
//
//	pending → processing → completed / failed
//	              ↘ cancelled（放弃，不是中断）
//
// pending/processing 只有在两个地方出现：
//   - 抽取/入库开始前先建行，让长达数分钟的等待在历史里看得见；
//   - 用户点"重试"把任务重新放回 pending。
//
// 将来接入 worker（见 model/worker 阶段的规划）时，pending 才是真正的排队，
// processing 才由 worker 认领时写入。
//
// Source 字段保留用于将来区分「同步审计记录」与「真正的后台任务」，
// 避免 worker 误领同步记录。
type ImportTask struct {
	SoftBase

	SubjectID *uint `gorm:"index" json:"subject_id"`
	UserID    uint  `gorm:"not null;index" json:"user_id"`

	Source ImportSource     `gorm:"size:16;not null;index" json:"source"`
	Status ImportTaskStatus `gorm:"size:16;not null;index:idx_import_status,priority:1" json:"status"`

	// OriginalFilename 来源文件名。
	//
	// 目前导入只有「粘贴文本 / 拍照识题」两条路，拿不到真实文件名，
	// 落库的是 service 生成的可读描述（如「图片识别（3 张）」）。
	// 将来接入文件上传时，这里会换成用户上传时的原始文件名。
	OriginalFilename string `gorm:"size:255" json:"original_filename"`
	// FilePath 落盘路径。**不对外暴露**（schema.ImportTaskResponse 不投影它）：
	// 它是服务器内部布局，回显给客户端等于泄露部署细节。
	// 它真正的用途是删除任务时清理磁盘上的文件。
	FilePath string `gorm:"size:512" json:"file_path"`
	// FileType: docx | markdown | image | text | zip
	FileType string `gorm:"size:16" json:"file_type"`
	// Mode: extract（抽取新题） | solve（补写答案） | commit（审核入库）
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

// ResultMap 解出 Result JSON 的顶层对象。
//
// Result 是个多用途的 JSON 列：排队任务在里面记解析方式，
// 抽取完成后又存下整份草稿。刻意不在 model 里给出 typed 读取——
// model 不能依赖 schema（会成环），而在这里硬编码一份结构同样会把
// 存储格式焊死在模型上。读法交给 service。
func (t *ImportTask) ResultMap() map[string]any {
	if t.Result.IsEmpty() {
		return nil
	}
	var m map[string]any
	if err := t.Result.Into(&m); err != nil {
		return nil
	}
	return m
}

// ResultString 读 Result 里某个字符串字段，取不到返回空串。
func (t *ImportTask) ResultString(key string) string {
	m := t.ResultMap()
	if m == nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}

// CanRetry 判断任务是否允许重试：失败或已取消，且未超过上限。
func (t *ImportTask) CanRetry(maxAttempt int) bool {
	if t.Status != ImportStatusFailed && t.Status != ImportStatusCancelled {
		return false
	}
	return t.Attempt < maxAttempt
}

// Retryable 是否允许重新排队。
//
// 比 CanRetry 多一类：**已完成但一题都没抽出来**。
// CanRetry 只看状态，会把这类任务判成"已经完成，没什么可重试的"，
// 于是用户只能把同一份内容重新粘一遍、重新等一次模型——
// 而他很可能只是想换个模型或修一下原文。
//
// 同样受 maxAttempt 限制：每次真正执行都会让 Attempt +1（见 MarkProcessing），
// 所以这里不会变成一个可以无限点的按钮。
func (t *ImportTask) Retryable(maxAttempt int) bool {
	if t.CanRetry(maxAttempt) {
		return true
	}
	return t.Status == ImportStatusCompleted && t.TotalCount == 0 && t.Attempt < maxAttempt
}

// IsActive 是否仍处在「可以被放弃」的状态。
//
// 只有等待处理与处理中两种状态算活动。completed / failed / cancelled 都是终态，
// 对它们调用取消会返回 409，而不是悄悄把一条历史记录改成"已取消"。
func (t *ImportTask) IsActive() bool {
	return t.Status == ImportStatusPending || t.Status == ImportStatusProcessing
}

// MarkProcessing 状态流转：→ processing。
//
// Attempt 在这里而不是在"创建任务"时 +1：它记的是"**已经跑过几次**"，
// 而不是"排过几次队"。重试只是把任务放回 pending，尚未消耗任何代价。
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

// MarkCancelled 状态流转：→ cancelled（放弃）。
//
// ⚠️ 语义边界，必须说清楚：这不是"中断一次正在进行的抽取"。
// 抽取是同步的，服务端没有可抢占的执行体——用户点取消时，
// 那次 HTTP 请求要么已经返回、要么还在浏览器里跑着，取消按钮影响不到它。
// 取消能做的是：**把这条任务记录标记为已放弃**，
// 于是它不再出现在队列里（IsActive 为假），也不会被将来的 worker 认领。
//
// 真正的中断需要后台 worker + 协作式取消（见 handler/import.go 的路由注释）。
func (t *ImportTask) MarkCancelled(reason string) {
	now := time.Now().UTC()
	t.Status = ImportStatusCancelled
	if reason != "" {
		t.ErrorMsg = reason
	}
	t.FinishedAt = &now
}

// MarkRetried 状态流转：→ pending（重新排队等待执行）。
//
// 清空上一轮的失败原因与结构化结果，否则重试成功后界面还会显示旧的报错；
// StartedAt/FinishedAt 一并清零，让"排队中"这一状态不带任何陈旧的时间戳。
//
// 不在这里 +1 Attempt：重新排队本身没有消耗任何执行代价，
// 真正的计数发生在 MarkProcessing。
func (t *ImportTask) MarkRetried() {
	t.Status = ImportStatusPending
	t.ErrorMsg = ""
	t.Result = nil
	t.StartedAt = nil
	t.FinishedAt = nil
}
