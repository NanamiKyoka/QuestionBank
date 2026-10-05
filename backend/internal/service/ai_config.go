package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/NanamiKyoka/questionmind/internal/ai"
	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
	"github.com/NanamiKyoka/questionmind/internal/pkg/cryptox"
	"github.com/NanamiKyoka/questionmind/internal/schema"
)

// AIConfigService AI 供应商与模型配置用例。
type AIConfigService struct {
	repos  *Repos
	cipher *cryptox.Cipher
	// timeout / maxRetries 来自应用配置，作为每次调用供应商的默认值。
	timeout    time.Duration
	maxRetries int
}

// NewAIConfigService 构造。
func NewAIConfigService(repos *Repos, cipher *cryptox.Cipher, timeout time.Duration, maxRetries int) *AIConfigService {
	return &AIConfigService{repos: repos, cipher: cipher, timeout: timeout, maxRetries: maxRetries}
}

// ---- 供应商 ----

// defaultModelMaxTokens 新建模型时的默认输出上限。
//
// 4096 是聊天场景的常见默认值，但**录题不是聊天**：一道题连题干、
// 选项、答案、解析，模型要吐一千多 token（实测 1370/题），
// 八道题就上万。默认值低于这个数，等于每个新装的人都先撞一次
// "输出被截断"，而这件事在保存配置时完全看不出来。
const defaultModelMaxTokens = 16384

func (s *AIConfigService) ListProviders(ctx context.Context, actor *model.User) ([]schema.ProviderResponse, error) {
	if err := authorizeSuperuser(actor); err != nil {
		return nil, err
	}

	list, err := s.repos.AIProviders.List(ctx)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}

	out := make([]schema.ProviderResponse, 0, len(list))
	active, err := s.inUseByModel(ctx)
	if err != nil {
		return nil, err
	}

	for i := range list {
		resp := schema.NewProviderResponse(&list[i])
		for j := range resp.Models {
			uses := active[resp.Models[j].ID]
			resp.Models[j].InUse = uses != nil
			resp.Models[j].InUseFor = inUseValues(uses)
		}
		out = append(out, resp)
	}
	return out, nil
}

func (s *AIConfigService) CreateProvider(ctx context.Context, actor *model.User, req *schema.ProviderCreateRequest) (*schema.ProviderResponse, error) {
	if err := authorizeSuperuser(actor); err != nil {
		return nil, err
	}
	if err := validateInterfaceType(req.InterfaceType); err != nil {
		return nil, err
	}
	if err := validateBaseURL(req.BaseURL); err != nil {
		return nil, err
	}

	dup, err := s.repos.AIProviders.ExistsName(ctx, req.Name, 0)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	if dup {
		return nil, apperr.New(apperr.CodeDuplicateName, "已存在同名供应商")
	}

	encrypted, err := s.cipher.Encrypt(req.APIKey)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}

	p := &model.AIProvider{
		Name:            req.Name,
		InterfaceType:   model.AIInterfaceType(req.InterfaceType),
		BaseURL:         strings.TrimSpace(req.BaseURL),
		APIKeyEncrypted: encrypted,
		KeyHint:         cryptox.Mask(req.APIKey),
		IsActive:        true,
	}
	if req.IsActive != nil {
		p.IsActive = *req.IsActive
	}
	if req.SortOrder != nil {
		p.SortOrder = *req.SortOrder
	}

	if err := s.repos.AIProviders.Create(ctx, p); err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}

	resp := schema.NewProviderResponse(p)
	return &resp, nil
}

