package service

import (
	"context"
	"sort"
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

	// 解析当前配置的模型。带图时必须用视觉模型：
	// 把图片塞给纯文本模型，供应商多半直接报错或者"看不见"地胡编一通，
	// 用户拿到的是一份看似正常、实则完全臆造的题目——比明确失败危险得多。
	modelType := model.AIModelText
	if len(req.Images) > 0 {
		modelType = model.AIModelVision
	}
	resolved, err := s.aiConfig.Resolve(ctx, modelType)
	if err != nil {
		if modelType == model.AIModelVision && apperr.Is(err, apperr.CodeAIUnavailable) {
			// 未配置视觉模型是可预期的配置状态，给出能照着做的提示，
			// 而不是把"AI 服务不可用"甩给用户。
			return nil, apperr.New(apperr.CodeAIUnavailable,
				"图片识题需要先配置视觉模型，请在 AI 设置中选择视觉模型后再试；"+
					"若只想录文字题，请改用文本方式导入").WithCause(err)
		}
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

	failures := make([]schema.CommitFailure, 0)
	unresolvedNames := map[string]bool{}

	// 母子题结构：模型用 temp_id / parent_temp_id 表达层级，而 parent_id 必须等
	// 母题建出来才知道真实主键，所以按层级深度分批落库——母题永远排在子题前面。
	// 一份 1 道母题 + 20 道小问的材料题如果不这么处理，就会变成 21 道互不相干的
	// 独立大题，而且没有任何提示（这是导入链路里最隐蔽的一种数据损坏）。
	depths, parents := draftDepths(req.Drafts)
	order := make([]int, len(req.Drafts))
	for i := range order {
		order[i] = i
	}
	// SliceStable：同层之间保持用户审核时的原始顺序，列表才对得上。
	sort.SliceStable(order, func(a, b int) bool { return depths[order[a]] < depths[order[b]] })

	// createdIDOf[i] 为 0 表示该草稿没有入库；失败原因记在 failReasonOf 里，
	// 子题据此判断"母题没了"，并把同样的原因继续传给自己的子题。
	createdIDOf := make([]uint, len(req.Drafts))
	failReasonOf := make([]string, len(req.Drafts))

	for _, i := range order {
		draft := &req.Drafts[i]

		fail := func(f schema.CommitFailure) {
			failures = append(failures, f)
			failReasonOf[i] = f.Error
		}

		if depths[i] >= maxDraftDepth {
			// 成环（a→b→a）意味着没有合法的落库顺序，只能整串拒绝。
			fail(failureOf(i, draft, apperr.New(apperr.CodeInvalidParam,
				"母子题结构无法解析（母题引用成环）")))
			continue
		}

		var parentID *uint
		if p := parents[i]; p >= 0 {
			if failReasonOf[p] != "" {
				// 母题没入库就跳过它的小问：否则子题会挂在空父节点上，
				// 用户既看不到它属于谁，也没法连着母题一起删。
				fail(failureOf(i, draft, apperr.New(apperr.CodeMissingRef,
					"母题未能入库，小问已一并跳过").
					WithDetail("母题 temp_id=%q 未入库：%s",
						draft.ParentTempID, failReasonOf[p])))
				continue
			}
			if pid := createdIDOf[p]; pid > 0 {
				parentID = &pid
			}
		}

		// 名称解析：草稿里的知识点/标签名称，能在本学科找到就关联，
		// 找不到就记下来提示用户，**不自动创建**。
		// 自动创建会迅速积累大量近义标签（"函数单调性" / "单调性" / "函数的单调性"），
		// 事后清理比当初手工选一次麻烦得多。
		kpIDs, tagIDs, unresolved := s.resolveNames(ctx, subjectID, draft.KnowledgePoints, draft.Tags)
		for _, n := range unresolved {
			unresolvedNames[n] = true
		}

		createReq, err := s.draftToCreateRequest(draft, subjectID, req, parentID)
		if err != nil {
			fail(failureOf(i, draft, err))
			continue
		}
		createReq.KnowledgePointIDs = mergeIDs(kpIDs, req.DefaultKnowledgePointIDs)
		createReq.TagIDs = mergeIDs(tagIDs, req.DefaultTagIDs)

		created, err := s.questions.Create(ctx, user, createReq)
		if err != nil {
			fail(failureOf(i, draft, err))
			continue
		}
		createdIDOf[i] = created.ID
	}

	// 响应里的 id 仍按请求里的原始顺序给出：前端按草稿下标回填界面，
	// 顺序变了会让用户对不上号（落库顺序与响应顺序是两件事）。
	createdIDs := make([]uint, 0, len(req.Drafts))
	for i := range req.Drafts {
		if createdIDOf[i] > 0 {
			createdIDs = append(createdIDs, createdIDOf[i])
		}
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

// maxDraftDepth 母子层级深度的上限。
//
// 正常试卷只有"母题 → 小问"两层，但模型偶尔会写出 a→b→a 这种环。
// 递归求深度时用它兜底：超过上限即判定为成环，宁可整串拒绝也不能无限递归。
const maxDraftDepth = 32

// draftDepths 计算每个草稿的母子层级深度，并给出它在本批次内的母题下标。
//
// 返回值：
//   - depths[i]：0 表示母题（无 parent_temp_id），子题 = 母题深度 + 1；
//     成环时返回 >= maxDraftDepth。
//   - parents[i]：母题在 drafts 里的下标；-1 表示没有可解析的母题
//     （parent_temp_id 为空、指向批次外的 temp_id、自引用或重复 temp_id）。
//
// 只用迭代 + 记忆化求深度，不做递归：链再长也不会把栈压爆。
func draftDepths(drafts []importing.Draft) (depths []int, parents []int) {
	n := len(drafts)
	depths = make([]int, n)
	parents = make([]int, n)
	for i := range parents {
		parents[i] = -1
	}

	// temp_id → 下标。重复的 temp_id 只认第一次出现的：
	// 模型给两道题编了同一个临时 id 时，无法判断谁才是母题，
	// 猜错会把一道普通题挂到另一道题下面。
	byTempID := make(map[string]int, n)
	for i := range drafts {
		tid := strings.TrimSpace(drafts[i].TempID)
		if tid == "" {
			continue
		}
		if _, dup := byTempID[tid]; dup {
			continue
		}
		byTempID[tid] = i
	}
	for i := range drafts {
		pid := strings.TrimSpace(drafts[i].ParentTempID)
		if pid == "" || pid == strings.TrimSpace(drafts[i].TempID) {
			continue
		}
		if p, ok := byTempID[pid]; ok {
			parents[i] = p
		}
	}

	memo := make([]int, n)
	for i := range memo {
		memo[i] = -1 // -1 = 尚未计算
	}
	var depthOf func(i int, path map[int]bool) int
	depthOf = func(i int, path map[int]bool) int {
		if memo[i] >= 0 {
			return memo[i]
		}
		p := parents[i]
		if p < 0 {
			memo[i] = 0
			return 0
		}
		if path[i] {
			// 回到自己身上：这条链成环，没有合法的落库顺序。
			memo[i] = maxDraftDepth
			return maxDraftDepth
		}
		path[i] = true
		d := depthOf(p, path) + 1
		delete(path, i)
		if d > maxDraftDepth {
			d = maxDraftDepth
		}
		memo[i] = d
		return d
	}
	for i := range drafts {
		depths[i] = depthOf(i, map[int]bool{})
	}
	return depths, parents
}

// draftToCreateRequest 把草稿转换为建题请求。
//
// parentID 为 nil 表示独立题；非 nil 时由 Commit 在母题入库后回填真实主键
// （模型给的 temp_id 不是数据库主键，不能直接写进 parent_id）。
func (s *ImportService) draftToCreateRequest(draft *importing.Draft, subjectID uint, req *schema.CommitRequest, parentID *uint) (*schema.QuestionCreateRequest, error) {
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
		ParentID:     parentID,
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
