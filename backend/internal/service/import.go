package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NanamiKyoka/questionmind/internal/ai"
	"github.com/NanamiKyoka/questionmind/internal/importing"
	"github.com/NanamiKyoka/questionmind/internal/infra/storage"
	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
	"github.com/NanamiKyoka/questionmind/internal/repository"
	"github.com/NanamiKyoka/questionmind/internal/schema"
	"github.com/NanamiKyoka/questionmind/internal/visionprep"
)

// extractTimeout 单次抽取的总预算。
//
// 这个预算是**实测**出来的，不是估的。2026 上海数学卷（3 页、21 道题、
// 仅录题模式、stealth/space-bunny-alpha）切 4 块并发跑，墙钟 336 秒。
// 原来的 3 分钟会在 180 秒处把 3 块一起掐掉，只剩 1 块的结果——
// 用户看到的是"识别成功 4 道题"，而卷子其实有 21 道，缺的 17 道没有任何提示。
//
// 注意它是**整次抽取**的预算，4 块并发共用，所以单块的实际可用时间只有它的
// 一部分。这也是为什么这里必须给得比"一块要多久"宽：按单块耗时设置上限，
// 并发几块就必然有几块在最后时刻被砍掉。
//
// 仍然保留一个上限：模型卡住时请求不能无限挂着，而超时的结果必须如实报告
// （缺失的段落会进 warnings），而不是假装整卷都读到了。
const extractTimeout = 20 * time.Minute

// ImportService 智能录题用例。
type ImportService struct {
	repos     *Repos
	aiConfig  *AIConfigService
	questions *QuestionService
	// vectors 可选：为草稿提供知识点语义匹配建议。
	vectors *VectorService
	// storage 可选：删除导入任务时清理落盘的原始文件。
	//
	// 没有它时同步导入照常工作（只是没有落盘文件可清），
	// 但队列导入不可用：worker 必须能从磁盘读回原始内容。
	storage *storage.Local
	// worker 可选：后台导入队列。没有它时 EnqueueFile 仍可建任务，
	// 但任务会一直停在 pending——界面会如实显示 worker 未启动。
	worker *ImportWorker
	// uploadDir 存放队列任务用到的原始文件。
	//
	// 刻意**不放在媒体目录里**：媒体目录是通过 /media 对外提供静态服务的，
	// 而原始试卷常常是尚未公开的教研材料，把它放进去等于顺手发布。
	// 它用的是 uploads 目录——只在本机，不对外。
	uploadDir string
	// papers 用于整卷导入：入库之后按大纲建一份可编辑的试卷。
	//
	// 单列一个字段而不是复用 questions：两条路径的生命周期不同
	// （题目逐题校验后写入，试卷是整棵树一次替换），混在一起会让
	// 构造函数的参数列表长到看不出依赖。
	papers *PaperService
}

func NewImportService(repos *Repos, aiConfig *AIConfigService, questions *QuestionService) *ImportService {
	return &ImportService{repos: repos, aiConfig: aiConfig, questions: questions}
}

// WithVectors 注入向量服务。未注入时跳过知识点语义匹配。
func (s *ImportService) WithVectors(v *VectorService) *ImportService {
	s.vectors = v
	return s
}

// WithStorage 注入媒体存储。未注入时删除任务不会清理磁盘文件。
func (s *ImportService) WithStorage(st *storage.Local) *ImportService {
	s.storage = st
	return s
}

// WithUploadDir 指定原始上传文件的落盘目录。
//
// 不设时队列导入不可用（会明确报错，而不是把文件塞进别处）——
// 静默换个位置存用户材料，比直接拒绝更糟。
func (s *ImportService) WithUploadDir(dir string) *ImportService {
	s.uploadDir = dir
	return s
}

// WithPapers 注入试卷服务，用于整卷导入。
func (s *ImportService) WithPapers(p *PaperService) *ImportService {
	s.papers = p
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
	// 页图（PDF 渲染出的题目页）与答案页一样都要求视觉模型，
	// 所以"有没有图"这件事要把两者都算进去。
	if content == "" && len(req.Images) == 0 && len(req.PageImages) == 0 && len(req.AssetImages) == 0 {
		return nil, apperr.New(apperr.CodeInvalidParam, "请提供待识别的文本或图片")
	}
	if err := validateImages(req.Images); err != nil {
		return nil, err
	}
	if err := validateImages(req.PageImages); err != nil {
		return nil, err
	}
	if err := validateImages(req.AssetImages); err != nil {
		return nil, err
	}

	// 抽取方式：规则解析完全不走模型。
	//
	// 它存在的意义不是"更省钱"，而是"没配模型也能录题"——
	// 未配置任何 AI 时，模型路径会直接失败，而带标签的电子稿本来
	// 就不需要模型来看。
	method, ok := importing.ParseMethod(req.Method)
	if !ok {
		return nil, apperr.New(apperr.CodeInvalidParam, "不支持的抽取方式").
			WithDetail("method=%s，可选 ai（调用模型）或 template（规则解析）", req.Method)
	}
	if method == importing.MethodTemplate && (len(req.Images) > 0 || len(req.PageImages) > 0) {
		return nil, apperr.New(apperr.CodeInvalidParam,
			"规则解析只处理文本，请改用 ai 方式提交图片或 PDF")
	}

	// 解析当前配置的模型。带图时必须用视觉模型：
	// 把图片塞给纯文本模型，供应商多半直接报错或者"看不见"地胡编一通，
	// 用户拿到的是一份看似正常、实则完全臆造的题目——比明确失败危险得多。
	var resolved *ResolvedModel
	if method == importing.MethodAI {
		modelType := model.AIModelText
		if len(req.Images) > 0 || len(req.PageImages) > 0 || len(req.AssetImages) > 0 {
			modelType = model.AIModelVision
		}
		r, err := s.aiConfig.Resolve(ctx, modelType)
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
		resolved = r
	}

	// 把已有知识点与标签喂给模型，让它优先复用而不是新造名字。
	// 规则解析用不到它们：它只按字面匹配已有名称，不生成新名字。
	var kpNames, tagNames []string
	if method == importing.MethodAI {
		kpNames, err = s.repos.KnowledgePoints.NamesBySubject(ctx, subjectID)
		if err != nil {
			return nil, apperr.ErrInternal.WithCause(err)
		}
		tagNames, err = s.repos.Tags.NamesBySubject(ctx, subjectID)
		if err != nil {
			return nil, apperr.ErrInternal.WithCause(err)
		}
	}

	promptOpts := importing.PromptOptions{
		Mode: mode,
		// 带上学科：提示词可以按学科覆盖，数学与语文的抽取要求本就不同。
		SubjectID:               &subjectID,
		SubjectName:             subject.Name,
		ExistingKnowledgePoints: kpNames,
		ExistingTags:            tagNames,
		// 只有用户上传的 Images 才是答案页。PDF 渲染出的页图是题目本身，
		// 混进这个计数会让提示词去"转写"题面，于是题目连同原答案一起被抽走。
		AnswerImages: len(req.Images),
		// 页图是题目页：告诉模型这是什么，否则它可能把 N 张图当成 N 道题。
		PageImages:  len(req.PageImages),
		AssetImages: len(req.AssetImages),
	}

	callCtx, cancel := context.WithTimeout(ctx, extractTimeout)
	defer cancel()

	// 任务先落库，再干活。
	//
	// 抽取要跑 30 秒到 3 分钟。如果只在跑完之后补一行记录，
	// 等待期间用户在历史页看到的是一片空白，进程被重启更是彻底无痕——
	// 用户会问"我刚才那次导入到底算不算数"。
	// 先建行 → processing → 终态，也让 pending/processing 两个状态第一次真正可达。
	task := &model.ImportTask{
		SubjectID:        &subjectID,
		UserID:           user.ID,
		Source:           model.ImportSourceAIAssist,
		Status:           model.ImportStatusPending,
		FileType:         fileTypeOf(req, mode),
		Mode:             string(mode),
		OriginalFilename: extractSourceLabel(req, mode),
	}
	if err := s.repos.ImportTasks.Create(ctx, task); err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	task.MarkProcessing()
	if err := s.saveTaskState(ctx, task); err != nil {
		return nil, err
	}

	started := time.Now()
	var (
		drafts      []importing.Draft
		modelLabel  string
		docWarnings []string
		outline     []importing.Outline
		sourceFiles = []string{task.OriginalFilename}
	)

	if method == importing.MethodTemplate {
		// 规则解析：确定性的，毫秒级返回，不消耗任何额度。
		// 它只按标签逐字取值，本来就一步也不求解，所以「仅录题」在这里
		// 与「完整抽取」没有区别——前端也不让用户组合出这个选择。
		// 走 Full 版本是为了拿到版面大纲——整卷导入要靠它还原卷面。
		drafts, outline, docWarnings = importing.ParseTemplateFull(content, task.OriginalFilename)
		modelLabel = "规则解析（未调用模型）"
	} else {
		// 页图与答案页分开传：前者按页切批，后者对整份卷子都要带。
		extracted, chunkWarnings, err := s.extractWithModel(
			callCtx, resolved, content, req.PageImages, req.PageTexts, req.Images, req.AssetImages, promptOpts)
		elapsed := time.Since(started)

		// 抽取失败也要留痕：用户会问"为什么没结果"。
		if err != nil {
			task.MarkFailed(humanizeAIError(err))
			// 留痕失败不应盖掉真正的失败原因——用户需要知道的是模型为什么出错。
			if saveErr := s.saveTaskState(ctx, task); saveErr != nil {
				slog.Warn("写入导入任务失败状态出错", "task_id", task.ID, "error", saveErr)
			}
			return nil, apperr.New(apperr.CodeAIUnavailable, humanizeAIError(err)).WithCause(err)
		}
		drafts = importing.Normalize(extracted, mode)
		modelLabel = resolved.Display
		// 模型只吐题目、不吐版面。这里给一份"只有题目引用"的大纲：
		// 整卷导入仍然可用，只是没有标题与说明文字——
		// 凭空造出原文没有的标题比没有标题糟糕得多。
		outline = importing.OutlineForDrafts(len(drafts))
		task.TotalCount = len(drafts)
		task.MarkCompleted()
		res := &schema.ExtractResponse{
			TaskID:       task.ID,
			Drafts:       drafts,
			Total:        len(drafts),
			WarningCount: countDraftWarnings(drafts),
			Model:        modelLabel,
			Method:       string(method),
			SourceFiles:  sourceFiles,
			Outline:      outline,
			Warnings:     chunkWarnings,
			// 任务里存 elapsed 不合适，这里只用于响应。
			ElapsedMs: elapsed.Milliseconds(),
		}
		// 草稿必须落库再返回：只放在响应里的话，用户刷新一次页面
		// 就等于把这几分钟等出来的题全丢了（见 saveExtractionResult）。
		if err := s.saveExtractionResult(ctx, task, res); err != nil {
			return nil, err
		}
		return res, nil
	}

	// ---- 规则解析的收尾 ----

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
	task.MarkCompleted()

	res := &schema.ExtractResponse{
		TaskID:       task.ID,
		Drafts:       drafts,
		Total:        len(drafts),
		WarningCount: countDraftWarnings(drafts),
		Model:        modelLabel,
		Method:       string(method),
		SourceFiles:  sourceFiles,
		Outline:      outline,
		Warnings:     docWarnings,
		ElapsedMs:    time.Since(started).Milliseconds(),
	}
	if err := s.saveExtractionResult(ctx, task, res); err != nil {
		return nil, err
	}
	return res, nil
}

