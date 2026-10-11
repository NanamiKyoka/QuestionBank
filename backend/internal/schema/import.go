package schema

import (
	"time"

	"github.com/NanamiKyoka/questionmind/internal/importing"
	"github.com/NanamiKyoka/questionmind/internal/model"
)

// ExtractRequest 智能录题：抽取请求。
type ExtractRequest struct {
	// Content 待抽取的文本（Markdown 或纯文本）。
	Content string `json:"content" binding:"max=200000"`
	// Images 图片，形如 data:image/png;base64,xxx 。
	// 支持多图：一道题可能配多张图。
	Images []string `json:"images" binding:"max=10"`
	// PageImages 由 PDF 渲染出的页图地址。
	//
	// 与 Images 分开而不是混在一起是有意的：两者对模型的意义完全不同——
	// Images 是"同一份卷子的答案页"，要**转写**不要重算；
	// PageImages 是"卷子的题目页"，要照着它识别题目。
	// 混在一起时提示词无法区分，模型会把题目页当成答案页照抄。
	//
	// 由服务端内部流转（PDF 上传路径），客户端一般不填。
	// 上限 50：与 extracting 的分页上限对齐；真有 100 页的卷子
	// 也不该一次塞进同一个请求——页图会被逐张 base64 内联进提示词。
	PageImages []string `json:"page_images,omitempty" binding:"max=50"`
	// Mode extract（抽取新题）| solve（补写答案）| record（仅录题，不求解）。
	Mode string `json:"mode" binding:"omitempty,oneof=extract solve record"`
	// SolveForContent 仅在 mode=solve 时使用：待补答案的题目文本。
	SolveForContent string `json:"solve_for_content" binding:"max=200000"`
	// Filename 本次导入内容的来源名称（原始文件名、粘贴的文档标题等）。
	//
	// 可选：留空时服务端按导入方式生成一句可读描述
	//（如「粘贴文本」「图片识别（3 张）」），历史列表不会因此一片空白。
	// 落库前只取路径末段并过滤控制字符，因此传 "../../etc/passwd" 也不会被原样存下。
	Filename string `json:"filename" binding:"omitempty,max=255"`
	// Method 抽取方式：ai（调用模型，默认）| template（确定性规则解析）。
	//
	// 留空按 ai 处理：老客户端不带这个字段时，行为必须与从前完全一致。
	Method string `json:"method" binding:"omitempty,oneof=ai template"`
}

// ExtractResponse 抽取结果。**不写库**，由用户审核后再提交。
type ExtractResponse struct {
	// TaskID 本次抽取的审计记录 id。
	TaskID uint `json:"task_id"`

	Drafts []importing.Draft `json:"drafts"`

	// Total 抽取到的题目数。
	Total int `json:"total"`
	// WarningCount 有警告的题目数，前端据此提示"有几题需要重点核对"。
	WarningCount int `json:"warning_count"`

	// Model 本次使用的模型名，便于排查"为什么效果不好"。
	Model string `json:"model"`
	// ElapsedMs 耗时。
	ElapsedMs int64 `json:"elapsed_ms"`

	// Method 实际走的哪条路径（ai / template）。
	//
	// 必须回显而不是让前端自己记：用户看到一份解析结果时，
	// 需要知道它到底有没有经过模型——这直接决定了他该多信这份结果。
	Method string `json:"method"`
	// SourceFiles 本次覆盖到的文件（压缩包会包含包内的各个文档）。
	// 审核界面靠它把题目按来源分组。
	SourceFiles []string `json:"source_files"`
	// Outline 版面大纲：标题、说明文字与题目引用的顺序。
	//
	// 模型路径下它只含题目引用（模型不吐版面，不该凭空编造标题）；
	// 规则解析路径下还包含标题与说明文字。
	Outline []importing.Outline `json:"outline"`
	// Warnings 文档级提示（如压缩包里跳过了不安全的条目），与逐题警告不同。
	Warnings []string `json:"warnings,omitempty"`
}