func (s *AIConfigService) UpdateProvider(ctx context.Context, actor *model.User, id uint, req *schema.ProviderUpdateRequest) (*schema.ProviderResponse, error) {
	if err := authorizeSuperuser(actor); err != nil {
		return nil, err
	}

	// 确认存在（Get 在不存在时返回 NotFound）。
	if _, err := s.repos.AIProviders.Get(ctx, id); err != nil {
		return nil, err
	}

	fields := map[string]any{}
	if req.Name != nil {
		dup, err := s.repos.AIProviders.ExistsName(ctx, *req.Name, id)
		if err != nil {
			return nil, apperr.ErrInternal.WithCause(err)
		}
		if dup {
			return nil, apperr.New(apperr.CodeDuplicateName, "已存在同名供应商")
		}
		fields["name"] = *req.Name
	}
	if req.InterfaceType != nil {
		// 改协议同样要当场校验：否则等于留了个"改完才发现用不了"的坑。
		if err := validateInterfaceType(*req.InterfaceType); err != nil {
			return nil, err
		}
		fields["interface_type"] = *req.InterfaceType
	}
	if req.BaseURL != nil {
		if err := validateBaseURL(*req.BaseURL); err != nil {
			return nil, err
		}
		fields["base_url"] = strings.TrimSpace(*req.BaseURL)
	}
	if req.IsActive != nil {
		fields["is_active"] = *req.IsActive
	}
	if req.SortOrder != nil {
		fields["sort_order"] = *req.SortOrder
	}

	// 密钥三态：空 / 掩码 → 不修改；新明文 → 覆盖。
	if req.APIKey != nil {
		key := strings.TrimSpace(*req.APIKey)
		switch {
		case key == "":
			// 不修改
		case cryptox.IsMasked(key):
			// 前端把掩码原样回传，绝不能拿它覆盖真实密钥。
		default:
			encrypted, err := s.cipher.Encrypt(key)
			if err != nil {
				return nil, apperr.ErrInternal.WithCause(err)
			}
			fields["api_key_encrypted"] = encrypted
			fields["key_hint"] = cryptox.Mask(key)
		}
	}

	if err := s.repos.AIProviders.UpdateFields(ctx, id, fields); err != nil {
		return nil, apperr.From(err)
	}

	updated, err := s.repos.AIProviders.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	resp := schema.NewProviderResponse(updated)
	return &resp, nil
}

// DeleteProvider 删除供应商，并解除对它的模型引用。
//
// 如果被删除的供应商正提供当前生效的模型，需要清掉对应设置，
// 否则 AI 功能会指向一个已删除的模型，报错信息还很难懂。
func (s *AIConfigService) DeleteProvider(ctx context.Context, actor *model.User, id uint) error {
	if err := authorizeSuperuser(actor); err != nil {
		return err
	}
	if _, err := s.repos.AIProviders.Get(ctx, id); err != nil {
		return err
	}

	models, err := s.repos.AIModels.ListByProvider(ctx, id)
	if err != nil {
		return apperr.ErrInternal.WithCause(err)
	}

	return s.repos.Tx.Within(ctx, func(ctx context.Context) error {
		for i := range models {
			if err := s.clearIfActive(ctx, models[i].ID, actor.ID); err != nil {
				return err
			}
		}
		if err := s.repos.AIProviders.SoftDeleteModels(ctx, id); err != nil {
			return apperr.ErrInternal.WithCause(err)
		}
		return s.repos.AIProviders.SoftDelete(ctx, id)
	})
}

// ---- 模型 ----

func (s *AIConfigService) CreateModel(ctx context.Context, actor *model.User, req *schema.ModelCreateRequest) (*schema.ModelResponse, error) {
	if err := authorizeSuperuser(actor); err != nil {
		return nil, err
	}
	if _, err := s.repos.AIProviders.Get(ctx, req.ProviderID); err != nil {
		return nil, err
	}

	dup, err := s.repos.AIModels.ExistsName(ctx, req.ProviderID, req.Name, 0)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	if dup {
		return nil, apperr.New(apperr.CodeDuplicateName, "该供应商下已存在同名模型")
	}

	caps, err := req.Caps()
	if err != nil {
		return nil, apperr.New(apperr.CodeInvalidParam, err.Error())
	}

	m := &model.AIModel{
		ProviderID:  req.ProviderID,
		Name:        req.Name,
		DisplayName: req.DisplayName,
		ModelType:   strings.Join(caps, ","),
		MaxTokens:   defaultModelMaxTokens,
		// 0 表示"未知"：不知道就��� 32768 更好——猜一个偏小的值只会在
		// 真正需要长上下文时才出错，而那时已经产生了一份坏结果。
		ContextWindow: 0,
		IsActive:      true,
	}
	if m.DisplayName == "" {
		m.DisplayName = req.Name
	}
	if req.MaxTokens != nil {
		m.MaxTokens = *req.MaxTokens
	}
	if req.ContextWindow != nil {
		m.ContextWindow = *req.ContextWindow
	}
	if req.IsActive != nil {
		m.IsActive = *req.IsActive
	}
	if req.SortOrder != nil {
		m.SortOrder = *req.SortOrder
	}

	if err := s.repos.AIModels.Create(ctx, m); err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}

	// 第一个创建的模型自动成为默认，省去"配好了却忘了选"的困惑。
	if err := s.autoSelectIfFirst(ctx, m, actor.ID); err != nil {
		return nil, err
	}

	created, err := s.repos.AIModels.Get(ctx, m.ID)
	if err != nil {
		return nil, err
	}
	active, err := s.inUseByModel(ctx)
	if err != nil {
		return nil, err
	}
	resp := schema.NewModelResponse(created)
	resp.InUse = active[resp.ID] != nil
	resp.InUseFor = inUseValues(active[resp.ID])
	return &resp, nil
}