// countDraftWarnings 统计带提示的草稿数，两条抽取路径共用一套口径。
func countDraftWarnings(drafts []importing.Draft) int {
	n := 0
	for i := range drafts {
		if len(drafts[i].Warnings) > 0 {
			n++
		}
	}
	return n
}

// Upload 从上传的文档文件中抽取题目。
//
// 与 Extract 的区别只在于"文本从哪来"：这里由摄取层把
// .md / .txt / .docx / .pdf / .zip 变成文本，之后完全走 Extract 的同一条管线。
//
// answerImages 是可选的答案页/解析页图片。这不是可有可无的附赠：
// 卷子本身通常**不带答案**，而让模型把二十道解答题一道道算出来，
// 光输出就���三万 token，几分钟起步且极易撞上供应商的超时上限。
// 老师手上有答案页时，把它一并发过去，模型只需**转写**，快一个数量级。
//
// 刻意**一次只收一个文件**：抽取是同步的，一个包塞十个 Word 会让
// 一个 HTTP 请求跑十几分钟，超时之后用户既不知道跑到哪了，
// 也无法中途叫停。多文件排队是后台任务的事，不该混在这里假装支持。
func (s *ImportService) Upload(ctx context.Context, user *model.User, subjectID uint, filename string, data []byte, method, mode string, answerImages []string) (*schema.ExtractResponse, error) {
	name := sanitizeUploadName(filename)
	if name == "" {
		return nil, apperr.New(apperr.CodeInvalidParam, "缺少文件名")
	}
	if !importing.UploadSupported(name) {
		return nil, apperr.New(apperr.CodeInvalidParam,
			"暂不支持这种文件").WithDetail("filename=%s；目前支持 %s", name, importing.UploadExtHint())
	}

	// 权限先判：不该让无权用户靠"文件能不能解析"探测系统能力。
	if err := authorize(user, model.PermEditQuestion, subjectID); err != nil {
		return nil, err
	}

	res, err := importing.Ingest(ctx, data, name, s.imageSaver())
	if err != nil {
		return nil, apperr.New(apperr.CodeInvalidParam, humanizeIngestError(err)).WithCause(err)
	}

	source := res.Files
	if len(source) == 0 {
		source = []string{name}
	}

	// PDF 被渲染成页图时没有 Docs：内容在图里，由视觉模型去读。
	// 这时不能再走下面那段"把多份文档拼成一段文本"的逻辑——
	// res.Docs 是空的，取 [0] 会直接 panic。
	if len(res.PageImages) > 0 {
		resp, err := s.Extract(ctx, user, subjectID, &schema.ExtractRequest{
			PageImages:  res.PageImages,
			PageTexts:   res.PageTexts,
			AssetImages: res.AssetImages,
			Images:      answerImages,
			Method:      method,
			Mode:        mode,
			Filename:    sourceLabel(name, len(source)),
		})
		if err != nil {
			return nil, err
		}
		resp.SourceFiles = source
		if len(res.Warnings) > 0 {
			resp.Warnings = append(resp.Warnings, res.Warnings...)
		}
		return resp, nil
	}

	// 多份文档必须逐份解析：文末答案区是按题号回填的，两份都用 1/2/3 编号时，
	// 合成一段再解析会把第一份的答案贴到第二份的题上——而且表面上完全看不出来。
	// 模型路径是一次调用返回一整份草稿，天然没有这个问题，所以那里仍然合并。
	if len(res.Docs) > 1 && isTemplateMethod(method) {
		return s.extractDocsIndependently(ctx, user, subjectID, method, mode, name, source, res)
	}

	content := res.Docs[0].Text
	if len(res.Docs) > 1 {
		parts := make([]string, 0, len(res.Docs))
		for _, d := range res.Docs {
			parts = append(parts, d.Text)
		}
		content = strings.Join(parts, "\n\n")
	}

	resp, err := s.Extract(ctx, user, subjectID, &schema.ExtractRequest{
		Content:     content,
		AssetImages: res.AssetImages,
		Images:      answerImages,
		Method:      method,
		Mode:        mode,
		Filename:    sourceLabel(name, len(source)),
	})
	if err != nil {
		return nil, err
	}
	resp.SourceFiles = source
	if len(res.Warnings) > 0 {
		resp.Warnings = append(resp.Warnings, res.Warnings...)
	}
	return resp, nil
}