// CommitRequest 审核完成后提交入库。
type CommitRequest struct {
	// Drafts 用户确认（可能已手工修改）后的草稿。
	Drafts []importing.Draft `json:"drafts" binding:"required,min=1,max=200"`

	// DefaultTagIDs / DefaultKnowledgePointIDs 应用到所有题目的默认关联。
	DefaultTagIDs            []uint `json:"default_tag_ids"`
	DefaultKnowledgePointIDs []uint `json:"default_knowledge_point_ids"`

	// Status 指定入库状态：留空则由服务端按权限决定（录入即草稿或直接发布）。
	Status model.QuestionStatus `json:"status" binding:"omitempty"`

	// Filename 本次入库批次的来源名称。语义同 ExtractRequest.Filename。
	Filename string `json:"filename" binding:"omitempty,max=255"`

	// ---- 整卷导入：顺手把这份卷子还原成可编辑的试卷 ----
	//
	// 默认关闭。导入题目和导入试卷是两种意图：用户只想把 200 道题存进
	// 题库时，凭空多出一份试卷只是垃圾数据。

	// SaveAsPaper 是否在入库的同时按大纲创建一份试卷。
	SaveAsPaper bool `json:"save_as_paper"`
	// PaperTitle 试卷标题；留空时用 Filename 或"导入 YYYY-MM-DD"。
	PaperTitle string `json:"paper_title" binding:"omitempty,max=200"`
	// Outline 版面大纲，draft_index 指向 Drafts 数组里的第几道题。
	Outline []importing.Outline `json:"outline"`

	// IdempotencyKey 同一次提交的重试键；相同键只落库一次。
	IdempotencyKey string `json:"idempotency_key" binding:"omitempty,max=64"`
}

// PaperImportPreviewRequest 整卷导入预检请求。
type PaperImportPreviewRequest struct {
	Drafts  []importing.Draft   `json:"drafts" binding:"required,min=1,max=200"`
	Outline []importing.Outline `json:"outline"`
}

// PaperImportPreviewResponse 整卷导入预检结果。
type PaperImportPreviewResponse struct {
	DraftCount          int      `json:"draft_count"`
	QuestionNodes       int      `json:"question_nodes"`
	OutlineNodes        int      `json:"outline_nodes"`
	MissingReferences   int      `json:"missing_references"`
	DuplicateReferences int      `json:"duplicate_references"`
	CanCreate           bool     `json:"can_create"`
	Warnings            []string `json:"warnings"`
}

// CommitFailure 单题入库失败。
type CommitFailure struct {
	// Index 该题在请求里的下标，便于前端定位并保留用户已填内容。
	Index  int    `json:"index"`
	TempID string `json:"temp_id,omitempty"`
	Stem   string `json:"stem,omitempty"`
	Error  string `json:"error"`
}

// CommitResponse 入库结果。
//
// 采用"部分成功"语义：一道题不合格不应该让整个导入失败，
// 否则用户要重新审核几十道已经没问题的题。
type CommitResponse struct {
	TaskID uint `json:"task_id"`

	CreatedCount int             `json:"created_count"`
	FailedCount  int             `json:"failed_count"`
	CreatedIDs   []uint          `json:"created_ids"`
	Failures     []CommitFailure `json:"failures"`

	// UnresolvedNames 草稿里提到、但本学科没有对应实体的知识点/标签名称。
	//
	// 刻意**不自动创建**：自动创建会很快积累大量近义标签
	// （"函数单调性"/"单调性"/"函数的单调性"），事后清理远比当初手工选一次麻烦。
	// 这里回传给前端，由用户决定是否去知识点/标签管理里补建。
	UnresolvedNames []string `json:"unresolved_names,omitempty"`

	// PaperID SaveAsPaper 为真且建卷成功时的试卷 id。
	//
	// 建卷失败**不影响题目入库**：题目已经落库并回传了 created_ids，
	// 丢一份排版远比丢几十道题轻。失败原因放在 PaperError 里。
	PaperID uint `json:"paper_id,omitempty"`
	// PaperError 建卷失败的���因；成功时为空。
	PaperError string `json:"paper_error,omitempty"`
}

