package service

import (
	"context"
	"strings"
	"time"

	"github.com/NanamiKyoka/questionmind/internal/ai"
	"github.com/NanamiKyoka/questionmind/internal/importing"
	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
	"github.com/NanamiKyoka/questionmind/internal/repository"
	"github.com/NanamiKyoka/questionmind/internal/schema"
)

// extractTimeout 单次抽取的超时。
//
// 比普通对话宽松得多：一份几十道题的试卷，模型要逐题输出结构化 JSON，
// 耗时通常在 30 秒到两分钟。给 3 分钟是为了避免"刚跑到一半就被掐断"。
const extractTimeout = 3 * time.Minute

// ImportService 智能录题用例。
type ImportService struct {
	repos     *Repos
	aiConfig  *AIConfigService
	questions *QuestionService
	// vectors 可选：为草稿提供知识点语义匹配建议。
	vectors *VectorService
}

func NewImportService(repos *Repos, aiConfig *AIConfigService, questions *QuestionService) *ImportService {
	return &ImportService{repos: repos, aiConfig: aiConfig, questions: questions}
}

// WithVectors 注入向量服务。未注入时跳过知识点语义匹配。
func (s *ImportService) WithVectors(v *VectorService) *ImportService {
	s.vectors = v
	return s
}

// ctxErr 判断上下文是否已取消，用于在长循环里及时退出。
func (s *ImportService) ctxErr(ctx context.Context) bool {
	return ctx.Err() != nil
}

// Extract 从文本或图片中抽取题目。
//
// 这一步**不写题库**：抽取结果只是草稿，必须经过人工审核再提交。
// 原因是模型识别题干、切分选项、解析答案都可能出错，
// 直接入库会污染题库，而且错误答案比没有答案更危险。
func (s *ImportService) Extract(ctx context.Context, user *model.User, subjectID uint, req *schema.ExtractRequest) (*schema.ExtractResponse, error) {
	if err := authorize(user, model.PermEditQuestion, subjectID); err != nil {
		return nil, err
	}

	subject, err := s.repos.Subjects.Get(ctx, subjectID)
	if err != nil {
		return nil, err
	}

	mode := importing.Mode(req.Mode)
	if mode == "" {
		mode = importing.ModeExtract
	}
	if !mode.Valid() {
		return nil, apperr.New(apperr.CodeInvalidParam, "不支持的导入模式").
			WithDetail("mode=%s", req.Mode)
	}

	content := strings.TrimSpace(req.Content)
	if mode == importing.ModeSolve {
		content = strings.TrimSpace(req.SolveForContent)
	}
	if content == "" && len(req.Images) == 0 {
		return nil, apperr.New(apperr.CodeInvalidParam, "请提供待识别的文本或图片")
	}
	if err := validateImages(req.Images); err != nil {
		return nil, err
	}

	// 解析当前配置的文本模型。未配置时给出可操作的提示，而不是笼统报错。
	resolved, err := s.aiConfig.Resolve(ctx, model.AIModelText)
	if err != nil {
		return nil, err
	}

	// 把已有知识点与标签喂给模型，让它优先复用而不是新造名字。
	kpNames, err := s.repos.KnowledgePoints.NamesBySubject(ctx, subjectID)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	tagNames, err := s.repos.Tags.NamesBySubject(ctx, subjectID)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}

	promptOpts := importing.PromptOptions{
		Mode:                    mode,
		SubjectName:             subject.Name,
		ExistingKnowledgePoints: kpNames,
		ExistingTags:            tagNames,
	}

	callCtx, cancel := context.WithTimeout(ctx, extractTimeout)
	defer cancel()

	started := time.Now()
	extracted, err := resolved.Provider.ExtractQuestions(callCtx, ai.ExtractRequest{
		Model:   resolved.ModelName,
		Prompt:  importing.BuildSystemPrompt(promptOpts),
		Content: importing.BuildUserContent(content, promptOpts),
		Images:  req.Images,
		Schema:  "", // schema 已经写进系统提示词
	})
	elapsed := time.Since(started)

	// 抽取失败也要留痕：用户会问"为什么没结果"。
	task := &model.ImportTask{
		SubjectID: &subjectID,
		UserID:    user.ID,
		Source:    model.ImportSourceAIAssist,
		Status:    model.ImportStatusCompleted,
		FileType:  fileTypeOf(req, mode),
		Mode:      string(mode),
	}
	if err != nil {
		task.Status = model.ImportStatusFailed
		task.ErrorMsg = humanizeAIError(err)
		_ = s.repos.ImportTasks.Create(ctx, task)
		return nil, apperr.New(apperr.CodeAIUnavailable, humanizeAIError(err)).WithCause(err)
	}

	drafts := importing.Normalize(extracted)

	// 用题干做一次语义检索，给出知识点候选。
	//
	// 向量能力是"有更好"而不是"必须有"：没配向量模型、索引为空、
	// 或调用失败时都静默跳过，不影响录题主流程。
	if s.vectors != nil {
		for i := range drafts {
			if s.ctxErr(ctx) {
				break
			}
			query := drafts[i].StemText
			if query == "" {
				query = drafts[i].StemMarkdown
			}
			suggestions := s.vectors.SuggestKnowledgePoints(ctx, subjectID, query, 3)
			for _, sg := range suggestions {
				drafts[i].SuggestedKnowledgePoints = append(drafts[i].SuggestedKnowledgePoints,
					importing.KnowledgeSuggestion{
						KnowledgePointID: sg.KnowledgePointID,
						Name:             sg.Name,
						Path:             sg.Path,
						Score:            sg.Score,
					})
			}
		}
	}

	task.TotalCount = len(drafts)
	task.SavedCount = 0 // 还没入库
	now := time.Now().UTC()
	task.FinishedAt = &now
	if err := s.repos.ImportTasks.Create(ctx, task); err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}

	warnings := 0
	for i := range drafts {
		if len(drafts[i].Warnings) > 0 {
			warnings++
		}
	}

	return &schema.ExtractResponse{
		TaskID:       task.ID,
		Drafts:       drafts,
		Total:        len(drafts),
		WarningCount: warnings,
		Model:        resolved.Display,
		ElapsedMs:    elapsed.Milliseconds(),
	}, nil
}

