package service

import (
	"context"
	"log/slog"
	"strings"

	"github.com/NanamiKyoka/questionmind/internal/importing"
	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
	"github.com/NanamiKyoka/questionmind/internal/repository"
	"github.com/NanamiKyoka/questionmind/internal/schema"
)

// PromptService 提示词模板用例。
//
// 它同时是三件事，缺一不可：
//
//  1. 管理接口（列表/新建/改/删、学科覆盖的读写）；
//  2. importing.TemplateStore 的实现——智能录题在运行时通过它取模板；
//  3. AI 助手系统提示词的解析入口（service/chat.go 走 importing 的解析链，
//     那条链最终落在这里）。
//
// **为什么第 2 点不是每次现查数据库，而是靠构造时注册一次**：
// BuildSystemPrompt 没有 ctx、也没有仓储，是 importing 包里的纯函数
// （service/import.go 调它时并不会传 service 进来）。与其把参数一路改下去，
// 不如在装配期把"能查模板的东西"交给它一次。注册在构造函数里，
// 于是"接口能通"与"提示词读得到"永远是同一件事——不会出现路由挂好了、
// 解析链却还连着内置默认的半成品状态。
type PromptService struct {
	repos *Repos
	tpl   *repository.PromptRepo
}

// NewPromptService 构造，并把自身注册为 importing 的模板源。
func NewPromptService(repos *Repos) *PromptService {
	s := &PromptService{
		repos: repos,
		// 仓储集合（service/service.go 的 Repos）里没有它，而改那份文件
		// 不在本页的职责内。与 ChatService 取 AgentRunRepo 同一手法：
		// 从已有的 TxManager 拿底层连接自己建一个。
		tpl: repository.NewPromptRepo(repos.Tx.DB()),
	}
	importing.UseTemplateStore(s)
	return s
}

// ---- 运行时解析 ----

// LookupTemplate 实现 importing.TemplateStore。
//
// 解析链（任一环缺失就往下一环找）：
//
//	学科覆盖 → 全局模板 → 系统设置兜底（仅录题两个键）→ 内置默认（由调用方兜）
//
// 返回 (空, nil) 表示"没配置"，不是错误：这是解析链上最常见的一种情况。
func (s *PromptService) LookupTemplate(ctx context.Context, key string, subjectID *uint) (importing.TemplateHit, error) {
	if subjectID != nil && *subjectID > 0 {
		if t, err := s.tpl.FindByKey(ctx, model.SubjectPromptKey(key, *subjectID)); err != nil {
			return importing.TemplateHit{}, err
		} else if t != nil && strings.TrimSpace(t.SystemPrompt) != "" {
			return importing.TemplateHit{
				Body:       t.SystemPrompt,
				Source:     importing.SourceSubject,
				TemplateID: t.ID,
				SubjectID:  subjectID,
			}, nil
		}
	}

	if t, err := s.tpl.FindByKey(ctx, key); err != nil {
		return importing.TemplateHit{}, err
	} else if t != nil && strings.TrimSpace(t.SystemPrompt) != "" {
		return importing.TemplateHit{
			Body:       t.SystemPrompt,
			Source:     importing.SourceGlobal,
			TemplateID: t.ID,
		}, nil
	}

	// 兜底键：老版本（以及现在的系统设置页）往这两个键里写过提示词。
	// 它们的优先级最低——模板表能按学科覆盖、能用界面编辑，
	// 管理员真要改提示词应该去「提示词」页。
	if spec, ok := importing.SpecOf(key); ok && spec.SettingKey != "" {
		if v := strings.TrimSpace(s.repos.Settings.Get(ctx, spec.SettingKey, "")); v != "" {
			return importing.TemplateHit{Body: v, Source: importing.SourceSetting}, nil
		}
	}

	return importing.TemplateHit{}, nil
}

// ---- 管理接口：全局模板 ----

