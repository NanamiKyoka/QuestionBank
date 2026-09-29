package service

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
)

// setupMarkerKey 首次初始化的原子标记键。
//
// 刻意与 service/auth.go 里的 setupClaimKey 取同一个字面量：那个常量是
// unexported 的，而本文件需要把这一行也列进目录，好让管理员看得见
// "系统已经初始化过了"这个事实。值写错不会造成功能影响——
// 真正的判据是 ClaimOnce 的"这一行在不在"。
const setupMarkerKey = "system_initialized"

// SettingType 设置项的取值形态：决定界面用什么控件编辑，也决定后端怎么校验。
type SettingType string

const (
	SettingTypeBool SettingType = "bool"
	SettingTypeInt  SettingType = "int"
	SettingTypeText SettingType = "text"
)

// settingDef 单个设置项的元数据。
type settingDef struct {
	Key   string
	Label string
	// Description 一句话说明这个键是干什么的，会作为行存储的 description 一起写入。
	Description string
	Type        SettingType
	// Def 默认值：数据库里没有这一行时系统使用的取值。
	Def string
	// Editable 是否允许通过接口改。只有"确实有代码会读"的键才配 true。
	Editable bool
	// Note 写给管理员看的"它现在到底生不生效"。
	//
	// 这个字段是整个设置页存在的意义：与其给一个能保存、却没有任何代码
	// 会读的输入框，不如直说"改了不起作用"。系统里确实存在这类键
	// （见下面 default_review_threshold / ai_extract_prompt 的注释），
	// 假装它们能用比不显示更糟——管理员会以为已经改过了。
	Note string
}

// settingCatalog 全部对外暴露的设置项。
//
// 顺序即界面顺序：先放真正能改的，再放只读的，最后是内部标记。
var settingCatalog = []settingDef{
	{
		Key:         model.SettingRegistrationOpen,
		Label:       "开放自助注册",
		Description: "是否允许未登录用户自行注册账号",
		Type:        SettingTypeBool,
		Def:         "false",
		Editable:    true,
		Note: "由 GET /system/setup-status 读取并回传 registration_enabled 字段（service/auth.go:52）。" +
			"但后端目前没有 POST /auth/register 端点，登录页也没有注册入口，" +
			"所以打开它只是把策略先存下来，暂时不会真的出现注册页面。",
	},
	{
		Key:         model.SettingReviewThreshold,
		Label:       "默认审核阈值",
		Description: "题目需审核通过多少次才自动发布",
		Type:        SettingTypeInt,
		Def:         "1",
		Editable:    false,
		Note: "⚠ 只读：键在 model/enums.go:327 声明了，但全仓没有任何读取方。" +
			"审核阈值实际取学科自身的 review_threshold（service/question_ops.go:274），" +
			"改这里不会有任何效果——请到「学科管理」里逐个学科设置。",
	},
	{
		Key:         model.SettingAIExtractPrompt,
		Label:       "智能录题抽取提示词",
		Description: "从文档中抽取题目时使用的系统提示词",
		Type:        SettingTypeText,
		Editable:    false,
		Note: "⚠ 只读：键在 model/enums.go:324 声明，importing.DefaultExtractPromptKey 也引用了它，" +
			"但没有任何代码真的去读这一行。智能录题仍使用 importing/prompt.go 里硬编码的提示词，" +
			"改这里不会影响抽取结果。",
	},
	{
		Key:         model.SettingAISolvePrompt,
		Label:       "智能录题解题提示词",
		Description: "为已有题目补写答案与解析时使用的系统提示词",
		Type:        SettingTypeText,
		Editable:    false,
		Note:        "⚠ 只读：键在 model/enums.go:325 声明，但没有任何读取方，与上一项情况相同。",
	},
	{
		Key:         model.SettingAITextModelID,
		Label:       "当前文本模型",
		Description: "智能录题、AI 助手实际调用的文本模型 id",
		Type:        SettingTypeInt,
		Editable:    false,
		Note: "确实被 service/ai_config.go:341 读取，但由「AI 设置」页的默认模型配置管理" +
			"（GET/PUT /ai/active-config）。这里只读，避免两处入口互相覆盖。",
	},
	{
		Key:         model.SettingAIVisionModelID,
		Label:       "当前视觉模型",
		Description: "图片识题实际调用的视觉模型 id",
		Type:        SettingTypeInt,
		Editable:    false,
		Note:        "由「AI 设置」页的默认模型配置管理（GET/PUT /ai/active-config），这里只读。",
	},
	{
		Key:         model.SettingAIEmbeddingModelID,
		Label:       "当前向量模型",
		Description: "知识点语义检索实际调用的向量模型 id",
		Type:        SettingTypeInt,
		Editable:    false,
		Note:        "由「AI 设置」页的默认模型配置管理（GET/PUT /ai/active-config），这里只读。",
	},
	{
		Key:         setupMarkerKey,
		Label:       "初始化标记",
		Description: "系统内部标记，记录首次完成初始化的时间",
		Type:        SettingTypeText,
		Editable:    false,
		Note: "系统内部标记（service/auth.go:18）。首次初始化时由 ClaimOnce 原子写入，" +
			"这一行存在就意味着初始化已经完成、并发的第二次初始化会被它挡住。" +
			"因此它不能被接口修改，本页也不提供任何删除入口。",
	},
}