// ImportTaskResponse 导入任务记录。
type ImportTaskResponse struct {
	ID        uint  `json:"id"`
	SubjectID *uint `json:"subject_id"`
	UserID    uint  `json:"user_id"`

	Source      model.ImportSource     `json:"source"`
	Status      model.ImportTaskStatus `json:"status"`
	StatusLabel string                 `json:"status_label"`

	// OriginalFilename 这次导入的是什么（原始文件名，或服务端生成的可读描述）。
	//
	// model.ImportTask.FilePath **刻意不投影**：它是服务器内部落盘路径，
	// 回显给客户端等于泄露部署结构。它的用途是删除任务时清理磁盘文件。
	OriginalFilename string `json:"original_filename"`
	FileType         string `json:"file_type"`
	Mode             string `json:"mode"`

	TotalCount  int `json:"total_count"`
	SavedCount  int `json:"saved_count"`
	FailedCount int `json:"failed_count"`

	// Attempt 已执行次数。配合后端的上限，防止"永远在重试"。
	Attempt int `json:"attempt"`
	// Retryable / Cancellable 由服务端按状态机计算后下发。
	//
	// 界面上该不该亮这个按钮由服务端说了算，而不是前端复制一份状态判断：
	// 两份规则必然漂移，漂移的表现是"按钮能点、点了报 409"。
	Retryable   bool `json:"retryable"`
	Cancellable bool `json:"cancellable"`

	ErrorMsg string `json:"error_message,omitempty"`

	CreatedAt  time.Time  `json:"created_at"`
	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
}

// ImportTaskStatusCounts 各状态的任务数量。
type ImportTaskStatusCounts struct {
	Pending    int64 `json:"pending"`
	Processing int64 `json:"processing"`
	Completed  int64 `json:"completed"`
	Failed     int64 `json:"failed"`
	Cancelled  int64 `json:"cancelled"`
	Total      int64 `json:"total"`
}

// ImportQueueItem 队列中的一行。
type ImportQueueItem struct {
	TaskID           uint                   `json:"task_id"`
	OriginalFilename string                 `json:"original_filename"`
	Status           model.ImportTaskStatus `json:"status"`
	StatusLabel      string                 `json:"status_label"`
	QueuePosition    int                    `json:"queue_position"`
	Attempt          int                    `json:"attempt"`
	CreatedAt        time.Time              `json:"created_at"`
}

// ImportQueueStatusResponse 队列状态。
type ImportQueueStatusResponse struct {
	Stats       ImportTaskStatusCounts `json:"stats"`
	ActiveTasks []ImportQueueItem      `json:"active_tasks"`

	// WorkerOnline 是否有后台 worker 在消费队列。
	//
	// 目前恒为 false：抽取是同步的，队列里的任务不会被自动执行。
	// 这个字段存在的意义就是让界面**如实**告诉用户"排队中"不等于"马上会开始"，
	// 而不是给一个永远不会被消费的进度条配上"处理中"的动画。
	WorkerOnline bool `json:"worker_online"`
	// Hint 面向用户的一句话说明，避免用户对着一个不动的队列反复刷新。
	Hint string `json:"hint"`
}

// NewImportTaskResponse 组装响应。
func NewImportTaskResponse(t *model.ImportTask) ImportTaskResponse {
	return ImportTaskResponse{
		ID:               t.ID,
		SubjectID:        t.SubjectID,
		UserID:           t.UserID,
		Source:           t.Source,
		Status:           t.Status,
		StatusLabel:      ImportStatusLabel(t.Status),
		OriginalFilename: t.OriginalFilename,
		FileType:         t.FileType,
		Mode:             t.Mode,
		TotalCount:       t.TotalCount,
		SavedCount:       t.SavedCount,
		FailedCount:      t.FailedCount,
		Attempt:          t.Attempt,
		Retryable:        t.Retryable(model.MaxImportAttempt),
		Cancellable:      t.IsActive(),
		ErrorMsg:         t.ErrorMsg,
		CreatedAt:        t.CreatedAt,
		StartedAt:        t.StartedAt,
		FinishedAt:       t.FinishedAt,
	}
}

// ImportStatusLabel 状态的中文名。
//
// 导出而不是留在包内：队列面板要在 service 层复用同一份文案，
// 复制一份迟早会漂移，而漂移的表现是"同一种状态在列表和队列里叫两个名字"。
func ImportStatusLabel(s model.ImportTaskStatus) string {
	switch s {
	case model.ImportStatusPending:
		return "等待处理"
	case model.ImportStatusProcessing:
		return "处理中"
	case model.ImportStatusCompleted:
		return "已完成"
	case model.ImportStatusFailed:
		return "失败"
	case model.ImportStatusCancelled:
		return "已放弃"
	}
	return string(s)
}