// List 返回提示词目录 + 每个提示词当前保存的全局模板。
//
// 目录驱动而不是"只列数据库里有的行"：没配过的提示词也必须出现在界面上，
// 否则管理员会以为这个功能不存在。
func (s *PromptService) List(ctx context.Context, actor *model.User) (*schema.PromptListResponse, error) {
	if err := authorizeSuperuser(actor); err != nil {
		return nil, err
	}

	rows, err := s.tpl.List(ctx)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}

	// 一次读全表再按基础键归拢：模板行一共就那么几条，
	// 为每条提示词单独查一次学科覆盖纯属浪费。
	byKey := make(map[string][]model.PromptTemplate, len(rows))
	overrides := make(map[string]int, len(rows))
	for _, row := range rows {
		base := model.BasePromptKey(row.Key)
		// 学科覆盖行不混进全局分组：它们在学科页单独管理，
		// 这里只统计数量，好让管理员知道"有几个学科配过"。
		if _, ok := model.PromptSubjectID(row.Key); ok {
			overrides[base]++
			continue
		}
		byKey[base] = append(byKey[base], row)
	}

	items := make([]schema.PromptCatalogEntry, 0, len(importing.PromptSpecs()))
	for _, spec := range importing.PromptSpecs() {
		entry := schema.PromptCatalogEntry{
			Key:             spec.Key,
			Name:            spec.Name,
			Scene:           spec.Scene,
			Description:     spec.Description,
			Placeholders:    schema.NewPromptPlaceholders(spec),
			Default:         spec.Default,
			SettingKey:      spec.SettingKey,
			OverrideCount:   overrides[spec.Key],
			EffectiveSource: importing.SourceBuiltin,
		}

		// 全局模板至多一行（key 唯一）。万一有重复行（历史脏数据），
		// 取最后一条并把重复这件事记进日志——静默取第一条会让人
		// "改了这一条不生效"。
		global := byKey[spec.Key]
		if len(global) > 1 {
			slog.Warn("同一提示词存在多行全局模板，取最后一行", "key", spec.Key, "count", len(global))
		}
		if len(global) > 0 {
			resp := schema.NewPromptTemplateResponse(&global[len(global)-1], spec)
			entry.Template = &resp
		}

		hit, err := s.LookupTemplate(ctx, spec.Key, nil)
		if err != nil {
			// 列表页不该因为读模板失败就整页打不开：把来源标成 builtin
			// 并照常返回，管理员依然能看到并修好模板。
			slog.Warn("读取提示词来源失败", "key", spec.Key, "error", err)
		} else if hit.Body != "" {
			entry.EffectiveSource = hit.Source
		} else if entry.Template == nil {
			entry.Fallback = "未配置模板，正在使用内置默认"
		}

		// 存下来了、但渲染不出内容的模板是最糟的一种状态：
		// 界面上看着配好了，运行时却每次都悄悄回落到默认。
		// 判据与运行时共用 importing.ExplainTemplate。
		if entry.Template != nil {
			entry.Fallback = joinText(entry.Fallback, describeTemplate(spec, entry.Template.SystemPrompt))
		}
		items = append(items, entry)
	}

	return &schema.PromptListResponse{Items: items}, nil
}

// Create 新建模板。全局与学科覆盖走同一个入口：差别只在键。
func (s *PromptService) Create(ctx context.Context, actor *model.User, req *schema.PromptCreateRequest) (*schema.PromptTemplateResponse, error) {
	if err := authorizeSuperuser(actor); err != nil {
		return nil, err
	}
	spec, err := requireSpec(req.Key)
	if err != nil {
		return nil, err
	}
	body, err := normalizePromptBody(req.SystemPrompt)
	if err != nil {
		return nil, err
	}

	subjectID, err := s.resolveScope(ctx, req.SubjectID)
	if err != nil {
		return nil, err
	}

	key := spec.Key
	if subjectID != nil {
		key = model.SubjectPromptKey(spec.Key, *subjectID)
	}

	// 键唯一，靠唯一索引兜住并发：两个管理员同时提交，
	// 第二个会撞唯一键而不是悄悄覆盖第一个的版本。
	existing, err := s.tpl.FindByKey(ctx, key)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	if existing != nil {
		return nil, apperr.New(apperr.CodeDuplicateName, "该提示词已经配置过了").
			WithDetail("key=%s，请直接修改现有模板", key)
	}

	row := &model.PromptTemplate{
		Key:          key,
		Name:         spec.Name,
		Scene:        spec.Scene,
		SystemPrompt: body,
		UpdatedBy:    actor.ID,
	}
	if err := s.tpl.Create(ctx, row); err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	resp := schema.NewPromptTemplateResponse(row, spec)
	return &resp, nil
}

// Update 修改模板正文。
func (s *PromptService) Update(ctx context.Context, actor *model.User, id uint, req *schema.PromptUpdateRequest) (*schema.PromptTemplateResponse, error) {
	if err := authorizeSuperuser(actor); err != nil {
		return nil, err
	}
	row, err := s.tpl.Get(ctx, id)
	if err != nil {
		return nil, apperr.From(err)
	}
	spec, err := requireSpec(model.BasePromptKey(row.Key))
	if err != nil {
		return nil, err
	}
	if req.SystemPrompt == nil {
		return nil, apperr.New(apperr.CodeInvalidParam, "缺少要修改的提示词正文")
	}
	body, err := normalizePromptBody(*req.SystemPrompt)
	if err != nil {
		return nil, err
	}

	if err := s.tpl.UpdateFields(ctx, id, map[string]any{
		"system_prompt": body,
		"updated_by":    actor.ID,
	}); err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	row.SystemPrompt = body
	row.UpdatedBy = actor.ID
	resp := schema.NewPromptTemplateResponse(row, spec)
	return &resp, nil
}