// extractDocsIndependently 逐份文档各跑一次抽取，再把草稿汇总成一次响应。
//
// 每份文档各留一条任务记录：出错时用户能看出是包里哪一份没解析出来，
// 而不是面对一个"共 8 题、2 题失败"的笼统结果。
func (s *ImportService) extractDocsIndependently(
	ctx context.Context, user *model.User, subjectID uint,
	method, mode, archiveName string, source []string, res *importing.IngestResult,
) (*schema.ExtractResponse, error) {
	var (
		all     []importing.Draft
		warns   []string
		lastID  uint
		totalMs int64
	)
	for _, doc := range res.Docs {
		sub, err := s.Extract(ctx, user, subjectID, &schema.ExtractRequest{
			Content: doc.Text, Method: method, Mode: mode, Filename: doc.Name,
		})
		if err != nil {
			warns = append(warns, fmt.Sprintf("%s：%v", doc.Name, err))
			continue
		}
		all = append(all, sub.Drafts...)
		warns = append(warns, sub.Warnings...)
		lastID = sub.TaskID
		totalMs += sub.ElapsedMs
	}
	if len(all) == 0 {
		return nil, apperr.New(apperr.CodeUnprocessable,
			"压缩包里的文档都没有解析出题目").WithDetail("%s：%v", archiveName, warns)
	}

	n := 0
	for i := range all {
		if len(all[i].Warnings) > 0 {
			n++
		}
	}
	return &schema.ExtractResponse{
		TaskID:       lastID,
		Drafts:       all,
		Total:        len(all),
		WarningCount: n,
		Model:        "规则解析（未调用模型）",
		Method:       string(importing.MethodTemplate),
		SourceFiles:  source,
		Warnings:     append(res.Warnings, warns...),
		ElapsedMs:    totalMs,
	}, nil
}

// isTemplateMethod 判断请求走的是否是规则解析。
func isTemplateMethod(method string) bool {
	m, ok := importing.ParseMethod(method)
	return ok && m == importing.MethodTemplate
}

// sourceLabel 给一次导入起一个人类可读的任务标题。
func sourceLabel(name string, docCount int) string {
	if docCount > 1 {
		return fmt.Sprintf("%s（%d 个文档）", name, docCount)
	}
	return name
}

// imageSaver 把摄取到的内嵌图片交给存储层。
func (s *ImportService) imageSaver() importing.ImageSaver {
	if s.storage == nil {
		return nil
	}
	return storageImageSaver{s.storage}
}

// storageImageSaver 适配 storage.Local 到 importing.ImageSaver。
type storageImageSaver struct{ st *storage.Local }

func (a storageImageSaver) Save(data []byte, name string) (string, error) {
	saved, err := a.st.Save(data, name)
	if err != nil {
		return "", err
	}
	// Saved.URL 已经是可直接写进文档的公开地址（/media/… 前缀），
	// 不需要再拼一次，否则会出现前缀套前缀。
	return saved.URL, nil
}

// sanitizeUploadName 只取路径末段并剥掉控制字符，
// 让 "../../etc/passwd" 这类名字既不会落盘也不会进历史记录。
func sanitizeUploadName(name string) string {
	name = strings.TrimSpace(name)
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
}

// humanizeIngestError 把摄取层的错误翻译成用户看得懂的话。
func humanizeIngestError(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "压缩包"):
		return msg
	case strings.Contains(msg, "文字层"):
		// PDF 抽取层已经把"该改走截图识别"写进消息里了，原样透出即可：
		// 再包一层只会把"下一步该做什么"这条关键信息冲淡。
		return msg
	case strings.Contains(msg, "Word"):
		return "无法读取这个 Word 文档，请确认它是 .docx 而不是 .doc 等旧格式"
	case strings.Contains(msg, "不支持"):
		return msg
	}
	return "文件解析失败：" + msg
}

