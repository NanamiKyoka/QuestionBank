package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/NanamiKyoka/questionmind/internal/ai"
	"github.com/NanamiKyoka/questionmind/internal/domain"
	"github.com/NanamiKyoka/questionmind/internal/importing"
	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
	"github.com/NanamiKyoka/questionmind/internal/schema"
)

// SolveService 在题库里为已有题目补写答案与解析。
//
// 这是「仅录题」的另一半。录题时为了不等模型把每道解答题算一遍
// （实测约 1370 输出 token/题，整卷近三万 token），答案被留成「待补充」；
// 如果之后没有地方把它补上，那个模式就只是把工作从"等模型"挪成了"老师自己写"。
// 所以补写入口必须在题库里：题目已经在库里，题型、选项都是确定的，
// 模型只需要回答答案本身。
//
// 与智能录题的 ImportService 分开：那条链路处理的是"材料"，这条处理的是
// "库里的一道题"，两者的输入形态、失败模式与权限判定的入口都不同。
type SolveService struct {
	repos     *Repos
	aiConfig  *AIConfigService
	questions *QuestionService
}

// NewSolveService 构造题库内解题服务。
func NewSolveService(repos *Repos, aiConfig *AIConfigService, questions *QuestionService) *SolveService {
	return &SolveService{repos: repos, aiConfig: aiConfig, questions: questions}
}

// solveTimeout 单题求解的墙钟上限。
//
// 比抽取的预算短：这里只有一道题，哪怕它是一道大题，
// 几分钟也足够；卡住不返回时用户更需要的是"失败"，而不是一直转圈。
const solveTimeout = 5 * time.Minute