func (s *AIConfigService) UpdateModel(ctx context.Context, actor *model.User, id uint, req *schema.ModelUpdateRequest) (*schema.ModelResponse, error) {
	if err := authorizeSuperuser(actor); err != nil {
		return nil, err
	}
	current, err := s.repos.AIModels.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	fields := map[string]any{}
	if req.Name != nil {
		dup, err := s.repos.AIModels.ExistsName(ctx, current.ProviderID, *req.Name, id)
		if err != nil {
			return nil, apperr.ErrInternal.WithCause(err)
		}
		if dup {
			return nil, apperr.New(apperr.CodeDuplicateName, "该供应商下已存在同名模型")
		}
		fields["name"] = *req.Name
	}
	if req.DisplayName != nil {
		fields["display_name"] = *req.DisplayName
	}
	if caps, ok, err := req.CapsUpdate(); err != nil {
		return nil, apperr.New(apperr.CodeInvalidParam, err.Error())
	} else if ok {
		fields["model_type"] = strings.Join(caps, ",")
	}
	if req.MaxTokens != nil {
		fields["max_tokens"] = *req.MaxTokens
	}
	if req.ContextWindow != nil {
		fields["context_window"] = *req.ContextWindow
	}
	if req.IsActive != nil {
		fields["is_active"] = *req.IsActive
	}
	if req.SortOrder != nil {
		fields["sort_order"] = *req.SortOrder
	}

	if err := s.repos.AIModels.UpdateFields(ctx, id, fields); err != nil {
		return nil, apperr.From(err)
	}

	updated, err := s.repos.AIModels.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	active, err := s.inUseByModel(ctx)
	if err != nil {
		return nil, err
	}
	resp := schema.NewModelResponse(updated)
	resp.InUse = active[resp.ID] != nil
	resp.InUseFor = inUseValues(active[resp.ID])
	return &resp, nil
}

func (s *AIConfigService) DeleteModel(ctx context.Context, actor *model.User, id uint) error {
	if err := authorizeSuperuser(actor); err != nil {
		return err
	}
	if _, err := s.repos.AIModels.Get(ctx, id); err != nil {
		return err
	}

	return s.repos.Tx.Within(ctx, func(ctx context.Context) error {
		if err := s.clearIfActive(ctx, id, actor.ID); err != nil {
			return err
		}
		return s.repos.AIModels.SoftDelete(ctx, id)
	})
}

// ---- 当前生效配置 ----

