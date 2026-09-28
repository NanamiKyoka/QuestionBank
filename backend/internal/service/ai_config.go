package service

import (
	"context"
	"errors"
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

func (s *AIConfigService) ListProviders(ctx context.Context, actor *model.User) ([]schema.ProviderResponse, error) {
	if err := authorizeSuperuser(actor); err != nil {
		return nil, err
	}

	list, err := s.repos.AIProviders.List(ctx)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}

	active, err := s.inUseModelIDs(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]schema.ProviderResponse, 0, len(list))
	for i := range list {
		resp := schema.NewProviderResponse(&list[i])
		for j := range resp.Models {
			resp.Models[j].InUse = active[resp.Models[j].ID]
		}
		out = append(out, resp)
	}
	return out, nil
}

func (s *AIConfigService) CreateProvider(ctx context.Context, actor *model.User, req *schema.ProviderCreateRequest) (*schema.ProviderResponse, error) {
	if err := authorizeSuperuser(actor); err != nil {
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
		fields["interface_type"] = *req.InterfaceType
	}
	if req.BaseURL != nil {
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

	m := &model.AIModel{
		ProviderID:    req.ProviderID,
		Name:          req.Name,
		DisplayName:   req.DisplayName,
		ModelType:     model.AIModelType(req.ModelType),
		MaxTokens:     4096,
		ContextWindow: 32768,
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
	active, _ := s.inUseModelIDs(ctx)
	resp := schema.NewModelResponse(created)
	resp.InUse = active[resp.ID]
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
	if req.ModelType != nil {
		fields["model_type"] = *req.ModelType
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
	active, _ := s.inUseModelIDs(ctx)
	resp := schema.NewModelResponse(updated)
	resp.InUse = active[resp.ID]
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

	if textID > 0 {
		if m, err := s.repos.AIModels.Get(ctx, textID); err == nil {
			item := schema.NewModelResponse(m)
			resp.Text = &item
		}
	}
	if visionID > 0 {
		if m, err := s.repos.AIModels.Get(ctx, visionID); err == nil {
			item := schema.NewModelResponse(m)
			resp.Vision = &item
		}
	}
	if embeddingID > 0 {
		if m, err := s.repos.AIModels.Get(ctx, embeddingID); err == nil {
			item := schema.NewModelResponse(m)
			resp.Embedding = &item
		}
	}

	resp.Ready = resp.Text != nil
	switch {
	case resp.Text == nil:
		resp.Message = "尚未配置文本模型，AI 助手与智能录题不可用"
	case resp.Vision == nil:
		resp.Message = "已配置文本模型；未配置视觉模型，图片识题不可用"
	case resp.Embedding == nil:
		resp.Message = "已配置文本与视觉模型；未配置向量模型，知识点语义检索不可用"
	default:
		resp.Message = "AI 功能已就绪"
	}
	return resp, nil
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
		if want != "" && m.ModelType != want {
			return apperr.New(apperr.CodeInvalidParam, "模型用途与配置项不匹配").
				WithDetail("期望 %s，实际 %s", want, m.ModelType)
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

// inUseModelIDs 返回当前被选为默认的模型 id 集合。
func (s *AIConfigService) inUseModelIDs(ctx context.Context) (map[uint]bool, error) {
	out := map[uint]bool{}
	for _, key := range model.SettingAIKeys {
		raw := s.repos.Settings.Get(ctx, key, "")
		if raw == "" {
			continue
		}
		if id, err := strconv.ParseUint(raw, 10, 64); err == nil && id > 0 {
			out[uint(id)] = true
		}
	}
	return out, nil
}

// clearIfActive 若该模型正被选为默认，则清空对应设置。
func (s *AIConfigService) clearIfActive(ctx context.Context, modelID uint, actorID uint) error {
	active, err := s.inUseModelIDs(ctx)
	if err != nil {
		return err
	}
	if !active[modelID] {
		return nil
	}
	for _, key := range model.SettingAIKeys {
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

// autoSelectIfFirst 该用途还没有默认模型时，把新模型设为默认。
func (s *AIConfigService) autoSelectIfFirst(ctx context.Context, m *model.AIModel, actorID uint) error {
	key, _ := settingFor(m.ModelType)
	if key == "" {
		return nil
	}
	if s.repos.Settings.Get(ctx, key, "") != "" {
		return nil
	}
	if err := s.repos.Settings.Set(ctx, key, strconv.FormatUint(uint64(m.ID), 10), "", actorID); err != nil {
		return apperr.ErrInternal.WithCause(err)
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
			if list[i].ModelType == model.AIModelText && list[i].IsActive {
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
	case errors.Is(err, ai.ErrUnavailable):
		return "供应商服务暂时不可用"
	}
	return err.Error()
}

func float64Ptr(v float64) *float64 { return &v }

func truncateRunes(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}