// Solve 让模型解一道题，并把答案与解析写回这道题。
//
// 返回的 *schema.SolveResult 覆盖三种结局，且**都算成功响应**：
//   - solved：答案已写入；
//   - skipped：这道题本来就有答案，而调用方没要求覆盖（不动数据）；
//   - failed：模型没给出可用答案，或给出的答案过不了题型校验。
//
// 为什么模型/解析层面的问题不回 4xx/5xx：批量解题是前端逐题循环调用的，
// 一道题答不出来（模型抽风、空位对不上）不该让后面九道题一起做不下去。
// 真正的硬错误（没权限、题目不存在、没配模型）仍然返回 error。
func (s *SolveService) Solve(ctx context.Context, user *model.User, id uint, req *schema.SolveRequest) (*schema.SolveResult, error) {
	q, err := s.repos.Questions.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := authorize(user, model.PermEditQuestion, q.SubjectID); err != nil {
		return nil, err
	}

	var current model.AnswerSpec
	if !q.Answer.IsEmpty() {
		_ = q.Answer.Into(&current)
	}
	if !req.Overwrite && answerIsFilled(current) {
		return &schema.SolveResult{
			QuestionID: id,
			Status:     "skipped",
			Message:    "这道题已经有答案了，未做修改",
		}, nil
	}

	var options []model.Option
	if !q.Options.IsEmpty() {
		_ = q.Options.Into(&options)
	}

	// 母题的小问：材料题的题目本体常常挂在小问上，只把母题发给模型，
	// 等于让它对着一段只有背景介绍的文字凭空编答案。
	children, err := s.repos.Questions.Children(ctx, q.ID)
	if err != nil {
		return nil, err
	}

	// 模型必须先解析成功：没配模型、模型被禁用这类问题是配置问题，
	// 该让整批解题停下来并明确告知，而不是伪装成"这道题没解出来"。
	resolved, err := s.aiConfig.Resolve(ctx, model.AIModelText)
	if err != nil {
		return nil, err
	}
	subject, err := s.repos.Subjects.Get(ctx, q.SubjectID)
	if err != nil {
		return nil, err
	}

	content := solvePromptContent(q, options, children)
	promptOpts := importing.PromptOptions{
		Mode: importing.ModeSolve,
		// 带上学科：提示词可以按学科覆盖（见 importing.ResolveSystemPrompt）。
		SubjectID:   &q.SubjectID,
		SubjectName: subject.Name,
	}

	callCtx, cancel := context.WithTimeout(ctx, solveTimeout)
	defer cancel()

	out, err := resolved.Provider.ExtractQuestions(callCtx, ai.ExtractRequest{
		Model:     resolved.ModelName,
		Prompt:    importing.BuildSystemPrompt(promptOpts),
		Content:   importing.BuildUserContent(content, promptOpts),
		MaxTokens: resolved.MaxTokens,
	})
	if err != nil {
		// 模型层面失败：如实说成"这一道没解出来"，让批量的其余题目继续。
		return &schema.SolveResult{
			QuestionID: id,
			Status:     "failed",
			Message:    humanizeAIError(err),
			Model:      resolved.Display,
		}, nil
	}
	if len(out) == 0 {
		return &schema.SolveResult{
			QuestionID: id,
			Status:     "failed",
			Message:    "模型没有返回任何内容",
			Model:      resolved.Display,
		}, nil
	}

	// 只看第一条：一次只发一道题，模型多吐的几条无从对应，也不该拿它们去猜。
	item := out[0]
	rawAnswer := strings.TrimSpace(item.Answer)
	if rawAnswer == "" {
		return &schema.SolveResult{
			QuestionID: id,
			Status:     "failed",
			Message:    "模型没有给出答案（可能它认为题目信息不足）",
			Model:      resolved.Display,
		}, nil
	}

	answer, warns := importing.ParseSolvedAnswer(q.QuestionType, rawAnswer, options, q.StemText)

	update := &schema.QuestionUpdateRequest{Answer: &answer}
	var analysisJSON model.JSON
	if md := strings.TrimSpace(item.Analysis); md != "" {
		analysisJSON = importing.MarkdownToRichDoc(md)
		update.Analysis = analysisJSON
	}
	// 变更说明写进审核日志：日后看到"这道题的答案是 AI 写的"，
	// 比看到一条没有任何说明的静默修改有用得多。
	update.ChangeNote = "AI 补写答案"

	detail, err := s.questions.Update(ctx, user, id, update)
	if err != nil {
		// 校验不过（空位数量对不上、选项引用不存在……）是最常见的一种失败，
		// 它属于"这一道题的答案不能用"，逐题报出来即可；内部错误才该中断。
		if apperr.Is(err, apperr.CodeInternal) || apperr.Is(err, apperr.CodeNotFound) {
			return nil, err
		}
		return &schema.SolveResult{
			QuestionID: id,
			Status:     "failed",
			Message:    "模型给出的答案未通过校验：" + apperr.From(err).Message,
			Model:      resolved.Display,
			Warnings:   warns,
		}, nil
	}

	return &schema.SolveResult{
		QuestionID:       id,
		Status:           "solved",
		Answer:           answer,
		Analysis:         analysisJSON,
		AnalysisMarkdown: strings.TrimSpace(item.Analysis),
		AnsweredText:     describeAnswer(q.QuestionType, answer),
		Warnings:         warns,
		Question:         detail,
		Model:            resolved.Display,
	}, nil
}

// answerIsFilled 判断一道题是否已经有答案。
//
// pending 是"按计划留白"（仅录题），unresolved 是"历史遗留的读不懂的答案"——
// 这两者都不算"有答案"，覆盖它们正是用户点这个按钮的目的。
func answerIsFilled(a model.AnswerSpec) bool {
	switch a.Kind {
	case "", model.AnswerKindPending, "unresolved":
		return false
	}
	return true
}