// Commit 把审核后的草稿写入题库。
//
// 关键设计：每一题都走 QuestionService.Create，而不是直接写库。
// 这样智能录题与手工录题共用同一套校验（题型与答案是否自洽、
// 选项引用是否合法、标签知识点是否属于本学科），不存在"AI 导入能绕过校验"的口子。
//
// 语义是**部分成功**：某一题不合格只跳过该题并回报原因，
// 不让整批失败——否则用户要重新审核几十道本来没问题的题。
func (s *ImportService) Commit(ctx context.Context, user *model.User, subjectID uint, req *schema.CommitRequest) (*schema.CommitResponse, error) {
	if err := authorize(user, model.PermEditQuestion, subjectID); err != nil {
		return nil, err
	}
	if _, err := s.repos.Subjects.Get(ctx, subjectID); err != nil {
		return nil, err
	}
	if len(req.Drafts) == 0 {
		return nil, apperr.New(apperr.CodeInvalidParam, "没有需要入库的题目")
	}

	// 默认关联先校验一遍归属，避免每题都重复查。
	if err := s.questions.checkTagOwnership(ctx, subjectID, req.DefaultTagIDs); err != nil {
		return nil, err
	}
	if err := s.questions.checkKPOwnership(ctx, subjectID, req.DefaultKnowledgePointIDs); err != nil {
		return nil, err
	}

	resp := &schema.CommitResponse{
		CreatedIDs: []uint{},
		Failures:   []schema.CommitFailure{},
	}

	createdIDs := make([]uint, 0, len(req.Drafts))
	failures := make([]schema.CommitFailure, 0)
	unresolvedNames := map[string]bool{}

	for i := range req.Drafts {
		draft := &req.Drafts[i]

		// 名称解析：草稿里的知识点/标签名称，能在本学科找到就关联，
		// 找不到就记下来提示用户，**不自动创建**。
		// 自动创建会迅速积累大量近义标签（"函数单调性" / "单调性" / "函数的单调性"），
		// 事后清理比当初手工选一次麻烦得多。
		kpIDs, tagIDs, unresolved := s.resolveNames(ctx, subjectID, draft.KnowledgePoints, draft.Tags)
		for _, n := range unresolved {
			unresolvedNames[n] = true
		}

		createReq, err := s.draftToCreateRequest(draft, subjectID, req)
		if err != nil {
			failures = append(failures, failureOf(i, draft, err))
			continue
		}
		createReq.KnowledgePointIDs = mergeIDs(kpIDs, req.DefaultKnowledgePointIDs)
		createReq.TagIDs = mergeIDs(tagIDs, req.DefaultTagIDs)

		created, err := s.questions.Create(ctx, user, createReq)
		if err != nil {
			failures = append(failures, failureOf(i, draft, err))
			continue
		}
		createdIDs = append(createdIDs, created.ID)
	}

	resp.CreatedIDs = createdIDs
	resp.CreatedCount = len(createdIDs)
	resp.Failures = failures
	resp.FailedCount = len(failures)

	// 记录本次入库结果。
	task := &model.ImportTask{
		SubjectID:   &subjectID,
		UserID:      user.ID,
		Source:      model.ImportSourceAIAssist,
		Status:      model.ImportStatusCompleted,
		FileType:    "text",
		Mode:        "commit",
		TotalCount:  len(req.Drafts),
		SavedCount:  len(createdIDs),
		FailedCount: len(failures),
	}
	now := time.Now().UTC()
	task.FinishedAt = &now
	if len(failures) > 0 && len(createdIDs) == 0 {
		task.Status = model.ImportStatusFailed
		task.ErrorMsg = "全部题目入库失败"
	}
	if err := s.repos.ImportTasks.Create(ctx, task); err != nil {
		// 入库结果已经产生，审计记录失败不应让用户以为题目没进去。
		task.ID = 0
	}
	resp.TaskID = task.ID

	if len(unresolvedNames) > 0 {
		// 未匹配的名称通过失败列表之外的方式提示不合适，
		// 这里直接并入响应里由前端展示。
		resp.UnresolvedNames = keysOf(unresolvedNames)
	}
	return resp, nil
}