// settingIndex 目录的键索引，O(1) 查定义。
var settingIndex = func() map[string]settingDef {
	m := make(map[string]settingDef, len(settingCatalog))
	for _, d := range settingCatalog {
		m[d.Key] = d
	}
	return m
}()

// SettingsService 系统设置用例。
//
// 三条不能破的约束（它们是这个页面不出事的原因）：
//
//  1. 写入只允许命中目录里 Editable 的键，白名单之外一律拒绝。
//     否则管理员就能给 system_initialized 之类的内部标记改值，
//     把"系统初始化过没有"这件事变得不可解释。
//  2. 落库只走 SettingRepo.Set——依赖 setting_key 唯一索引的单条 upsert。
//     绝不"先 Get 再 Set"：那是经典的读改写竞态，两个管理员同时保存会
//     后写覆盖先写，而且中间还多一次查询。
//  3. 不提供删除接口。删掉 system_initialized 那一行会让 Setup 重新
//     可以执行，等于凭空多出一条绕过"系统已初始化"检查的路。
type SettingsService struct {
	repos *Repos
}

func NewSettingsService(repos *Repos) *SettingsService {
	return &SettingsService{repos: repos}
}

// SettingItem 单个设置项对外的完整状态。
type SettingItem struct {
	Key         string      `json:"key"`
	Label       string      `json:"label"`
	Description string      `json:"description"`
	Type        SettingType `json:"type"`
	// Value 当前生效的取值。数据库里没有这一行时等于 Def。
	Value string `json:"value"`
	// Def 默认值，界面用它做"未配置"的提示文案。
	Def string `json:"def"`
	// Editable 是否可改。false 时界面只读展示并显示 Note。
	Editable bool `json:"editable"`
	// Note 见 settingDef.Note。
	Note string `json:"note"`
	// Saved 数据库里是否真的有这一行。false 表示界面显示的是默认值。
	Saved bool `json:"saved"`
	// UpdatedBy / UpdatedAt 仅在 Saved 为 true 时有意义。
	UpdatedBy uint       `json:"updated_by"`
	UpdatedAt *time.Time `json:"updated_at"`
}

// List 读取全部设置项。仅管理员。
//
// 结果 = 目录里的每一项 + 数据库里存在但目录未登记的键。
// 后者按只读返回：把它们藏起来等于让管理员以为自己看全了，
// 而"表里有一行、界面上没有"正是排查设置问题最费时间的情况。
func (s *SettingsService) List(ctx context.Context, actor *model.User) ([]SettingItem, error) {
	if err := authorizeSuperuser(actor); err != nil {
		return nil, err
	}
	rows, err := s.repos.Settings.All(ctx)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}

	index := make(map[string]model.SystemSetting, len(rows))
	for _, r := range rows {
		index[r.Key] = r
	}

	out := make([]SettingItem, 0, len(settingCatalog)+len(rows))
	for _, def := range settingCatalog {
		out = append(out, buildItem(def, index[def.Key]))
	}
	for _, r := range rows {
		if _, known := settingIndex[r.Key]; known {
			continue
		}
		out = append(out, unknownItem(r))
	}
	return out, nil
}