func commitReplay(task *model.ImportTask) (*schema.CommitResponse, error) {
	if task.Status != model.ImportStatusCompleted && task.Status != model.ImportStatusFailed {
		return nil, apperr.New(apperr.CodeConflict, "相同请求正在处理中，请稍后重试")
	}
	var resp schema.CommitResponse
	if err := task.Result.Into(&resp); err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	if resp.TaskID == 0 {
		resp.TaskID = task.ID
	}
	return &resp, nil
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
	idempotencyKey := strings.TrimSpace(req.IdempotencyKey)
	if idempotencyKey != "" {
		existing, err := s.repos.ImportTasks.GetByIdempotencyKey(ctx, user.ID, idempotencyKey)
		if err != nil {
			return nil, apperr.ErrInternal.WithCause(err)
		}
		if existing != nil {
			return commitReplay(existing)
		}
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

	// 任务同样先落库。
	//
	// 不只是为了"看得见"：题目入库后要回填 questions.import_task_id 做溯源，
	// 而那道任务的 id 必须在建题**之前**就存在。
	// 以前任务行是入库结束后才补的，于是题目永远挂不回它来自的那次导入。
	var keyPtr *string
	if idempotencyKey != "" {
		keyPtr = &idempotencyKey
	}
	task := &model.ImportTask{
		SubjectID:        &subjectID,
		UserID:           user.ID,
		Source:           model.ImportSourceAIAssist,
		Status:           model.ImportStatusPending,
		FileType:         "text",
		Mode:             "commit",
		TotalCount:       len(req.Drafts),
		OriginalFilename: commitSourceLabel(req),
		IdempotencyKey:   keyPtr,
	}
	if err := s.repos.ImportTasks.Create(ctx, task); err != nil {
		if idempotencyKey != "" {
			if existing, replayErr := s.repos.ImportTasks.GetByIdempotencyKey(ctx, user.ID, idempotencyKey); replayErr == nil && existing != nil {
				return commitReplay(existing)
			}
		}
		// 还没建任何题目，此刻失败就是整体失败，不需要"假装成功"。
		return nil, apperr.ErrInternal.WithCause(err)
	}
	resp.TaskID = task.ID
	task.MarkProcessing()
	if err := s.saveTaskState(ctx, task); err != nil {
		return nil, err
	}

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
		// 溯源：请求里带上任务 id，真正写列的动作在下面统一回填。
		createReq.ImportTaskID = &task.ID

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

	// 溯源回填：一次 UPDATE 把整批题目挂到任务上。
	//
	// 为什么放在循环之后而不是建题时写：题目要先有主键才能被更新，
	// 而循环里每题一次 UPDATE 会让 200 道题的导入多出 200 次数据库往返。
	//
	// 失败只记日志不中断：题目此时**已经入库**，返回一个错误会让用户
	// 以为"这批题没进去"从而重复导入一遍，那比缺少一个溯源标记糟糕得多。
	if err := s.repos.Questions.SetImportTask(ctx, createdIDs, task.ID); err != nil {
		slog.Warn("回填题目溯源失败",
			"task_id", task.ID, "count", len(createdIDs), "error", err)
	}

	// 整卷导入：题目入库之后，按大纲建一份可编辑的试卷。
	//
	// 放在最后、且失败不影响题目：题目此刻已经落库并回传了 created_ids，
	// 丢一份排版远比丢几十道题轻。所以失败只写进响应，不返回 error。
	if req.SaveAsPaper {
		paperID, err := s.paperFromImport(ctx, user, subjectID, task.ID, req, createdIDOf)
		if err != nil {
			resp.PaperError = err.Error()
			slog.Warn("整卷导入建卷失败", "task_id", task.ID, "error", err)
		} else {
			resp.PaperID = paperID
		}
	}

	// 记录本次入库结果，同时供幂等重试直接回放。
	task.Result = model.MustJSONOf(resp)
	task.SavedCount = len(createdIDs)
	task.FailedCount = len(failures)
	if len(failures) > 0 && len(createdIDs) == 0 {
		task.MarkFailed("全部题目入库失败")
	} else {
		task.MarkCompleted()
	}
	if err := s.saveTaskState(ctx, task); err != nil {
		// 入库结果已经产生，审计记录失败不应让用户以为题目没进去。
		slog.Warn("写入导入任务完成状态出错", "task_id", task.ID, "error", err)
	}

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
//
// **可见范围按人隔离**：默认只返回当前用户自己的导入记录。
// 导入记录里带的是"谁在什么时候把哪份卷子导进来了"，
// 而原实现只按学科过滤，于是任何能读该学科的成员都能看到同事的导入历史
// （包括 filename 与失败原因）。超级管理员保持"能看到全部"，与全站其它地方的
// 超级管理员语义一致；只有超级管理员才能用 userID 去筛别人的记录。
//
// 学科维度的过滤依然保留：它是外层边界，userID 只能在这个边界内再收窄。
func (s *ImportService) ListTasks(ctx context.Context, user *model.User, subjectID uint, status model.ImportTaskStatus, userID uint, limit, offset int) ([]schema.ImportTaskResponse, int64, error) {
	if err := authorizeRead(user, subjectID); err != nil {
		return nil, 0, err
	}

	filter := repository.ImportTaskListFilter{
		SubjectID: subjectID,
		Status:    status,
	}
	switch {
	case user.IsSuperuser:
		// 管理员不传 userID 就是"看全部"，传了就是"只看这个人"。
		if userID > 0 {
			filter.UserID = userID
		}
	default:
		filter.UserID = user.ID
	}

	list, total, err := s.repos.ImportTasks.List(ctx, filter, limit, offset)
	if err != nil {
		return nil, 0, apperr.ErrInternal.WithCause(err)
	}

	out := make([]schema.ImportTaskResponse, 0, len(list))
	for i := range list {
		out = append(out, schema.NewImportTaskResponse(&list[i]))
	}
	return out, total, nil
}

// activeStatuses 可以被"放弃"的前置状态。
// 与 model.ImportTask.IsActive 保持一致，放在一起是为了让"允许的状态集合"只有一个出处。
var activeStatuses = []model.ImportTaskStatus{
	model.ImportStatusPending,
	model.ImportStatusProcessing,
}

// CancelTask 放弃一个导入任务。
//
// ⚠️ 语义边界（当前没有后台 worker）：
// 抽取是**同步**执行的——用户点下"开始识别"之后，那次 HTTP 请求在服务端
// 一路跑到模型返回为止，服务端没有可以抢占的执行体。
// 因此"取消"能做到的是：**把这条任务记录标记为已放弃**，
// 让它离开队列、将来也不会被 worker 认领；它**不会**中断任何正在进行的抽取，
// 更不会退还已经花掉的模型调用。
//
// 真正的中断需要两样东西，都还没有：后台 worker（把执行体从请求里搬出去）
// 与协作式取消（在抽取循环里检查一个标志位）。
// 在那之前，与其假装能中断，不如把话说明白。
func (s *ImportService) CancelTask(ctx context.Context, user *model.User, subjectID, taskID uint) (*schema.ImportTaskResponse, error) {
	if err := authorize(user, model.PermEditQuestion, subjectID); err != nil {
		return nil, err
	}
	task, err := s.loadTask(ctx, subjectID, taskID)
	if err != nil {
		return nil, err
	}
	// 放弃/重试/删除都会动到别人导的记录（连同上传的源文件），
	// 与读取端同一条可见性规则：本人或管理员。
	if err := s.authorizeTaskRead(user, task); err != nil {
		return nil, err
	}
	if !task.IsActive() {
		return nil, apperr.New(apperr.CodeConflict, "任务已经结束，无法取消").
			WithDetail("当前状态：%s", task.Status)
	}

	task.MarkCancelled("用户已放弃该任务")
	// 用 CAS 写入：并发的第二次取消必须失败，否则第一次的结果会被静默覆盖。
	ok, err := s.repos.ImportTasks.SwapStatus(ctx, task.ID, activeStatuses, taskStateFields(task))
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	if !ok {
		return nil, apperr.New(apperr.CodeConflict, "任务状态已被其他操作改变，请刷新后重试")
	}
	return ptrTo(schema.NewImportTaskResponse(task)), nil
}

// RetryTask 把失败、已放弃、或"已完成但一题都没抽出来"的任务重新放回队列。
//
// 复位成 pending 之后由进程内的 worker 认领并重跑，用户不用重新发起：
// 原文仍在任务里。这是"取消"的反面——取消不中断正在跑的抽取，
// 只是把它从队列里拿掉。
func (s *ImportService) RetryTask(ctx context.Context, user *model.User, subjectID, taskID uint) (*schema.ImportTaskResponse, error) {
	if err := authorize(user, model.PermEditQuestion, subjectID); err != nil {
		return nil, err
	}
	task, err := s.loadTask(ctx, subjectID, taskID)
	if err != nil {
		return nil, err
	}
	if err := s.authorizeTaskRead(user, task); err != nil {
		return nil, err
	}

	from := []model.ImportTaskStatus{
		model.ImportStatusFailed,
		model.ImportStatusCancelled,
	}
	retryable := task.Retryable(model.MaxImportAttempt)
	// "已完成但一题都没抽出来"也是允许重试的，但它不是终态机里的错误态，
	// 因此单独放宽前置条件；上限依旧由 Retryable 判定。
	if task.Status == model.ImportStatusCompleted && task.TotalCount == 0 {
		from = append(from, model.ImportStatusCompleted)
	}
	if !retryable {
		return nil, apperr.New(apperr.CodeConflict, "该任务当前状态不允许重试").
			WithDetail("状态=%s，已执行 %d 次（上限 %d 次）",
				task.Status, task.Attempt, model.MaxImportAttempt)
	}

	task.MarkRetried()
	ok, err := s.repos.ImportTasks.SwapStatus(ctx, task.ID, from, taskStateFields(task))
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	if !ok {
		return nil, apperr.New(apperr.CodeConflict, "任务状态已被其他操作改变，请刷新后重试")
	}
	return ptrTo(schema.NewImportTaskResponse(task)), nil
}

// DeleteTask 删除一条导入记录，并清理由它带来的落盘文件。
//
// 刻意**不**动题目：导入记录是一次操作的历史，题目是内容。
// 用户删记录通常是因为"这次导错了、不想看见"，而不是想把已经录好的题也抹掉；
// 真要删题有题库页的删除与回收站。删完之后题目上的 import_task_id 会指向
// 一个不存在的任务——这是有意的：它仍然记录着"这道题曾由第 N 次导入产生"。
func (s *ImportService) DeleteTask(ctx context.Context, user *model.User, subjectID, taskID uint) error {
	if err := authorize(user, model.PermEditQuestion, subjectID); err != nil {
		return err
	}
	task, err := s.loadTask(ctx, subjectID, taskID)
	if err != nil {
		return err
	}
	// 删除会连同上传的 .docx/.pdf 源文件一起清掉，不能让同学科的
	// 其他编辑者枚举任务 id 后把别人的记录连锅端。
	if err := s.authorizeTaskRead(user, task); err != nil {
		return err
	}

	// 先删记录再删文件：反过来的话，文件删不掉会留下"记录还在、文件没了"的
	// 半截状态，而磁盘上的残留文件是人工就能清掉的东西。
	if err := s.repos.ImportTasks.Delete(ctx, task.ID); err != nil {
		return apperr.ErrInternal.WithCause(err)
	}
	s.removeTaskFile(task)
	return nil
}

// QueueStatus 队列状态：各状态计数 + 活动任务的 FIFO 位置。
func (s *ImportService) QueueStatus(ctx context.Context, user *model.User, subjectID uint) (*schema.ImportQueueStatusResponse, error) {
	if err := authorizeRead(user, subjectID); err != nil {
		return nil, err
	}

	// 计数与列表共用同一个可见范围：否则"队列里有 3 个"却一个都列不出来，
	// 用户会以为是自己筛错了。
	filter := s.taskVisibility(user, subjectID)

	counts, err := s.repos.ImportTasks.StatusCounts(ctx, filter)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}
	active, err := s.repos.ImportTasks.ListActive(ctx, filter, queuePreviewLimit)
	if err != nil {
		return nil, apperr.ErrInternal.WithCause(err)
	}

	stats := schema.ImportTaskStatusCounts{
		Pending:    counts[model.ImportStatusPending],
		Processing: counts[model.ImportStatusProcessing],
		Completed:  counts[model.ImportStatusCompleted],
		Failed:     counts[model.ImportStatusFailed],
		Cancelled:  counts[model.ImportStatusCancelled],
		Total: counts[model.ImportStatusPending] + counts[model.ImportStatusProcessing] +
			counts[model.ImportStatusCompleted] + counts[model.ImportStatusFailed] +
			counts[model.ImportStatusCancelled],
	}

	// 处理中的排在最前面：它已经占用了执行位，
	// 让它显示成"队列第 3 位"会让人以为还得等两个任务。
	sort.SliceStable(active, func(a, b int) bool {
		return active[a].Status == model.ImportStatusProcessing &&
			active[b].Status != model.ImportStatusProcessing
	})

	items := make([]schema.ImportQueueItem, 0, len(active))
	for i := range active {
		items = append(items, schema.ImportQueueItem{
			TaskID:           active[i].ID,
			OriginalFilename: active[i].OriginalFilename,
			Status:           active[i].Status,
			StatusLabel:      importStatusLabel(active[i].Status),
			QueuePosition:    i + 1,
			Attempt:          active[i].Attempt,
			CreatedAt:        active[i].CreatedAt,
		})
	}

	// WorkerOnline 反映真实的 worker 状态。
	//
	// 以前这里恒为 false，界面上如实写着"排队中不会自动开始"——
	// 那时确实没有 worker。现在有了，就必须把它接上：否则界面会给一个
	// 真正在跑的任务显示"不会开始"，用户就不敢等，队列也就白做了。
	online := s.worker != nil && s.worker.Online()
	hint := "抽取是同步执行的，队列中的任务不会自动开始；需要重跑请回到智能录题页重新发起。"
	if online {
		hint = "队列已启动，任务会按提交顺序依次处理；处理中的任务可以随时放弃。"
	}

	return &schema.ImportQueueStatusResponse{
		Stats:        stats,
		ActiveTasks:  items,
		WorkerOnline: online,
		Hint:         hint,
	}, nil
}

// WithWorker 注入后台 worker。
//
// 构造与注入分开，是为了让 ImportService 在没有 worker 的场景下
// （单元测试、只想用同步导入的部署）依然完全可用。
func (s *ImportService) WithWorker(w *ImportWorker) *ImportService {
	s.worker = w
	return s
}

// TaskQuestions 列出某次导入产生的题目。
func (s *ImportService) TaskQuestions(ctx context.Context, user *model.User, subjectID, taskID uint, limit, offset int) ([]schema.QuestionBrief, int64, error) {
	if err := authorizeRead(user, subjectID); err != nil {
		return nil, 0, err
	}
	if _, err := s.loadTask(ctx, subjectID, taskID); err != nil {
		return nil, 0, err
	}

	list, total, err := s.repos.Questions.ListByImportTask(ctx, model.QuestionFilter{
		SubjectID:         subjectID,
		ViewerID:          user.ID,
		CanViewPrivateAll: user.Can(model.PermViewPrivateAny, subjectID),
	}, taskID, model.SortByCreatedAsc, limit, offset)
	if err != nil {
		return nil, 0, apperr.ErrInternal.WithCause(err)
	}
	if len(list) == 0 {
		return []schema.QuestionBrief{}, total, nil
	}

	// 复用题库列表的批量装载（标签/知识点/子题数各一条 SQL），
	// 否则这里会退化成每题三次查询的老写法。
	briefs, err := s.questions.toBriefs(ctx, list)
	if err != nil {
		return nil, 0, err
	}
	return briefs, total, nil
}

// queuePreviewLimit 队列面板最多预览多少个活动任务。
// 计数是全量的，预览是给人看的——队列真排到几百个时，界面也不该被撑爆。
const queuePreviewLimit = 20

// taskVisibility 导入任务列表/队列的可见范围：与 ListTasks 同一套规则。
func (s *ImportService) taskVisibility(user *model.User, subjectID uint) repository.ImportTaskListFilter {
	f := repository.ImportTaskListFilter{SubjectID: subjectID}
	if !user.IsSuperuser {
		f.UserID = user.ID
	}
	return f
}

// loadTask 读取任务并确认它属于该学科。
//
// 跨学科一律按"不存在"处理（404 而不是 403）：告诉调用者
// "这个 id 存在，但在另一个学科"等于提供了一台学科 id 探测器。
func (s *ImportService) loadTask(ctx context.Context, subjectID, taskID uint) (*model.ImportTask, error) {
	task, err := s.repos.ImportTasks.Get(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if task.SubjectID == nil || *task.SubjectID != subjectID {
		return nil, apperr.ErrNotFound.WithDetail("导入任务 %d 不存在", taskID)
	}
	return task, nil
}

// removeTaskFile 清理任务附带的落盘文件。
//
// 只在注入了存储时才做，且交给 storage 层去解析路径：
// 它会把解析结果限制在媒体目录内，因此即使 FilePath 里是
// "../../../etc/passwd" 也只会解析成媒体目录下的一个不存在的相对路径。
// 删不掉不是错误——磁盘上的残留文件由运维处理，不该让"删除记录"整体失败。
func (s *ImportService) removeTaskFile(task *model.ImportTask) {
	if s.storage == nil || strings.TrimSpace(task.FilePath) == "" {
		return
	}
	if err := s.storage.Remove(task.FilePath); err != nil {
		slog.Warn("删除导入任务附件失败", "task_id", task.ID, "error", err)
	}
}

// saveExtractionResult 收尾：把整份抽取结果（含草稿）连同状态一起写进任务记录。
//
// 为什么非存不可：抽取要跑几十秒到几分钟，而结果此前**只存在于那一个
// HTTP 响应里**。浏览器刷新、误点返回、关掉标签页，用户等出来的几十道题
// 就再没有任何副本——任务行上只剩一个 total_count，导入历史里点
// 「审核草稿」得到的是"该任务没有保存草稿"。于是"识别成功"与
// "什么题目都没有录入"可以同时成立，而且用户完全看不出问题出在哪一步。
//
// 排队任务（worker）一直这么存，这里补上同步路径，两条路的结果形状一致。
func (s *ImportService) saveExtractionResult(ctx context.Context, task *model.ImportTask, res *schema.ExtractResponse) error {
	raw, err := model.JSONOf(res)
	if err != nil {
		return apperr.ErrInternal.WithCause(err)
	}
	task.Result = raw
	return s.saveTaskState(ctx, task)
}

// saveTaskState 把任务的状态机字段与计数字段写回数据库。
//
// 集中在一处是为了避免"改了状态机却漏写某个字段"——
// 这类遗漏不会报错，只会让历史页显示出一条自相矛盾的记录
// （比如状态是已完成却没有结束时间）。
func (s *ImportService) saveTaskState(ctx context.Context, task *model.ImportTask) error {
	if err := s.repos.ImportTasks.UpdateFields(ctx, task.ID, taskStateFields(task)); err != nil {
		return apperr.ErrInternal.WithCause(err)
	}
	return nil
}

// taskStateFields 需要持久化的任务字段。
//
// 用 map 而不是 Save(struct)：GORM 的结构体更新会跳过零值，
// 于是"重试时清空 error_msg / finished_at"会被静默丢掉。
//
// result 也在这里：抽取的草稿与入库的逐条失败原因都存进它，
// 而这两样正是导入历史与"重新审核"要读的东西（见 saveExtractionResult）。
func taskStateFields(t *model.ImportTask) map[string]any {
	return map[string]any{
		"status":       t.Status,
		"attempt":      t.Attempt,
		"error_msg":    t.ErrorMsg,
		"started_at":   t.StartedAt,
		"finished_at":  t.FinishedAt,
		"total_count":  t.TotalCount,
		"saved_count":  t.SavedCount,
		"failed_count": t.FailedCount,
		"result":       t.Result,
	}
}

// importStatusLabel 状态的中文名，与 schema 中的标签保持一致。
func importStatusLabel(s model.ImportTaskStatus) string {
	return schema.ImportStatusLabel(s)
}

// ---- 模型抽取 ----

// pagesPerCall 一次调用最多送几页图。
//
// 这个数字来自实测：2026 全国 1 卷（3 页 21 题）在**完整抽取**（带答案与解析）
// 下要约 5 万输出 token，而 32K 上限装不下——finish_reason=length 且 JSON 残缺，
// 一道题都读不出来。每页约 1.7 万 token，于是"一次一页"最稳，两页就已经贴边。
//
// 取 1 而不是 2：宁可多一次往返，也不要让"刚好差一点"变成整批报废。
// 页面少的卷子（1~2 页）仍然只发一次调用，不受影响。
//
// 它只是起点而不是保证——题目密度无法预知，所以真正兜底的是
// callPages 里"截断就拆半重试"。
const pagesPerCall = 1

// extractWithModel 调模型抽题。
//
// 文本与页图走**两条不同的切分策略**，因为它们的瓶颈不是一回事：
//
//   - 文本按题数切（见 importing.SplitForExtraction）：一次调用的耗时几乎全在
//     输出上，而输出量由题数决定。
//   - 页图按页切：页与页是天然边界，一页就是一道完整的题组，
//     不像文本那样要担心把一道题劈成两半。
//
// 页图路径之前是**完全不切**的（"带图不切块"），那在只有一两页答案页时是对的；
// 但 PDF 渲染出的页图可能有几十页，一次全发必然撞上输出上限——
// 而输出被截断时 JSON 是残缺的，整份卷子的题一道都读不出来。
// 这是比"少几道题"严重得多的失败。
func (s *ImportService) extractWithModel(
	ctx context.Context,
	resolved *ResolvedModel,
	content string,
	pageImages, pageTexts, answerImages, assetImages []string,
	opts importing.PromptOptions,
) ([]ai.ExtractedQuestion, []string, error) {
	// 图片必须先变成模型**真正拿得到**的形态再发出去。
	// 上传接口给的是 /media/xxx.jpg 这种本服务自己的相对路径，
	// 远程模型没有任何办法去访问它——原样发过去等于没发。
	pageData, pageWarn, err := s.materializeImages(pageImages)
	if err != nil {
		return nil, nil, err
	}
	answerData, answerWarn, err := s.materializeImages(answerImages)
	if err != nil {
		return nil, nil, err
	}
	assetData, assetWarn, err := s.materializeImages(assetImages)
	if err != nil {
		return nil, nil, err
	}
	imgWarnings := append(append(append([]string(nil), pageWarn...), answerWarn...), assetWarn...)

	// 每次调用都带上答案页：它是对**整份卷子**的一次性说明，
	// 只给其中某几次调用等于告诉模型"这一段没有答案"。
	call := func(text string, imgs []string, pageCount, answerCount int) ([]ai.ExtractedQuestion, error) {
		opts := opts
		opts.PageImages = pageCount
		opts.AnswerImages = answerCount
		opts.AssetImages = len(assetData)
		return resolved.Provider.ExtractQuestions(ctx, ai.ExtractRequest{
			Model:  resolved.ModelName,
			Prompt: importing.BuildSystemPrompt(opts),
			// 提示词已经写进系统消息，schema 字段留着是空的。
			Content:   importing.BuildUserContent(text, opts),
			Images:    imgs,
			MaxTokens: resolved.MaxTokens,
		})
	}

	// ---- 页图路径：按页分批 ----
	if len(pageData) > 0 {
		return s.extractPageBatches(ctx, call, pageData, answerData, pageTexts, pagesPerCall, len(answerData), imgWarnings)
	}

	// ---- 文本路径：按题数切块 ----
	textImages := append(append([]string(nil), assetData...), answerData...)
	chunks := importing.SplitForExtractionMode(content, 0, opts.Mode)
	if len(chunks) <= 1 {
		text := content
		if len(chunks) == 1 {
			text = chunks[0].Text
		}
		out, err := call(text, textImages, 0, len(answerData))
		if len(chunks) == 1 {
			assignSourceNumbers(chunks[0], out)
		}
		return out, imgWarnings, err
	}

	results := make([][]ai.ExtractedQuestion, len(chunks))
	errs := make([]error, len(chunks))
	var wg sync.WaitGroup
	sem := make(chan struct{}, importing.ExtractionConcurrency)

	for i := range chunks {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				errs[i] = ctx.Err()
				return
			}
			defer func() { <-sem }()
			results[i], errs[i] = call(chunks[i].Text, textImages, 0, len(answerData))
			if errs[i] == nil {
				assignSourceNumbers(chunks[i], results[i])
			}
		}(i)
	}
	wg.Wait()

	// 全军覆没：照旧报错，用户至少知道该重试还是该换模型。
	var failed []int
	for i, e := range errs {
		if e != nil {
			failed = append(failed, i)
		}
	}
	if len(failed) == len(chunks) {
		return nil, nil, firstError(errs, failed)
	}

	var (
		merged   []ai.ExtractedQuestion
		warnings = imgWarnings
	)
	for i := range chunks {
		if errs[i] != nil {
			warnings = append(warnings, fmt.Sprintf(
				"第 %d 段（共 %d 段）识别失败，该段内容缺失：%s（%s）",
				i+1, len(chunks), chunks[i].Label, humanizeAIError(errs[i])))
			slog.Warn("导入分块失败", "chunk", i, "label", chunks[i].Label, "error", errs[i])
			continue
		}
		merged = append(merged, reidentify(len(merged), results[i])...)
	}
	return merged, warnings, nil
}