// draftToCreateRequest 把草稿转换为建题请求。
func (s *ImportService) draftToCreateRequest(draft *importing.Draft, subjectID uint, req *schema.CommitRequest) (*schema.QuestionCreateRequest, error) {
	if !draft.QuestionType.Valid() {
		return nil, apperr.New(apperr.CodeInvalidParam, "题型不合法").
			WithDetail("question_type=%q", draft.QuestionType)
	}

	// 题干：以 Markdown 原文为准重新转换一次。
	// 用户在审核界面改的是 markdown 源码，服务端据此重新生成富文本，
	// 客户端不必（也不应该）自己构造富文本结构。
	stem := draft.Stem
	if strings.TrimSpace(draft.StemMarkdown) != "" {
		stem = importing.MarkdownToRichDoc(draft.StemMarkdown)
	}
	if stem.IsEmpty() {
		return nil, apperr.New(apperr.CodeInvalidParam, "题干不能为空")
	}

	var options []schema.OptionInput
	for i := range draft.Options {
		options = append(options, schema.OptionInput{
			Label:   draft.Options[i].Label,
			Content: draft.Options[i].Content,
		})
	}

	createReq := &schema.QuestionCreateRequest{
		SubjectID:    subjectID,
		QuestionType: draft.QuestionType,
		Stem:         stem,
		Options:      options,
		Answer:       draft.Answer,
		Analysis:     draft.Analysis,
		Solution:     draft.Solution,
		Summary:      draft.Summary,
		Source:       "AI 智能录题",
		Status:       req.Status,
	}
	if draft.Difficulty >= 1 && draft.Difficulty <= 5 {
		d := draft.Difficulty
		createReq.Difficulty = &d
	}
	return createReq, nil
}