// describeAnswer 给人看的答案摘要。
//
// 富文本答案没法在列表里直接看，而"AI 写了什么"恰恰是用户最想先扫一眼的东西，
// 所以这里给出一句能读的话；完整内容仍然写进了题目详情。
func describeAnswer(qtype model.QuestionType, a model.AnswerSpec) string {
	switch a.Kind {
	case "single", "multiple":
		if len(a.Correct) == 0 {
			return "（未识别出选项）"
		}
		return strings.Join(a.Correct, "")
	case "boolean":
		if a.BoolAnswer == nil {
			return "（未识别出对错）"
		}
		if *a.BoolAnswer {
			return "正确"
		}
		return "错误"
	case "blanks":
		parts := make([]string, 0, len(a.Blanks))
		for i := range a.Blanks {
			group := make([]string, 0, len(a.Blanks[i].Accepts))
			for _, accepts := range a.Blanks[i].Accepts {
				alts := make([]string, 0, len(accepts))
				for _, item := range accepts {
					var doc domain.RichDoc
					if err := item.Into(&doc); err == nil {
						alts = append(alts, domain.PlainText(doc))
					}
				}
				if len(alts) > 0 {
					group = append(group, strings.Join(alts, " "))
				}
			}
			parts = append(parts, fmt.Sprintf("第%d空 %s", i+1, strings.Join(group, "或")))
		}
		if len(parts) == 0 {
			return "（未识别出填空答案）"
		}
		return strings.Join(parts, "；")
	case "reference":
		var doc domain.RichDoc
		if err := a.Reference.Into(&doc); err != nil {
			return "参考答案（内容无法预览）"
		}
		text := []rune(strings.TrimSpace(domain.PlainText(doc)))
		if len(text) == 0 {
			return "参考答案"
		}
		const preview = 40
		if len(text) > preview {
			return fmt.Sprintf("参考答案 %d 字：%s…", len(text), string(text[:preview]))
		}
		return "参考答案：" + string(text)
	}
	return qtype.Label() + "答案"
}

// solvePromptContent 把库里的一道题堆成给模型看的文本。
//
// 题型与选项一并给出：它们已经是确定的事实，让模型自己从题干里猜题型
// 是纯粹的风险——猜错题型，答案结构就整条错位。公式保留 $ 定界符
// （domain.MarkdownText），否则模型会把数学当成散文。
//
// 末尾的「作答要求」不是客套话。模型看到一段带选项的文字时，很容易
// 直接把**选项的内容**当成答案（"正确答案是 $x=3$"）；而答案解析器
// extractChoiceLetters 要求整串都是选项字母，于是这一题被判成
// "未能识别答案，请手动选择正确选项"——用户看到的就是"AI 根本没看选项"。
// 把规则写在离选项最近的地方，比事后猜模型为什么答错要便宜得多。
func solvePromptContent(q *model.Question, options []model.Option, children []model.Question) string {
	var sb strings.Builder

	var stem domain.RichDoc
	_ = q.Stem.Into(&stem)

	sb.WriteString("题型：")
	sb.WriteString(q.QuestionType.Label())
	// 题型是库里已经确定的事实，不是有待模型判断的问题。
	sb.WriteString("（已确定，不要改成别的题型）\n\n")
	sb.WriteString("题干：\n")
	sb.WriteString(domain.MarkdownText(stem))
	sb.WriteString("\n")

	if len(options) > 0 {
		sb.WriteString("\n选项（与题干同属这道题，缺一不可）：\n")
		for i := range options {
			var content domain.RichDoc
			_ = options[i].Content.Into(&content)
			sb.WriteString(options[i].Label)
			sb.WriteString(". ")
			sb.WriteString(domain.MarkdownText(content))
			sb.WriteString("\n")
		}
	}

	if len(children) > 0 {
		sb.WriteString("\n本题下的小问（仅作背景，不要为它们作答）：\n")
		for i := range children {
			var childStem domain.RichDoc
			_ = children[i].Stem.Into(&childStem)
			fmt.Fprintf(&sb, "(%d) %s\n", i+1, domain.MarkdownText(childStem))
		}
	}

	sb.WriteString("\n作答要求：\n")
	rules := make([]string, 0, 3)
	if len(children) > 0 {
		rules = append(rules, "只回答上面这道题，不要顺带回答下面的小问。")
	}
	if len(options) > 0 {
		rules = append(rules,
			"题型是选择题，答案**只写选项字母**（例如 B 或 BD），"+
				"不要写选项内容，也不要写「答案是」「故选」之类的说明文字。")
	}
	rules = append(rules, "answer 字段只放答案本身，解析过程放在 analysis 字段里。")
	for i, rule := range rules {
		fmt.Fprintf(&sb, "%d. %s\n", i+1, rule)
	}

	return sb.String()
}