// Update 写入单个设置项。仅管理员，且只接受目录里可写的键。
func (s *SettingsService) Update(ctx context.Context, actor *model.User, key, raw string) (*SettingItem, error) {
	if err := authorizeSuperuser(actor); err != nil {
		return nil, err
	}
	def, known := settingIndex[key]
	if !known {
		return nil, apperr.ErrNotFound.WithMessage("未知的设置项").WithDetail("key=%s", key)
	}
	if !def.Editable {
		// 用 403 而不是 400：这不是"参数写错了"，是"这个键不由接口管"。
		return nil, apperr.ErrForbidden.WithMessage(fmtReadOnly(def.Label))
	}

	value, err := def.normalize(raw)
	if err != nil {
		return nil, err
	}

	if err := s.repos.Settings.Set(ctx, def.Key, value, def.Description, actor.ID); err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}

	// 回读一次拿 updated_at/updated_by：Set 本身不返回实体，
	// 而界面上"最后由谁在什么时候改的"比"改成功了"更有用。
	rows, err := s.repos.Settings.All(ctx)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	for _, r := range rows {
		if r.Key == def.Key {
			item := buildItem(def, r)
			return &item, nil
		}
	}
	// 理论上不可达：Set 成功即代表这一行存在。兜底成"已保存"，
	// 免得因为回读竞态把一次成功的写入报成失败。
	fallback := buildItem(def, model.SystemSetting{Key: def.Key, Value: value})
	return &fallback, nil
}

// buildItem 把定义与数据库行合成对外结构。
//
// row 的零值表示"库里没有这一行"：此时展示默认值而不是空串。
// 这与仓储 Get 的语义一致（repository/setting.go:31-34 把空串也当作未配置），
// 两边不能一个显示空、一个显示默认值。
func buildItem(def settingDef, row model.SystemSetting) SettingItem {
	item := SettingItem{
		Key:         def.Key,
		Label:       def.Label,
		Description: def.Description,
		Type:        def.Type,
		Value:       def.Def,
		Def:         def.Def,
		Editable:    def.Editable,
		Note:        def.Note,
		Saved:       row.Key != "",
		UpdatedBy:   row.UpdatedBy,
	}
	if row.Value != "" {
		item.Value = row.Value
	}
	if item.Saved {
		updated := row.UpdatedAt
		item.UpdatedAt = &updated
	}
	return item
}

// unknownItem 目录未登记、但数据库里确实存在的键。
//
// 一定标成只读：它连"是什么类型"都不知道，贸然给个输入框等于让管理员
// 往一个没人读的键里写垃圾。它更可能是某个版本的代码留下的，
// 也可能是别的功能（AI、导入）自己管理的。
func unknownItem(row model.SystemSetting) SettingItem {
	return SettingItem{
		Key:         row.Key,
		Label:       row.Key,
		Description: row.Description,
		Type:        SettingTypeText,
		Value:       row.Value,
		Def:         "",
		Editable:    false,
		Note: "数据库中存在，但当前版本的设置目录没有登记它，因此只读。" +
			"如果它由别的功能管理（如 AI 配置），请到对应的设置页修改。",
		Saved:     true,
		UpdatedBy: row.UpdatedBy,
		UpdatedAt: &row.UpdatedAt,
	}
}

// normalize 校验并规整提交的取值。
//
// 布尔值的接受集合刻意与仓储 GetBool（repository/setting.go:37-46）完全一致，
// 存进去的 "1" / "on" 才不会被读回来当成 false。落库统一成 "true"/"false"。
func (d settingDef) normalize(raw string) (string, error) {
	v := strings.TrimSpace(raw)
	switch d.Type {
	case SettingTypeBool:
		switch strings.ToLower(v) {
		case "true", "1", "yes", "on":
			return "true", nil
		case "false", "0", "no", "off":
			return "false", nil
		default:
			return "", apperr.New(apperr.CodeInvalidParam, "取值不合法").
				WithDetail("%s 需要布尔值（true/false），收到 %q", d.Label, raw)
		}
	case SettingTypeInt:
		// 目前只出现在只读项上；分支保留是为了 allowlist 扩容时不必重写校验。
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return "", apperr.New(apperr.CodeInvalidParam, "取值不合法").
				WithDetail("%s 需要非负整数，收到 %q", d.Label, raw)
		}
		return strconv.Itoa(n), nil
	default:
		// text：提示词这类长文本。只挡空串——空串在仓储里等同于
		// "未配置"，写进去等于没写，不如直接拒绝。
		if v == "" {
			return "", apperr.New(apperr.CodeInvalidParam, "取值不能为空").
				WithDetail("%s 的空值等同于未配置", d.Label)
		}
		return v, nil
	}
}

// fmtReadOnly 统一的只读拒绝文案。
func fmtReadOnly(label string) string {
	return "「" + label + "」是只读设置项，不能通过接口修改"
}