// resolveNames 把名称解析为本学科的实体 id。
//
// 返回 (命中 id 列表, 命中 id 列表, 未命中的名称)。
// 未命中的名称不自动创建——理由见 Commit 的注释。
func (s *ImportService) resolveNames(ctx context.Context, subjectID uint, kpNames, tagNames []string) ([]uint, []uint, []string) {
	var unresolved []string

	var kpIDs []uint
	if len(kpNames) > 0 {
		mapping, err := s.repos.KnowledgePoints.IDsByNames(ctx, subjectID, kpNames)
		if err == nil {
			for _, n := range kpNames {
				if id, ok := mapping[n]; ok {
					kpIDs = append(kpIDs, id)
				} else {
					unresolved = append(unresolved, n)
				}
			}
		}
	}

	var tagIDs []uint
	if len(tagNames) > 0 {
		mapping, err := s.repos.Tags.IDsByNames(ctx, subjectID, tagNames)
		if err == nil {
			for _, n := range tagNames {
				if id, ok := mapping[n]; ok {
					tagIDs = append(tagIDs, id)
				} else {
					unresolved = append(unresolved, n)
				}
			}
		}
	}

	return kpIDs, tagIDs, unresolved
}

// ListTasks 导入历史。
func (s *ImportService) ListTasks(ctx context.Context, user *model.User, subjectID uint, status model.ImportTaskStatus, limit, offset int) ([]schema.ImportTaskResponse, int64, error) {
	if err := authorizeRead(user, subjectID); err != nil {
		return nil, 0, err
	}

	list, total, err := s.repos.ImportTasks.List(ctx, repository.ImportTaskListFilter{
		SubjectID: subjectID,
		Status:    status,
	}, limit, offset)
	if err != nil {
		return nil, 0, apperr.ErrInternal.WithCause(err)
	}

	out := make([]schema.ImportTaskResponse, 0, len(list))
	for i := range list {
		out = append(out, schema.NewImportTaskResponse(&list[i]))
	}
	return out, total, nil
}

// ---- 辅助 ----

// validateImages 校验图片是数据 URL 或 http(s) 地址。
func validateImages(images []string) error {
	for i, img := range images {
		s := strings.TrimSpace(img)
		if s == "" {
			return apperr.New(apperr.CodeInvalidParam, "图片内容为空").
				WithDetail("第 %d 张", i+1)
		}
		ok := strings.HasPrefix(s, "data:image/") ||
			strings.HasPrefix(s, "http://") ||
			strings.HasPrefix(s, "https://")
		if !ok {
			return apperr.New(apperr.CodeInvalidParam,
				"图片必须是 data URL 或 http(s) 地址").WithDetail("第 %d 张", i+1)
		}
		// 数据 URL 的大小上限：单张约 10MB（base64 后约 13MB）。
		if len(s) > 14<<20 {
			return apperr.New(apperr.CodeInvalidParam, "单张图片过大").
				WithDetail("第 %d 张约 %.1fMB", i+1, float64(len(s))/1024/1024)
		}
	}
	return nil
}

func failureOf(index int, draft *importing.Draft, err error) schema.CommitFailure {
	f := schema.CommitFailure{
		Index:  index,
		TempID: draft.TempID,
	}
	// 只取题干前 40 字，便于用户定位是哪一题。
	stem := []rune(strings.TrimSpace(draft.StemMarkdown))
	if len(stem) > 40 {
		f.Stem = string(stem[:40]) + "…"
	} else {
		f.Stem = string(stem)
	}

	if e := apperr.From(err); e != nil {
		f.Error = e.Message
		if e.Detail != "" {
			f.Error += "（" + e.Detail + "）"
		}
	} else {
		f.Error = err.Error()
	}
	return f
}

func fileTypeOf(req *schema.ExtractRequest, mode importing.Mode) string {
	if len(req.Images) > 0 && strings.TrimSpace(req.Content) == "" {
		return "image"
	}
	if mode == importing.ModeSolve {
		return "solve"
	}
	return "text"
}

func mergeIDs(a, b []uint) []uint {
	seen := make(map[uint]bool, len(a)+len(b))
	out := make([]uint, 0, len(a)+len(b))
	for _, group := range [][]uint{a, b} {
		for _, id := range group {
			if id == 0 || seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