func assignSourceNumbers(chunk importing.Chunk, questions []ai.ExtractedQuestion) {
	assignSourceNumbersByRange(questions, chunk.QuestionFrom, chunk.QuestionTo)
}

func assignSourceNumbersByRange(questions []ai.ExtractedQuestion, from, to int) {
	if from <= 0 || to < from {
		return
	}
	rootIndexes := make([]int, 0, len(questions))
	for i := range questions {
		if questions[i].ParentTempID == "" {
			rootIndexes = append(rootIndexes, i)
		}
	}
	if len(rootIndexes) == 0 {
		return
	}
	for i, qIndex := range rootIndexes {
		n := from + i
		if n > to {
			n = to
		}
		questions[qIndex].SourceNumber = n
	}
}

var pageQuestionNumberPattern = regexp.MustCompile(`(?m)^\s*(\d{1,3})\s*[.、．]\s*\S`)

type questionRange struct {
	from int
	to   int
}

func pageQuestionRanges(pageTexts []string, pageCount int) []questionRange {
	if pageCount <= 0 {
		return nil
	}
	out := make([]questionRange, pageCount)
	for i := 0; i < pageCount && i < len(pageTexts); i++ {
		matches := pageQuestionNumberPattern.FindAllStringSubmatch(pageTexts[i], -1)
		for _, m := range matches {
			n, err := strconv.Atoi(m[1])
			if err != nil || n <= 0 {
				continue
			}
			if out[i].from == 0 {
				out[i].from = n
			}
			out[i].to = n
		}
	}
	return out
}