func (s *AIConfigService) GetActiveConfig(ctx context.Context, actor *model.User) (*schema.ActiveConfigResponse, error) {
	if err := authorizeSuperuser(actor); err != nil {
		return nil, err
	}

	resp := &schema.ActiveConfigResponse{}
	textID := uint(s.repos.Settings.GetInt(ctx, model.SettingAITextModelID, 0))
	visionID := uint(s.repos.Settings.GetInt(ctx, model.SettingAIVisionModelID, 0))
	embeddingID := uint(s.repos.Settings.GetInt(ctx, model.SettingAIEmbeddingModelID, 0))

	resp.TextModelID = textID
	resp.VisionModelID = visionID
	resp.EmbeddingModelID = embeddingID

	// 三种用途各自记一份"供应商是否真的能调用"，用于最后的就绪判断。
	var textServable, visionServable, embeddingServable bool
	if textID > 0 {
		if m, err := s.repos.AIModels.Get(ctx, textID); err == nil {
			item := schema.NewModelResponse(m)
			resp.Text = &item
			textServable = providerServable(m)
		}
	}
	if visionID > 0 {
		if m, err := s.repos.AIModels.Get(ctx, visionID); err == nil {
			item := schema.NewModelResponse(m)
			resp.Vision = &item
			visionServable = providerServable(m)
		}
	}
	if embeddingID > 0 {
		if m, err := s.repos.AIModels.Get(ctx, embeddingID); err == nil {
			item := schema.NewModelResponse(m)
			resp.Embedding = &item
			embeddingServable = providerServable(m)
		}
	}

	resp.Ready = resp.Text != nil
	switch {
	case resp.Text == nil:
		resp.Message = "尚未配置文本模型，AI 助手与智能录题不可用"
	case !textServable:
		// 接口类型是唯一"存得下却一定用不了"的配置：密钥对错、额度高低
		// 都得真发一次请求才知道，而这里能立刻判定。页面若还报"已就绪"，
		// 用户会去怀疑账号和额度，白白浪费时间。
		resp.Ready = false
		resp.Message = unusableProviderMsg(resp.Text, "AI 助手与智能录题")
	case resp.Vision == nil:
		resp.Message = "已配置文本模型；未配置视觉模型，图片识题不可用"
	case !visionServable:
		resp.Message = unusableProviderMsg(resp.Vision, "图片识题")
	case resp.Embedding == nil:
		resp.Message = "已配置文本与视觉模型；未配置向量模型，知识点语义检索不可用"
	case !embeddingServable:
		resp.Message = unusableProviderMsg(resp.Embedding, "知识点语义检索")
	default:
		resp.Message = "AI 功能已就绪"
	}
	return resp, nil
}

// validateInterfaceType 挡下"能选、存得下、用不了"的协议。
//
// 接口类型的权威清单在 ai 包里（界面下拉框读的是同一个函数），
// 这里不另抄一份：将来加了新协议，只改 ai 一处即可。
func validateInterfaceType(t string) error {
	if ai.SupportsInterfaceType(t) {
		return nil
	}
	return apperr.New(apperr.CodeInvalidParam,
		"不支持的接口类型，当前仅支持："+strings.Join(ai.InterfaceTypes(), "、")).
		WithDetail("收到的值是 %q；该厂商若提供 OpenAI 兼容端点，请选择 %s 并填写对应的 base_url", t, ai.TypeOpenAI)
}

// validateBaseURL 校验自定义网关地址。新建与更新共用，规则来自 ai 包，
// 与真正发起请求时的校验是同一套——不会出现"存得下、连不上"的缝。
//
// 更新路径必须在 service 里校验：指针字段上的 binding 对"指向空串"不可靠
// （omitempty 看的是指针非 nil），只靠 tag 会漏掉整条更新链路。
func validateBaseURL(raw string) error {
	if err := ai.ValidateBaseURL(raw); err != nil {
		return apperr.New(apperr.CodeInvalidParam, "网关地址无效："+err.Error())
	}
	return nil
}

// providerServable 判断该模型所属供应商当前能否真正发起调用。
func providerServable(m *model.AIModel) bool {
	return m.Provider != nil && ai.SupportsInterfaceType(string(m.Provider.InterfaceType))
}

// unusableProviderMsg 组装"配好了但用不了"的用户提示。
func unusableProviderMsg(item *schema.ModelResponse, feature string) string {
	name := item.DisplayName
	if name == "" {
		name = item.Name
	}
	return fmt.Sprintf("模型「%s」所属供应商「%s」的接口类型不受支持，%s不可用；"+
		"请在 AI 设置中改用 OpenAI 兼容协议，或重新选择模型", name, item.ProviderName, feature)
}

