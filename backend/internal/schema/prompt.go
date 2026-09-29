package schema

import (
	"time"

	"github.com/NanamiKyoka/questionmind/internal/importing"
	"github.com/NanamiKyoka/questionmind/internal/model"
)

// maxPromptBody 提示词正文的绑定上限（字符数）。
//
// 写进 binding 而不是只在 service 里判，是为了让"提交了一个 20 万字的
// 提示词"变成一条明确的 400，而不是让它进到渲染层再被静默丢弃。
const maxPromptBody = 65535

// PromptPlaceholder 占位符说明。
type PromptPlaceholder struct {
	Name string `json:"name"`
	Desc string `json:"desc"`
	// Optional 为真时取值为空会连带删掉整行。
	Optional bool `json:"optional"`
	// Token 带大括号的写法，界面上的"插入占位符"按钮用它。
	Token string `json:"token"`
}

// PromptTemplateResponse 一条模板（全局或学科覆盖）。
type PromptTemplateResponse struct {
	ID   uint   `json:"id"`
	Key  string `json:"key"`
	Name string `json:"name"`
	// Scene 分组用，取值 import / chat。
	Scene string `json:"scene"`
	// Description 这个提示词是干什么的。
	Description string `json:"description"`
	// Placeholders 该提示词支持的占位符。
	Placeholders []PromptPlaceholder `json:"placeholders"`
	// SubjectID 为空表示全局模板；非空表示该学科的覆盖。
	SubjectID *uint `json:"subject_id"`

	SystemPrompt string `json:"system_prompt"`
	UpdatedBy    uint   `json:"updated_by"`

	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	DeletedAt *time.Time `json:"-"`
}

// PromptCatalogEntry 目录里的一条提示词，连同"当前有没有配"。
//
// GET /prompts 返回的是它而不是模板行本身：管理界面需要先知道
// 系统里到底有哪几个提示词、每个长什么样，才能把"没配"这件事
// 显示成一条待填的卡片，而不是让管理员面对一个空列表。
type PromptCatalogEntry struct {
	Key          string              `json:"key"`
	Name         string              `json:"name"`
	Scene        string              `json:"scene"`
	Description  string              `json:"description"`
	Placeholders []PromptPlaceholder `json:"placeholders"`
	// Default 内置默认模板正文，供"恢复默认"时对照或复制。
	Default string `json:"default"`
	// Template 当前保存的全局模板；nil 表示尚未配置。
	Template *PromptTemplateResponse `json:"template"`
	// EffectiveSource 全局模板的来源：global / setting / builtin。
	EffectiveSource importing.Source `json:"effective_source"`
	// Fallback 非空说明当前实际用的不是保存的模板。
	Fallback string `json:"fallback"`
	// SettingKey 该提示词在系统设置里的兜底键；空表示没有。
	SettingKey string `json:"setting_key"`
	// SettingValue 兜底键里当前的值（只读，仅供管理员了解解析链）。
	SettingValue string `json:"setting_value"`
	// Overrides 已配置的学科覆盖数量。
	OverrideCount int `json:"override_count"`
}

// PromptListResponse GET /prompts 的响应体。
type PromptListResponse struct {
	Items []PromptCatalogEntry `json:"items"`
}

// PromptCreateRequest 新建模板。
type PromptCreateRequest struct {
	// Key 必须是 importing 目录里的提示词键；自由文本会被拒绝，
	// 否则管理员可以存下一条永远不会被任何代码读取的模板。
	Key string `json:"key" binding:"required,max=64"`
	// SubjectID 为空表示全局模板。
	SubjectID *uint `json:"subject_id"`
	// SystemPrompt 模板正文，必须带至少一个占位符之外的内容。
	SystemPrompt string `json:"system_prompt" binding:"required,max=$maxPromptBody"`
}

// PromptUpdateRequest 修改模板正文。
type PromptUpdateRequest struct {
	SystemPrompt *string `json:"system_prompt" binding:"omitempty,max=$maxPromptBody"`
}