func (s *ImportService) attachPageFigures(pageData string, questions []ai.ExtractedQuestion) []string {
	if s.storage == nil || pageData == "" {
		return nil
	}
	var warnings []string
	for i := range questions {
		q := &questions[i]
		if q.ParentTempID != "" || len(q.FigureBox) < 4 {
			continue
		}
		raw, err := visionprep.CropJPEG(pageData, q.FigureBox)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("题图裁剪失败：%v", err))
			continue
		}
		saved, err := s.storage.Save(raw, "figure.jpg")
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("题图保存失败：%v", err))
			continue
		}
		q.FigureRefs = appendUnique(q.FigureRefs, saved.URL)
	}
	return warnings
}

func appendUnique(base []string, extra ...string) []string {
	seen := make(map[string]bool, len(base)+len(extra))
	out := make([]string, 0, len(base)+len(extra))
	for _, s := range append(base, extra...) {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func rangeForPages(ranges []questionRange, index, perCall int) questionRange {
	if perCall <= 0 {
		perCall = 1
	}
	var out questionRange
	start := index * perCall
	end := start + perCall
	if end > len(ranges) {
		end = len(ranges)
	}
	for i := start; i < end; i++ {
		if ranges[i].from > 0 && out.from == 0 {
			out.from = ranges[i].from
		}
		if ranges[i].to > 0 {
			out.to = ranges[i].to
		}
	}
	return out
}

// pageCall 一次页图调用的签名。
type pageCall func(text string, imgs []string, pageCount, answerCount int) ([]ai.ExtractedQuestion, error)

// extractPageBatches 把页图按页分批并发抽取，截断时自动对半拆。
//
// perCall 是一次送几页；生产上用 pagesPerCall。做成参数而不是读常量，
// 是因为"3 页会超限、拆成 1+2 就能过"这类行为只有把批大小调大才测得出来——
// 按生产值（1）跑，批次永远是单页，"会不会拆"这条分支根本走不到。
//
// 为什么并发而不是顺序：每批都要等模型读完那几页图（几十秒到几分钟），
// 顺序跑十页就是十倍等待。而这些调用彼此独立，没有任何共享状态。
//
// 为什么还要"截断时对半拆"：perCall 是按常见卷子定的经验值，
// 而题目密度是没法预知的——一份全是压轴大题的两页纸，两页一批照样会超。
// 与其猜一个更小的数让每次都多跑几轮，不如超了就地退让：拆成两半重跑，
// 直到能装下为止。拆到单页还装不下，说明单页内容就超过上限，如实报告。
func (s *ImportService) extractPageBatches(
	ctx context.Context,
	call pageCall,
	pageData, answerData, pageTexts []string,
	perCall, answerCount int,
	warnings []string,
) ([]ai.ExtractedQuestion, []string, error) {
	if perCall <= 0 {
		perCall = 1
	}
	batches := chunkStrings(pageData, perCall)

	ranges := pageQuestionRanges(pageTexts, len(pageData))
	results := make([][]ai.ExtractedQuestion, len(batches))
	errs := make([]error, len(batches))
	var wg sync.WaitGroup
	sem := make(chan struct{}, importing.ExtractionConcurrency)

	for i := range batches {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				errs[i] = ctx.Err()
				return
			}
			defer func() { <-sem }()
			results[i], errs[i] = s.callPages(ctx, call, batches[i], answerData, answerCount, 0)
			if errs[i] == nil {
				r := rangeForPages(ranges, i, perCall)
				assignSourceNumbersByRange(results[i], r.from, r.to)
			}
		}(i)
	}
	wg.Wait()

	var merged []ai.ExtractedQuestion
	for i := range batches {
		if errs[i] != nil {
			warnings = append(warnings, fmt.Sprintf(
				"第 %d 页组（共 %d 组）识别失败，该部分题目缺失：%s（%s）",
				i+1, len(batches), pageRangeLabel(i, perCall), humanizeAIError(errs[i])))
			slog.Warn("PDF 页组抽取失败", "batch", i, "error", errs[i])
			continue
		}
		if len(batches[i]) == 1 {
			warnings = append(warnings, s.attachPageFigures(batches[i][0], results[i])...)
		}
		merged = append(merged, reidentify(len(merged), results[i])...)
	}
	if len(merged) == 0 {
		return nil, nil, apperr.New(apperr.CodeAIUnavailable,
			"这份 PDF 里的题目一道都没能识别出来").WithDetail("%s", strings.Join(warnings, "；"))
	}
	return merged, warnings, nil
}

