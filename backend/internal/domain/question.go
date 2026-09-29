// Package domain 承载业务规则与不变量校验。
//
// 为什么要有这一层：题目的"答案必须与题型匹配、选项 id 必须被答案引用"这类规则
// 数据库无法表达（JSON 列没有约束能力）。把它们集中在这里，好处是
// HTTP 接口、AI 抽取、批量导入三条写入路径共用同一套校验，
// 不会出现"从界面录题被拦住、AI 录题却能写进脏数据"的情况。
package domain

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
)

// ---- 富文本 ----

// RichDoc 是富文本文档的最小结构，与前端编辑器（Tiptap）的 JSON 一致。
//
// 这里刻意不定义全部节点类型：后端只需要能"取纯文本"和"数空位"，
// 其余节点原样透传即可。前端新增节点类型时后端无需改动。
type RichDoc struct {
	Type    string `json:"type"`
	Content []Node `json:"content,omitempty"`
}

// Node 富文本节点。
type Node struct {
	Type    string         `json:"type"`
	Text    string         `json:"text,omitempty"`
	Content []Node         `json:"content,omitempty"`
	Marks   []any          `json:"marks,omitempty"`
	Attrs   map[string]any `json:"attrs,omitempty"`
}

// PlainText 提取纯文本，用于列表展示与关键词检索。
//
// 表格用空格分隔单元格、行之间用换行，避免把相邻单元格的文本粘成一个词。
//
// 公式与空位必须显式处理：它们的文字信息在 attrs 里而不是 content
// 里，走 default 分支递归会**什么都取不到**。结果是"若 f(x)=x^2，则…"
// 变成"若 ，则…"——公式位置留下空洞，既没法用公式做关键词检索，
// 也让 AI 助手看到的题干残缺不全。
func PlainText(doc RichDoc) string {
	var sb strings.Builder
	walkText(doc.Content, &sb)
	// 折叠连续空行：段落之间已经加了 \n，公式块又各加一次，
	// 不收敛会得到一串空行，展示时很难看。
	return collapseBlankLines(strings.TrimSpace(sb.String()))
}

func walkText(nodes []Node, sb *strings.Builder) {
	for i := range nodes {
		n := &nodes[i]
		switch n.Type {
		case "text":
			sb.WriteString(n.Text)
		case "hardBreak":
			sb.WriteString("\n")
		case "blank":
			sb.WriteString("______")
		case "inlineMath":
			// 用 LaTeX 原文而不是渲染后的字形：检索时用户会输入
			// "x^2" 这样的写法，存源文才能匹配上。
			sb.WriteString(mathLatex(n.Attrs))
		case "blockMath":
			sb.WriteString("\n")
			sb.WriteString(mathLatex(n.Attrs))
			sb.WriteString("\n")
		case "tableRow":
			walkText(n.Content, sb)
			sb.WriteString("\n")
		case "tableCell":
			walkText(n.Content, sb)
			sb.WriteString(" ")
		case "paragraph", "heading":
			walkText(n.Content, sb)
			sb.WriteString("\n")
		default:
			walkText(n.Content, sb)
		}
	}
}

func mathLatex(attrs map[string]any) string {
	if attrs == nil {
		return ""
	}
	if latex, ok := attrs["latex"].(string); ok {
		return latex
	}
	return ""
}