func (s *AIConfigService) SetActiveConfig(ctx context.Context, actor *model.User, req *schema.SetActiveConfigRequest) (*schema.ActiveConfigResponse, error) {
	if err := authorizeSuperuser(actor); err != nil {
		return nil, err
	}

	set := func(key string, modelID *uint, want model.AIModelType) error {
		if modelID == nil {
			return nil
		}
		if *modelID == 0 {
			// 传 0 表示清空该配置。
			return s.repos.Settings.Set(ctx, key, "", "", actor.ID)
		}
		m, err := s.repos.AIModels.Get(ctx, *modelID)
		if err != nil {
			return err
		}
		// 校验用途匹配：把向量模型设成对话模型会导致调用时才发现问题。
		// 多模态模型可以同时满足多项，所以这里判的是"具备其中之一"而非相等。
		if want != "" && !m.Caps().Has(want) {
			return apperr.New(apperr.CodeInvalidParam, "模型用途与配置项不匹配").
				WithDetail("模型「%s」具备的能力是 %s，不含 %s", m.Name, m.Caps().String(), want)
		}
		if !m.IsActive {
			return apperr.New(apperr.CodeUnprocessable, "该模型已被禁用，无法设为默认")
		}
		return s.repos.Settings.Set(ctx, key, strconv.FormatUint(uint64(m.ID), 10), "", actor.ID)
	}

	if err := set(model.SettingAITextModelID, req.TextModelID, model.AIModelText); err != nil {
		return nil, err
	}
	if err := set(model.SettingAIVisionModelID, req.VisionModelID, model.AIModelVision); err != nil {
		return nil, err
	}
	if err := set(model.SettingAIEmbeddingModelID, req.EmbeddingModelID, model.AIModelEmbedding); err != nil {
		return nil, err
	}

	return s.GetActiveConfig(ctx, actor)
}

// ---- 运行时解析 ----

// ResolvedModel 解析好的模型调用配置。
type ResolvedModel struct {
	Provider     ai.Provider
	ModelID      uint
	ModelName    string
	Display      string
	ProviderName string
	MaxTokens    int
}

// Resolve 解析指定用途的模型，供业务代码调用。
func (s *AIConfigService) Resolve(ctx context.Context, t model.AIModelType) (*ResolvedModel, error) {
	key, missingMsg := settingFor(t)

	raw := s.repos.Settings.Get(ctx, key, "")
	if raw == "" {
		return nil, apperr.New(apperr.CodeAIUnavailable, missingMsg)
	}
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || id == 0 {
		return nil, apperr.New(apperr.CodeAIUnavailable, missingMsg)
	}

	m, err := s.repos.AIModels.Get(ctx, uint(id))
	if err != nil {
		return nil, apperr.New(apperr.CodeAIUnavailable,
			"当前配置的模型已不存在，请在 AI 设置中重新选择")
	}
	if !m.IsActive {
		return nil, apperr.New(apperr.CodeAIUnavailable,
			"当前配置的模型已被禁用，请在 AI 设置中重新选择")
	}
	if m.Provider == nil {
		return nil, apperr.New(apperr.CodeAIUnavailable,
			"模型所属的供应商已被删除，请在 AI 设置中重新配置")
	}
	if !m.Provider.IsActive {
		return nil, apperr.New(apperr.CodeAIUnavailable,
			"模型所属的供应商已被禁用")
	}

	plainKey, err := s.cipher.Decrypt(m.Provider.APIKeyEncrypted)
	if err != nil {
		return nil, apperr.New(apperr.CodeAIUnavailable,
			"供应商密钥解密失败，请重新填写 API Key")
	}
	if plainKey == "" {
		return nil, apperr.New(apperr.CodeAIUnavailable, "供应商尚未配置 API Key")
	}

	provider, err := ai.NewProvider(ai.Config{
		ID:            m.Provider.ID,
		Name:          m.Provider.Name,
		InterfaceType: string(m.Provider.InterfaceType),
		BaseURL:       m.Provider.BaseURL,
		APIKey:        plainKey,
		Timeout:       s.timeout,
		MaxRetries:    s.maxRetries,
	})
	if err != nil {
		return nil, apperr.New(apperr.CodeAIUnavailable, err.Error())
	}

	display := m.DisplayName
	if display == "" {
		display = m.Name
	}
	return &ResolvedModel{
		Provider:     provider,
		ModelID:      m.ID,
		ModelName:    m.Name,
		Display:      display,
		ProviderName: m.Provider.Name,
		MaxTokens:    m.MaxTokens,
	}, nil
}