// Delete 删除模板。删掉之后该作用域回退到下一级来源（学科覆盖 → 全局 → 内置）。
func (s *PromptService) Delete(ctx context.Context, actor *model.User, id uint) error {
	if err := authorizeSuperuser(actor); err != nil {
		return err
	}
	if err := s.tpl.Delete(ctx, id); err != nil {
		return apperr.From(err)
	}
	return nil
}

// ---- 管理接口：学科覆盖 ----

// SubjectPrompts 读取某学科的覆盖情况与最终生效值。
func (s *PromptService) SubjectPrompts(ctx context.Context, actor *model.User, subjectID uint) (*schema.SubjectPromptsResponse, error) {
	if err := authorizeSuperuser(actor); err != nil {
		return nil, err
	}
	subject, err := s.repos.Subjects.Get(ctx, subjectID)
	if err != nil {
		return nil, apperr.From(err)
	}

	out := &schema.SubjectPromptsResponse{
		SubjectID:   subject.ID,
		SubjectName: subject.Name,
		Prompts:     make([]schema.SubjectPromptItem, 0, len(importing.PromptSpecs())),
	}

	for _, spec := range importing.PromptSpecs() {
		hit, err := s.LookupTemplate(ctx, spec.Key, &subjectID)
		if err != nil {
			return nil, apperr.ErrInternal.WithCause(err)
		}

		item := schema.SubjectPromptItem{
			Key:          spec.Key,
			Name:         spec.Name,
			Scene:        spec.Scene,
			Description:  spec.Description,
			Placeholders: schema.NewPromptPlaceholders(spec),
			Default:      spec.Default,
			Source:       importing.SourceBuiltin,
			Fallback:     "该学科没有覆盖，正在使用内置默认",
		}
		switch {
		case hit.Body != "":
			item.Effective = hit.Body
			item.Source = hit.Source
			if hit.Source == importing.SourceSubject {
				item.Fallback = ""
			}
		default:
			item.Effective = spec.Default
		}

		// 覆盖行本身（含最后修改时间）单独取一次：LookupTemplate 只返回
		// 命中的结果，不返回"这一学科原来存了什么"。
		if row, err := s.tpl.FindByKey(ctx, model.SubjectPromptKey(spec.Key, subjectID)); err != nil {
			return nil, apperr.ErrInternal.WithCause(err)
		} else if row != nil {
			item.OverrideID = row.ID
			updated := row.UpdatedAt
			item.OverrideUpdatedAt = &updated
			item.Fallback = joinText(item.Fallback, describeTemplate(spec, row.SystemPrompt))
		}

		// 预览：把学科名与学科说明代进去之后的样子。知识点/标签清单在预览里
		// 是空的——真正抽取时才会灌进几百个名字，预览里放进去只会让人
		// 分不清自己改的是模板还是清单。
		item.Preview = importing.Render(item.Effective, subjectVars(subject))
		out.Prompts = append(out.Prompts, item)
	}
	return out, nil
}