func collapseBlankLines(text string) string {
	if !strings.Contains(text, "\n\n") {
		return text
	}
	out := make([]string, 0, 8)
	previousBlank := true // 开头的空行也要去掉
	for _, line := range strings.Split(text, "\n") {
		blank := strings.TrimSpace(line) == ""
		if blank && previousBlank {
			continue
		}
		out = append(out, line)
		previousBlank = blank
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// CountBlanks 统计题干中填空题空位节点的数量。
func CountBlanks(doc RichDoc) int {
	return countNodes(doc.Content, "blank")
}

func countNodes(nodes []Node, want string) int {
	n := 0
	for i := range nodes {
		if nodes[i].Type == want {
			n++
		}
		n += countNodes(nodes[i].Content, want)
	}
	return n
}

// ---- 选项 ----

// OptionID 生成稳定的选项 id。
//
// 用「序号 + 标签 + 内容」的哈希，而不是随机值或数据库自增：
// 同一道题经过 AI 抽取、前端编辑、再次保存之后，只要内容没变，
// id 就不会变，答案里的引用自然继续有效。
// 若用随机 id，每次保存答案都会失效，用户会莫名其妙地"答案丢了"。
func OptionID(index int, label, content string) string {
	h := sha1.New()
	fmt.Fprintf(h, "%d\x00%s\x00%s", index, label, content)
	return "opt_" + hex.EncodeToString(h.Sum(nil))[:8]
}

// NormalizeOptions 重新计算全部选项 id，并校验标签唯一。
func NormalizeOptions(opts []model.Option) ([]model.Option, error) {
	if len(opts) == 0 {
		return nil, nil
	}
	seen := make(map[string]bool, len(opts))
	out := make([]model.Option, len(opts))
	for i := range opts {
		label := strings.TrimSpace(opts[i].Label)
		if label == "" {
			return nil, apperr.New(apperr.CodeInvalidParam, "选项标签不能为空")
		}
		if seen[label] {
			return nil, apperr.New(apperr.CodeInvalidParam, "选项标签重复").WithDetail("label=%s", label)
		}
		seen[label] = true

		var doc RichDoc
		if len(opts[i].Content) > 0 {
			_ = opts[i].Content.Into(&doc)
		}
		out[i] = model.Option{
			ID:      OptionID(i, label, PlainText(doc)),
			Label:   label,
			Content: opts[i].Content,
		}
	}
	return out, nil
}

// OptionIDs 返回选项 id 集合。
func OptionIDs(opts []model.Option) map[string]bool {
	set := make(map[string]bool, len(opts))
	for i := range opts {
		set[opts[i].ID] = true
	}
	return set
}

// ResolveCorrectLabels 把答案里的选项引用统一解析为选项 id。
//
// 选项 id 是服务端按内容哈希生成的，客户端在提交前无从知道。
// 因此接口约定：答案里可以填选项**标签**（"A"/"B"），由服务端解析为 id。
// 已经填了合法 id 的也接受，保证幂等（读取后原样回写不会出错）。
func ResolveCorrectLabels(opts []model.Option, answer *model.AnswerSpec) error {
	if len(answer.Correct) == 0 {
		return nil
	}

	byID := make(map[string]bool, len(opts))
	byLabel := make(map[string]string, len(opts))
	for i := range opts {
		byID[opts[i].ID] = true
		byLabel[strings.ToUpper(strings.TrimSpace(opts[i].Label))] = opts[i].ID
	}

	resolved := make([]string, 0, len(answer.Correct))
	for _, ref := range answer.Correct {
		ref = strings.TrimSpace(ref)
		switch {
		case byID[ref]:
			resolved = append(resolved, ref)
		case byLabel[strings.ToUpper(ref)] != "":
			resolved = append(resolved, byLabel[strings.ToUpper(ref)])
		default:
			return apperr.New(apperr.CodeAnswerMismatch, "答案引用了不存在的选项").
				WithDetail("引用值 %q 既不是选项 id 也不是选项标签", ref)
		}
	}
	answer.Correct = resolved
	return nil
}

// ---- 题型与答案的一致性 ----

// ValidateShape 校验题型的"外形"：题型合法、选项数量、答案结构种类。
//
// 与 ValidateQuestion 分开的原因：答案里的选项引用既可以写标签也可以写 id，
// 需要先确定外形再去解析引用。否则"选择题一个选项都没给"会被报成
// "引用了不存在的选项"，把真正的问题掩盖掉。
func ValidateShape(qtype model.QuestionType, opts []model.Option, answer model.AnswerSpec) error {
	if !qtype.Valid() {
		return apperr.New(apperr.CodeInvalidParam, "非法的题型").WithDetail("type=%s", qtype)
	}

	if qtype.HasOptions() && len(opts) < 2 {
		return apperr.New(apperr.CodeInvalidParam, "选择题至少需要两个选项")
	}
	if !qtype.HasOptions() && len(opts) > 0 {
		return apperr.New(apperr.CodeInvalidParam, "该题型不应包含选项").
			WithDetail("type=%s options=%d", qtype, len(opts))
	}

	switch qtype {
	case model.QuestionTypeSingleChoice:
		return requireKind(answer, "single")
	case model.QuestionTypeMultipleChoice:
		return requireKind(answer, "multiple")
	case model.QuestionTypeTrueFalse:
		return requireKind(answer, "boolean")
	case model.QuestionTypeFillBlank:
		return requireKind(answer, "blanks")
	case model.QuestionTypeEssay:
		return requireKind(answer, "reference")
	}
	return nil
}

// ValidateQuestion 校验题型、选项、答案三者是否自洽。
// 调用前应先通过 ValidateShape，并用 ResolveCorrectLabels 把引用解析为选项 id。
func ValidateQuestion(qtype model.QuestionType, stem RichDoc, opts []model.Option, answer model.AnswerSpec) error {
	if err := ValidateShape(qtype, opts, answer); err != nil {
		return err
	}

	ids := OptionIDs(opts)

	switch qtype {
	case model.QuestionTypeSingleChoice:
		if len(answer.Correct) != 1 {
			return apperr.New(apperr.CodeInvalidAnswer, "单选题必须且只能有一个正确选项")
		}
		if !ids[answer.Correct[0]] {
			return apperr.New(apperr.CodeAnswerMismatch, "正确选项不在选项列表中")
		}

	case model.QuestionTypeMultipleChoice:
		// 多选题要求**至少两个**正确选项：只有一个正确答案的"多选题"
		// 与单选没有区别，多半是录入或模型抽取时判错了题型。
		// 放过它会让题库里混入无法区分单选的题目，且事后无从发现。
		if len(answer.Correct) < 2 {
			return apperr.New(apperr.CodeInvalidAnswer, "多选题至少需要两个正确选项").
				WithDetail("当前 %d 个；若只有一个正确答案请改用单选题", len(answer.Correct))
		}
		dup := make(map[string]bool, len(answer.Correct))
		for _, id := range answer.Correct {
			if !ids[id] {
				return apperr.New(apperr.CodeAnswerMismatch, "正确选项不在选项列表中").
					WithDetail("option_id=%s", id)
			}
			if dup[id] {
				return apperr.New(apperr.CodeInvalidAnswer, "正确选项重复")
			}
			dup[id] = true
		}

	case model.QuestionTypeTrueFalse:
		if answer.BoolAnswer == nil {
			return apperr.New(apperr.CodeInvalidAnswer, "判断题必须给出正确或错误")
		}

	case model.QuestionTypeFillBlank:
		if len(answer.Blanks) == 0 {
			return apperr.New(apperr.CodeInvalidAnswer, "填空题至少需要一个空")
		}
		if expected := CountBlanks(stem); expected > 0 && expected != len(answer.Blanks) {
			return apperr.New(apperr.CodeAnswerMismatch, "答案个数与题干空位数量不一致").
				WithDetail("题干空位 %d，答案 %d", expected, len(answer.Blanks))
		}
		for i := range answer.Blanks {
			b := &answer.Blanks[i]
			if b.ID == "" {
				b.ID = fmt.Sprintf("blk_%d", i+1)
			}
			if len(b.Accepts) == 0 {
				return apperr.New(apperr.CodeInvalidAnswer, "每个空至少要有一个可接受答案").
					WithDetail("blank=%s", b.ID)
			}
			for j := range b.Accepts {
				if len(b.Accepts[j]) == 0 {
					return apperr.New(apperr.CodeInvalidAnswer, "可接受答案不能为空组").
						WithDetail("blank=%s group=%d", b.ID, j)
				}
			}
		}

	case model.QuestionTypeEssay:
		if answer.Reference.IsEmpty() {
			return apperr.New(apperr.CodeInvalidAnswer, "解答题需要提供参考答案")
		}
	}

	return nil
}

func requireKind(answer model.AnswerSpec, want string) error {
	if answer.Kind == "unresolved" {
		return apperr.New(apperr.CodeInvalidAnswer,
			"该题目的答案格式无法识别，请先修正后再保存")
	}
	if answer.Kind != want {
		return apperr.New(apperr.CodeAnswerMismatch, "答案结构与题型不匹配").
			WithDetail("题型要求 %s，实际 %s", want, answer.Kind)
	}
	return nil
}

// NormalizeQuestionDisplay 把显示设置补齐为四个确定的布尔值。
func NormalizeQuestionDisplay(raw model.JSON) (model.JSON, model.QuestionDisplay) {
	d := model.DefaultQuestionDisplay()
	if !raw.IsEmpty() {
		var parsed model.QuestionDisplay
		if err := raw.Into(&parsed); err == nil {
			d = parsed
		}
	}
	out, err := model.JSONOf(d)
	if err != nil {
		return raw, d
	}
	return out, d
}