// ResolveOptional 解析可选用途的模型；未配置时返回 (nil, nil) 而不是错误。
// 用于向量检索这类"没有也能跑"的能力。
func (s *AIConfigService) ResolveOptional(ctx context.Context, t model.AIModelType) *ResolvedModel {
	rm, err := s.Resolve(ctx, t)
	if err != nil {
		return nil
	}
	return rm
}

// ---- 内部辅助 ----

func settingFor(t model.AIModelType) (key, missingMsg string) {
	switch t {
	case model.AIModelText:
		return model.SettingAITextModelID, "尚未配置文本模型，请先在 AI 设置中选择"
	case model.AIModelVision:
		return model.SettingAIVisionModelID, "尚未配置视觉模型，图片识题不可用"
	case model.AIModelEmbedding:
		return model.SettingAIEmbeddingModelID, "尚未配置向量模型，知识点语义检索不可用"
	}
	return "", "未知的模型用途"
}

// inUseByModel 返回每个当前生效的模型被用在了哪些用途上。
//
// 返回 map 而非 set：一个多模态模型可以同时是默认文本模型和默认视觉模型，
// 而这正是要显示给用户看的信息（"使用中：文本、视觉"），
// 压成一个 true/false 就把这个区别抹掉了。
func (s *AIConfigService) inUseByModel(ctx context.Context) (map[uint][]model.AIModelType, error) {
	out := map[uint][]model.AIModelType{}
	for _, t := range model.AIModelTypes {
		key, _ := settingFor(t)
		raw := s.repos.Settings.Get(ctx, key, "")
		if raw == "" {
			continue
		}
		id, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || id == 0 {
			continue
		}
		out[uint(id)] = append(out[uint(id)], t)
	}
	return out, nil
}

// inUseValues 把用途集合转成响应里的字符串切片。
func inUseValues(list []model.AIModelType) []string {
	if len(list) == 0 {
		return nil
	}
	out := make([]string, len(list))
	for i, t := range list {
		out[i] = string(t)
	}
	return out
}

// clearIfActive 若该模型正被选为默认，则清空对应设置。
//
// 按用途逐项清，而不是"是默认就全清"：多模态模型可能只被选为默认视觉模型，
// 把文本那一栏一起清掉会让用户在毫不知情的情况下丢掉已生效的配置。
func (s *AIConfigService) clearIfActive(ctx context.Context, modelID uint, actorID uint) error {
	for _, t := range model.AIModelTypes {
		key, _ := settingFor(t)
		if key == "" {
			continue
		}
		raw := s.repos.Settings.Get(ctx, key, "")
		if raw == "" {
			continue
		}
		if id, err := strconv.ParseUint(raw, 10, 64); err == nil && uint(id) == modelID {
			if err := s.repos.Settings.Set(ctx, key, "", "", actorID); err != nil {
				return apperr.ErrInternal.WithCause(err)
			}
		}
	}
	return nil
}

// autoSelectIfFirst 该模型具备、却还没有默认模型的用途，把新模型设为默认。
func (s *AIConfigService) autoSelectIfFirst(ctx context.Context, m *model.AIModel, actorID uint) error {
	for _, t := range m.Caps() {
		key, _ := settingFor(t)
		if key == "" {
			continue
		}
		if s.repos.Settings.Get(ctx, key, "") != "" {
			continue
		}
		if err := s.repos.Settings.Set(ctx, key, strconv.FormatUint(uint64(m.ID), 10), "", actorID); err != nil {
			return apperr.ErrInternal.WithCause(err)
		}
	}
	return nil
}