// SaveSubjectPrompts 批量写入/删除某学科的覆盖。
//
// 批量而不是逐条接口：管理界面一次保存会同时动好几条提示词，
// 让管理员点五次"保存"只会制造出"保存到一半失败"的中间态。
// 整个批次放在一个事务里，要么全成要么全不成。
func (s *PromptService) SaveSubjectPrompts(ctx context.Context, actor *model.User, subjectID uint, req *schema.SubjectPromptsUpdateRequest) (*schema.SubjectPromptsResponse, error) {
	if err := authorizeSuperuser(actor); err != nil {
		return nil, err
	}
	if _, err := s.repos.Subjects.Get(ctx, subjectID); err != nil {
		return nil, apperr.From(err)
	}

	// 先整体校验再落库：不能在事务里返回业务错误，
	// 否则前面几条已经写入、后面一条报错的中间态会留在库里。
	type action struct {
		key  string
		id   uint
		body string
	}
	plan := make([]action, 0, len(req.Prompts))
	seen := make(map[string]bool, len(req.Prompts))
	for _, item := range req.Prompts {
		spec, err := requireSpec(item.Key)
		if err != nil {
			return nil, err
		}
		if seen[spec.Key] {
			return nil, apperr.New(apperr.CodeInvalidParam, "同一条提示词出现了两次").
				WithDetail("key=%s", spec.Key)
		}
		seen[spec.Key] = true

		key := model.SubjectPromptKey(spec.Key, subjectID)
		existing, err := s.tpl.FindByKey(ctx, key)
		if err != nil {
			return nil, apperr.ErrInternal.WithCause(err)
		}

		body := strings.TrimSpace(item.SystemPrompt)
		if body == "" {
			// 空 = 删掉这条覆盖。没有覆盖行时是无操作，不必报错。
			if existing != nil {
				plan = append(plan, action{key: key, id: existing.ID})
			}
			continue
		}
		if _, err := normalizePromptBody(body); err != nil {
			return nil, err
		}
		if existing != nil {
			// 已存在的直接改：走 Update 而不是"删了再建"，
			// 这样 id 与 created_at 保持不变，界面上的"最后修改"才有意义。
			if err := s.tpl.UpdateFields(ctx, existing.ID, map[string]any{
				"system_prompt": body,
				"updated_by":    actor.ID,
			}); err != nil {
				return nil, apperr.ErrInternal.WithCause(err)
			}
			continue
		}
		plan = append(plan, action{key: key, body: body})
	}

	// 删除与新建放在一起执行：先删后建，避免同一批里"删 A 又建 A"时撞唯一键。
	err := s.repos.Tx.Within(ctx, func(ctx context.Context) error {
		for _, a := range plan {
			if a.id == 0 {
				continue
			}
			if err := s.tpl.Delete(ctx, a.id); err != nil {
				return err
			}
		}
		for _, a := range plan {
			if a.id != 0 {
				continue
			}
			spec, _ := importing.SpecOf(model.BasePromptKey(a.key))
			row := &model.PromptTemplate{
				Key:          a.key,
				Name:         spec.Name,
				Scene:        spec.Scene,
				SystemPrompt: a.body,
				UpdatedBy:    actor.ID,
			}
			if err := s.tpl.Create(ctx, row); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}

	return s.SubjectPrompts(ctx, actor, subjectID)
}

// ---- 辅助 ----

// requireSpec 校验提示词键必须来自目录。
//
// 这是"不发明产品里没有的提示词"的落地点：自由文本的键会被存成一行
// 永远不会被任何代码读取的模板，而管理员会以为它生效了。
func requireSpec(key string) (importing.PromptSpec, error) {
	spec, ok := importing.SpecOf(key)
	if !ok {
		return importing.PromptSpec{}, unknownPromptKey(key)
	}
	return spec, nil
}

// describeTemplate 说明一段已保存的模板在什么情况下会失效。
//
// 两种失效程度要分开说，因为处置方式不同：
//   - 无论取什么值都渲染不出内容：这条模板根本是废的，运行时永远用不到它；
//   - 没有任何上下文时才渲染不出：例如整条模板只由 {{subject_name}} 组成，
//     在"跨学科会话"这种没有学科的场景下会被整行删空，运行时回落到内置默认。
//
// 判据用 importing.ExplainTemplate，与运行时同源。
func describeTemplate(spec importing.PromptSpec, tmpl string) string {
	if ok, reason := importing.ExplainTemplate(tmpl, importing.SampleVars(spec)); !ok {
		return "已保存的模板" + reason + "，运行时不会使用它"
	}
	if ok, _ := importing.ExplainTemplate(tmpl, importing.EmptyVars(spec)); !ok {
		return "已保存的模板完全由占位符组成，在没有学科等上下文的场景下会渲染为空，" +
			"届时运行时自动回落到内置默认"
	}
	return ""
}

// unknownPromptKey 目录外的键。
func unknownPromptKey(key string) error {
	return apperr.New(apperr.CodeInvalidParam, "未知的提示词").
		WithDetail("key=%q 不在提示词目录中", key)
}

// joinText 拼接多条给管理员看的说明。
func joinText(a, b string) string {
	if a == "" {
		return b
	}
	return a + "；" + b
}

// normalizePromptBody 规整模板正文。
func normalizePromptBody(raw string) (string, error) {
	body := strings.TrimSpace(raw)
	if body == "" {
		// 存空串等于"删掉这条配置"，但界面给的是保存而不是删除——
		// 让人以为存进去了、实际什么都没生效，比直接报错难查。
		return "", apperr.New(apperr.CodeInvalidParam, "提示词不能为空").
			WithDetail("清空一条模板请用删除接口")
	}
	return body, nil
}

// resolveScope 校验学科范围：nil 表示全局。
func (s *PromptService) resolveScope(ctx context.Context, subjectID *uint) (*uint, error) {
	if subjectID == nil {
		return nil, nil
	}
	if *subjectID == 0 {
		return nil, apperr.New(apperr.CodeInvalidParam, "学科 id 不合法")
	}
	if _, err := s.repos.Subjects.Get(ctx, *subjectID); err != nil {
		return nil, apperr.From(err)
	}
	return subjectID, nil
}

// subjectVars 学科上下文变量。
//
// 目前只有预览用得上：知识点与标签刻意用真实学科名/说明，而清单类占位符
// 留空——预览时把几百个知识点全灌进去，管理员就看不出自己到底改了什么。
func subjectVars(subject *model.Subject) importing.Vars {
	return importing.Vars{
		"subject_name":        subject.Name,
		"subject_description": importing.DescribeSubject(subject.Description),
	}
}