// SubjectPromptItem 某个学科在某条提示词上的最终状态。
type SubjectPromptItem struct {
	Key          string              `json:"key"`
	Name         string              `json:"name"`
	Scene        string              `json:"scene"`
	Description  string              `json:"description"`
	Placeholders []PromptPlaceholder `json:"placeholders"`
	// Default 内置默认正文。
	Default string `json:"default"`
	// Effective 该学科最终会喂给模型的**模板**正文。
	//
	// 给模板而不是渲染结果：知识点与标签清单要到真正发起抽取/对话那一刻
	// 才确定，现在渲染出来的只会是一份假的结果。
	Effective string `json:"effective"`
	// Preview 把学科名与学科说明代进去之后的渲染结果，供管理员核对
	// "这个学科最终会看到什么"。知识点/标签块在预览里为空。
	Preview string           `json:"preview"`
	Source  importing.Source `json:"source"`
	// OverrideID 该学科覆盖行的 id；没有覆盖时为 0。
	OverrideID uint `json:"override_id"`
	// OverrideUpdatedAt 覆盖的最后修改时间；没有覆盖时为 null。
	OverrideUpdatedAt *time.Time `json:"override_updated_at"`
	// Fallback 非空说明为什么最终用的不是覆盖模板。
	Fallback string `json:"fallback"`
}

// SubjectPromptsResponse GET /subjects/:sid/prompts。
type SubjectPromptsResponse struct {
	SubjectID   uint                `json:"subject_id"`
	SubjectName string              `json:"subject_name"`
	Prompts     []SubjectPromptItem `json:"prompts"`
}

// SubjectPromptUpsert 一条学科覆盖的写入。
type SubjectPromptUpsert struct {
	Key string `json:"key" binding:"required,max=64"`
	// SystemPrompt 为空表示删除该学科的覆盖、回退到全局模板。
	//
	// 用"空值即删除"而不是额外的 enabled 标志：覆盖只有"有"和"没有"
	// 两种有意义的状态，多一个开关只会让人分不清"关掉的覆盖"和
	// "删掉的覆盖"在解析链上有什么区别（其实没有区别）。
	SystemPrompt string `json:"system_prompt" binding:"max=$maxPromptBody"`
}

// SubjectPromptsUpdateRequest PUT /subjects/:sid/prompts。
type SubjectPromptsUpdateRequest struct {
	Prompts []SubjectPromptUpsert `json:"prompts" binding:"required,min=1,max=8,dive"`
}

// NewPromptTemplateResponse 组装单条模板的响应。
func NewPromptTemplateResponse(t *model.PromptTemplate, spec importing.PromptSpec) PromptTemplateResponse {
	return PromptTemplateResponse{
		ID:           t.ID,
		Key:          model.BasePromptKey(t.Key),
		Name:         t.Name,
		Scene:        t.Scene,
		Description:  spec.Description,
		Placeholders: NewPromptPlaceholders(spec),
		SubjectID:    subjectIDOf(t),
		SystemPrompt: t.SystemPrompt,
		UpdatedBy:    t.UpdatedBy,
		CreatedAt:    t.CreatedAt,
		UpdatedAt:    t.UpdatedAt,
	}
}

// NewPromptPlaceholders 把目录里的占位符转成前端结构。
func NewPromptPlaceholders(spec importing.PromptSpec) []PromptPlaceholder {
	out := make([]PromptPlaceholder, 0, len(spec.Placeholders))
	for _, p := range spec.Placeholders {
		out = append(out, PromptPlaceholder{
			Name:     p.Name,
			Desc:     p.Desc,
			Optional: p.Optional,
			Token:    "{{" + p.Name + "}}",
		})
	}
	return out
}

// subjectIDOf 解析模板行对应的学科。
//
// 键里带着学科 id（见 model.SubjectPromptKey）；这行是历史数据、
// 键的格式变了时宁可当成全局模板，也不要凭空猜一个学科。
func subjectIDOf(t *model.PromptTemplate) *uint {
	id, ok := model.PromptSubjectID(t.Key)
	if !ok {
		return nil
	}
	return &id
}