// TestProvider 测试供应商连通性。
func (s *AIConfigService) TestProvider(ctx context.Context, actor *model.User, providerID uint, req *schema.ProviderTestRequest) (*schema.ProviderTestResponse, error) {
	if err := authorizeSuperuser(actor); err != nil {
		return nil, err
	}

	provider, err := s.repos.AIProviders.Get(ctx, providerID)
	if err != nil {
		return nil, err
	}

	var target *model.AIModel
	if req.ModelID > 0 {
		m, err := s.repos.AIModels.Get(ctx, req.ModelID)
		if err != nil {
			return nil, err
		}
		if m.ProviderID != providerID {
			return nil, apperr.New(apperr.CodeCrossSubject, "该模型不属于此供应商")
		}
		target = m
	} else {
		list, err := s.repos.AIModels.ListByProvider(ctx, providerID)
		if err != nil {
			return nil, apperr.ErrInternal.WithCause(err)
		}
		for i := range list {
			if list[i].Caps().Has(model.AIModelText) && list[i].IsActive {
				target = &list[i]
				break
			}
		}
	}
	if target == nil {
		return nil, apperr.New(apperr.CodeUnprocessable,
			"该供应商下没有可用的文本模型，请先添加模型")
	}

	plainKey, err := s.cipher.Decrypt(provider.APIKeyEncrypted)
	if err != nil {
		return nil, apperr.New(apperr.CodeAIUnavailable, "供应商密钥解密失败，请重新填写 API Key")
	}

	client, err := ai.NewProvider(ai.Config{
		ID:            provider.ID,
		Name:          provider.Name,
		InterfaceType: string(provider.InterfaceType),
		BaseURL:       provider.BaseURL,
		APIKey:        plainKey,
		Timeout:       s.timeout,
		MaxRetries:    0, // 连通性测试不重试，快速给出结论
	})
	if err != nil {
		return nil, apperr.New(apperr.CodeAIUnavailable, err.Error())
	}

	prompt := req.Prompt
	if prompt == "" {
		prompt = "请只回复两个字：正常"
	}

	resp := &schema.ProviderTestResponse{Model: target.Name}
	started := time.Now()

	var reply strings.Builder
	callCtx, cancel := context.WithTimeout(ctx, s.timeoutOrDefault())
	defer cancel()

	err = client.ChatStream(callCtx, ai.ChatRequest{
		Model:       target.Name,
		Messages:    []ai.Message{{Role: ai.RoleUser, Content: prompt}},
		MaxTokens:   64,
		Temperature: float64Ptr(0),
	}, func(c ai.Chunk) error {
		reply.WriteString(c.Text)
		// 收到一点内容就够判断连通性了，不必等模型说完整段。
		if reply.Len() > 100 {
			return errStopStream
		}
		return nil
	})

	resp.LatencyMs = time.Since(started).Milliseconds()

	// 主动中断不算失败。
	if err != nil && !errors.Is(err, errStopStream) {
		resp.OK = false
		resp.Error = humanizeAIError(err)
		return resp, nil
	}

	resp.OK = true
	resp.Reply = truncateRunes(reply.String(), 100)
	return resp, nil
}

// errStopStream 用于在已经拿到足够内容后主动结束流。
var errStopStream = errors.New("ai: 已收到足够内容，主动结束")

func (s *AIConfigService) timeoutOrDefault() time.Duration {
	if s.timeout > 0 {
		return s.timeout
	}
	return 60 * time.Second
}

// humanizeAIError 把领域错误翻译成用户能看懂的话。
func humanizeAIError(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ai.ErrUnauthorized):
		return "API Key 无效或权限不足"
	case errors.Is(err, ai.ErrModelNotFound):
		return "模型不存在或未开通，请检查模型名"
	case errors.Is(err, ai.ErrRateLimited):
		return "请求被限流或额度不足"
	case errors.Is(err, ai.ErrTimeout):
		return "请求超时，请检查网络或网关地址"
	case errors.Is(err, ai.ErrTruncated):
		// 截断不是"再试一次"能解决的：同样的输入会得到同样的截断。
		// 直接把该去哪儿调说清楚，比"服务不可用"有用得多。
		return truncateHint(err)
	case errors.Is(err, ai.ErrBlockedAddress):
		// 不要只说"连不上"：这句话直接告诉运维该改哪个配置。
		return "网关地址指向了内网或本机地址，已阻止（本机自建网关可设置 QM_AI_ALLOW_PRIVATE_URL=true 放开）"
	case errors.Is(err, ai.ErrBadRequest):
		return "上游拒绝了请求，请检查模型名、参数与网关地址"
	case errors.Is(err, ai.ErrUnavailable):
		return "供应商服务暂时不可用"
	}
	return err.Error()
}

// truncateHint 剥掉错误前缀，只留可执行的那句话。
func truncateHint(err error) string {
	msg := err.Error()
	if i := strings.Index(msg, "："); i >= 0 {
		return msg[i+len("："):]
	}
	return msg
}

func float64Ptr(v float64) *float64 { return &v }

func truncateRunes(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}