// callPages 用一批页图调用模型，装不下就逐步细分。
//
// 两级细分，对应两种"装不下"：
//  1. **多页一批装不下** → 拆成两半，各发一次（页是天然边界）。
//  2. **单页就装不下** → 把这一页的图横向切成两条再发。
//     实测 2026 全国 1 卷第 1 页，11 道题带解析就要 3 万多 token，
//     32K 装不下——此时按页拆已经无能为力，只能拆页面本身。
//
// 深度有上限：拆到 maxSliceDepth 层仍然装不下，就如实报错。
// 再切下去每段只剩几行字，模型既认不出题也写不出解析，
// 那种结果比明确失败更糟——它看起来像成功了。
func (s *ImportService) callPages(
	ctx context.Context,
	call pageCall,
	pages, answerData []string,
	answerCount int,
	depth int,
) ([]ai.ExtractedQuestion, error) {
	imgs := make([]string, 0, len(pages)+len(answerData))
	imgs = append(imgs, pages...)
	imgs = append(imgs, answerData...)

	out, err := call("", imgs, len(pages), answerCount)
	if err == nil {
		return out, nil
	}
	if !errors.Is(err, ai.ErrTruncated) || depth >= maxSliceDepth {
		return nil, err
	}

	// 还能按页拆就先按页拆——比切图更保守，不会动到版面。
	if len(pages) > 1 {
		half := len(pages) / 2
		slog.Info("页图批次输出被截断，拆页重试",
			"pages", len(pages), "depth", depth, "left", half, "right", len(pages)-half)
		lhs, lerr := s.callPages(ctx, call, pages[:half], answerData, answerCount, depth)
		rhs, rerr := s.callPages(ctx, call, pages[half:], answerData, answerCount, depth)
		return mergePartial(lhs, lerr, rhs, rerr)
	}

	// 只剩一页了：把这一页的图切成两条。
	parts, serr := visionprep.SplitVertical(pages[0], 2)
	if serr != nil || len(parts) < 2 {
		// 切不动（图片过小或不是 data URL）：如实把截断报出去。
		return nil, err
	}
	slog.Info("单页输出被截断，切图重试", "depth", depth, "parts", len(parts))

	lhs, lerr := s.callPages(ctx, call, parts[:1], answerData, answerCount, depth+1)
	rhs, rerr := s.callPages(ctx, call, parts[1:], answerData, answerCount, depth+1)
	return mergePartial(lhs, lerr, rhs, rerr)
}

// maxSliceDepth 切图重试的层数上限。
//
// 2 层的意思是：整页 → 两半 → 每半再两半，最多把一页拆成 4 条。
// 再多就切到认不出题了，而"看起来成功、题目却是碎的"是最难发现的一类坏结果。
const maxSliceDepth = 2

// mergePartial 合并两半的结果：一半失败时保留另一半。
//
// 全都失败才返回错误；只要有一半成功，就带着告警往下走——
// 部分成功远好过整份重来，用户还能看见缺了哪部分。
func mergePartial(lhs []ai.ExtractedQuestion, lerr error, rhs []ai.ExtractedQuestion, rerr error) ([]ai.ExtractedQuestion, error) {
	switch {
	case lerr != nil && rerr != nil:
		return nil, lerr
	case lerr != nil:
		slog.Warn("细分后有一半失败", "error", lerr)
		return rhs, nil
	case rerr != nil:
		slog.Warn("细分后有一半失败", "error", rerr)
		return lhs, nil
	}
	return append(lhs, rhs...), nil
}

// chunkStrings 把切片按固定大小分批，保留顺序。
func chunkStrings(in []string, size int) [][]string {
	if size <= 0 || len(in) <= size {
		return [][]string{in}
	}
	out := make([][]string, 0, (len(in)+size-1)/size)
	for i := 0; i < len(in); i += size {
		end := min(i+size, len(in))
		out = append(out, in[i:end])
	}
	return out
}

// pageRangeLabel 给人看的页码范围，例如 "第 1~2 页"。
func pageRangeLabel(index, perCall int) string {
	from := index*perCall + 1
	return fmt.Sprintf("第 %d~%d 页", from, from+perCall-1)
}

// maxInlineImageBytes 单次抽取允许内联的图片总字节数。
//
// 每张引用的图都会被读进来、base64 后直接写进提示词——base64 还要再膨胀
// 约 1/3。没有这个预算时，100 页扫描件会变成几十 MB 的请求体：
// 供应商那边要么 413，要么按这个体量计费，而用户在提交前毫无察觉。
//
// 声明为变量而不是常量，是为了让测试能把它调小（同 backoffBase）。
var maxInlineImageBytes int64 = 24 << 20

// materializeImages 把图片引用换成模型能真正拿到的东西。
//
// 上传接口返回的是 /media/xxx.jpg 这种**本服务自己的**相对路径。
// 模型跑在供应商那边，它没有任何办法访问我们的 /media——
// 原样发过去，模型只会拿到一个无法解析的地址，然后"看不见"地编一通题，
// 那是比明确失败危险得多的结果。所以这里必须把图读回来内联成 data URL。
//
// 已经是 data URL 或 http(s) 的原样放行：前端也可能直接传公网地址。
// 单张读不出来只丢这一张并如实报告，不让整批录题失败。
// 内联总量受 maxInlineImageBytes 约束，超预算的直接跳过并写进告警。
func (s *ImportService) materializeImages(refs []string) ([]string, []string, error) {
	if len(refs) == 0 {
		return nil, nil, nil
	}

	out := make([]string, 0, len(refs))
	var warnings []string
	var used int64

budget:
	for i, ref := range refs {
		ref = strings.TrimSpace(ref)
		if isInlineImageRef(ref) {
			// 已经是 data URL 或公网地址的按长度计：它同样会原样进提示词。
			if used+int64(len(ref)) > maxInlineImageBytes {
				warnings = append(warnings, budgetWarning(i))
				break budget
			}
			used += int64(len(ref))
			out = append(out, ref)
			continue
		}

		if s.storage == nil {
			return nil, nil, apperr.New(apperr.CodeInternal,
				"服务未配置媒体存储，图片识题不可用")
		}
		data, err := s.storage.Read(ref)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf(
				"第 %d 张图片读取失败，已跳过：%v", i+1, err))
			slog.Warn("读取导入图片失败", "ref", ref, "error", err)
			continue
		}
		if used+int64(len(data)) > maxInlineImageBytes {
			warnings = append(warnings, budgetWarning(i))
			break budget
		}
		url, err := visionprep.ToDataURL(data)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf(
				"第 %d 张图片无法处理，已跳过：%v", i+1, err))
			slog.Warn("处理导入图片失败", "ref", ref, "error", err)
			continue
		}
		used += int64(len(data))
		out = append(out, url)
	}

	if len(out) == 0 {
		return nil, warnings, apperr.New(apperr.CodeInvalidParam,
			"图片都不可用，无法识别").WithDetail("%s", strings.Join(warnings, "；"))
	}
	return out, warnings, nil
}

// isInlineImageRef 判断引用是否已经可以直接发给模型，不必读本地文件。
func isInlineImageRef(ref string) bool {
	return strings.HasPrefix(ref, "data:image/") ||
		strings.HasPrefix(ref, "http://") ||
		strings.HasPrefix(ref, "https://")
}

// budgetWarning 内联预算耗尽时的告警，带上跳过的位置与额度。
func budgetWarning(index int) string {
	return fmt.Sprintf("图片总量超过 %d MB，第 %d 张及其后的图片已跳过",
		maxInlineImageBytes>>20, index+1)
}

// firstError 挑一个最有代表性的错误返回。
//
// 同一个作业里所有分块几乎总是同一个原因（限流、鉴权、上游故障），
// 所以直接报第一个错就够；把四个一模一样的错误堆在一起只会更难读。
func firstError(errs []error, failed []int) error {
	if len(failed) == 0 {
		return nil
	}
	return errs[failed[0]]
}

// reidentify 把一个分块的临时编号换成全局唯一的，并按偏移改写 parent。
//
// 模型在每一块里都从 1 开始编号，不改的话入库时 Commit 里的 draftDepths
// 会把后一块的"母题 1"认成前一块的，母子关系直接错位。
//
// offset 是本块之前已经合并进去的题目数。
func reidentify(offset int, block []ai.ExtractedQuestion) []ai.ExtractedQuestion {
	out := make([]ai.ExtractedQuestion, len(block))
	copy(out, block)

	// 先建旧→新映射，再统一改写子题指向。
	// 两趟而不是一趟：模型完全可能把母题排在子题后面。
	//
	// 新编号按**位置**存，不靠旧编号反查：同一块里出现重复 temp_id 时，
	// 反查会让两道题拿到同一个新编号，合并之后照样冲突。
	newIDs := make([]string, len(out))
	idMap := make(map[string]string, len(out))
	for i := range out {
		old := strings.TrimSpace(out[i].TempID)
		if old == "" {
			continue
		}
		newIDs[i] = fmt.Sprintf("c%dq%d", offset, i)
		if _, dup := idMap[old]; !dup {
			// 重复编号只认第一次：猜错会把一道题挂到另一道题下面，
			// 那比挂不上更难查。
			idMap[old] = newIDs[i]
		}
	}
	for i := range out {
		if newIDs[i] == "" {
			continue
		}
		out[i].TempID = newIDs[i]
		parent := strings.TrimSpace(out[i].ParentTempID)
		if parent == "" {
			continue
		}
		// 母题不在本块里（切分点本该避免，但模型也可能写错）：
		// 保留原样的 parent 比悄悄改成空更容易被发现。
		if mapped, ok := idMap[parent]; ok {
			out[i].ParentTempID = mapped
		}
	}
	return out
}

// validateImages 校验图片引用是模型能拿到的形态。
//
// 接受三类：data URL、http(s) 绝对地址、以及**本服务自己的媒体地址**
// （/media/…）。第三类以前被这里挡掉了，可 materializeImages 明确是按它
// 读回文件再内联的——两处对同一份输入给出相反判断，于是"存了图再交给模型"
// 这条路根本走不通。PDF 渲染出的页图正是这种地址。
//
// 放开它不构成越权：读回时走 storage.Read，那里有媒体目录边界检查与
// 绝对路径兜底（见 storage.Local.Read），传入 "../../etc/passwd" 会被挡下。
func validateImages(images []string) error {
	for i, img := range images {
		s := strings.TrimSpace(img)
		if s == "" {
			return apperr.New(apperr.CodeInvalidParam, "图片内容为空").
				WithDetail("第 %d 张", i+1)
		}
		ok := strings.HasPrefix(s, "data:image/") ||
			strings.HasPrefix(s, "http://") ||
			strings.HasPrefix(s, "https://") ||
			strings.HasPrefix(s, "/")
		if !ok {
			return apperr.New(apperr.CodeInvalidParam,
				"图片必须是 data URL、http(s) 地址或本站媒体地址").WithDetail("第 %d 张", i+1)
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
	if mode == importing.ModeRecord {
		return "record"
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

func ptrTo[T any](v T) *T { return &v }

// extractSourceLabel 抽取记录的来源描述。
//
// 现状：还没有文件上传入口（粘贴文本 / 拍照识题是仅有的两条路），
// 拿不到真实文件名。与其让历史列表一列空白，不如如实写清这次到底导了什么——
// "图片识别（3 张）"对用户是有用信息，NULL 不是。
// 接入文件上传之后，这里会优先返回用户上传时的原始文件名。
func extractSourceLabel(req *schema.ExtractRequest, mode importing.Mode) string {
	if name := sanitizeFilename(req.Filename); name != "" {
		return name
	}
	switch {
	case len(req.Images) > 0 && strings.TrimSpace(req.Content) == "":
		return fmt.Sprintf("图片识别（%d 张）", len(req.Images))
	case mode == importing.ModeSolve:
		return "补写答案"
	case mode == importing.ModeRecord:
		return "仅录题"
	default:
		return "粘贴文本"
	}
}

// commitSourceLabel 入库记录的来源描述。
func commitSourceLabel(req *schema.CommitRequest) string {
	if name := sanitizeFilename(req.Filename); name != "" {
		return name + "（入库）"
	}
	return fmt.Sprintf("入库审核（%d 题）", len(req.Drafts))
}

// sanitizeFilename 清洗调用方给的文件名：只保留路径末段，去掉控制字符。
//
// 它会落库并回显到历史列表，因此不能原样存——传 "../../etc/passwd"
// 或一段 ANSI 转义序列都不该被接受。超过 200 字符的部分按**字符**截断，
// 按字节切会把一个中文字劈成乱码。
func sanitizeFilename(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	name = strings.ReplaceAll(name, "\\", "/")
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if runes := []rune(name); len(runes) > 200 {
		name = string(runes[:200])
	}
	return name
}
