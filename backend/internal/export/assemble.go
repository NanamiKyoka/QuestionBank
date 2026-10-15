package export

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/NanamiKyoka/questionmind/internal/domain"
	"github.com/NanamiKyoka/questionmind/internal/model"
)

type ContentMode string

const (
	ContentAll       ContentMode = "all"
	ContentQuestions ContentMode = "questions"
	ContentAnswers   ContentMode = "answers"
)

func (m ContentMode) Valid() bool {
	return m == ContentAll || m == ContentQuestions || m == ContentAnswers
}

// QuestionView 导出时需要的题目内容投影。
type QuestionView struct {
	ID        uint
	Type      model.QuestionType
	TypeLabel string

	Stem     model.JSON
	Options  []model.Option
	Answer   model.AnswerSpec
	Analysis model.JSON
	Solution model.JSON
	Summary  model.JSON
}

// Input 组卷导出所需的全部输入。
type Input struct {
	Title            string
	SubjectName      string
	NumberingEnabled bool
	ScoringEnabled   bool
	Display          model.QuestionDisplay

	// Nodes 扁平节点列表（含 parent_id 与 position）。
	Nodes []model.PaperNode
	// Questions 节点引用到的题目内容。
	Questions map[uint]QuestionView
	// TotalScore 分值合计。
	TotalScore float64
	// Content 控制导出题目、答案或两者。
	Content ContentMode
}

// Assemble 把组卷数据组装为可导出文档。
//
// 排版规则全部集中在这里，DOCX 与 LaTeX 两个渲染器都消费同一份 IR，
// 因此两种格式的题号、分值、选项排布不会出现差异。
func Assemble(in Input, loader ImageLoader) *Document {
	if in.Content == "" {
		in.Content = ContentAll
	}
	doc := &Document{
		Title:  in.Title,
		Meta:   map[string]string{"title": in.Title, "subject": in.SubjectName},
		Blocks: []Block{},
	}

	// 标题始终输出。
	//
	// 曾经把它放在 "NumberingEnabled || ScoringEnabled" 条件下，理由是
	// "两者都关就导出一份纯内容"。但标题是元信息而不是内容：
	// 关掉编号后导出的试卷没有标题，打印出来根本认不出是哪份卷子。
	doc.Blocks = append(doc.Blocks, &Paragraph{
		Runs:       []Inline{&Text{Value: in.Title, Bold: true}},
		Align:      AlignCenter,
		SpaceAfter: 8,
	})
	if in.ScoringEnabled && in.TotalScore > 0 {
		doc.Blocks = append(doc.Blocks, &Paragraph{
			Runs:       []Inline{&Text{Value: fmt.Sprintf("（满分 %.0f 分）", in.TotalScore)}},
			Align:      AlignCenter,
			SpaceAfter: 10,
		})
	}

	// 建立父子索引，并按 position 排序。
	children := make(map[string][]model.PaperNode)
	var roots []model.PaperNode
	for _, n := range in.Nodes {
		if n.ParentID == nil {
			roots = append(roots, n)
			continue
		}
		children[*n.ParentID] = append(children[*n.ParentID], n)
	}
	sortByPosition(roots)
	for k := range children {
		sortByPosition(children[k])
	}

	// 逐题跟在题目后的解析条目（scope=before）。键是题目块节点 id。
	anchored := make(map[string]model.PaperNode)
	for _, root := range roots {
		if root.NodeType != model.NodeTypeQuestionDetails {
			continue
		}
		for _, child := range children[root.ID] {
			if child.AnchorBeforeNodeID != nil {
				anchored[*child.AnchorBeforeNodeID] = child
			}
		}
	}

	if in.Content == ContentAnswers {
		appendAnswers(doc, in, roots, children, loader)
		return doc
	}

	counter := newCounter()
	for i := range roots {
		root := &roots[i]
		switch root.NodeType {
		case model.NodeTypeRichText:
			doc.Blocks = append(doc.Blocks, RichDocBlocks(root.Content, loader)...)

		case model.NodeTypeHeading:
			level := attrIntFromJSON(root.Props, "level")
			if level < 1 {
				level = 1
			}
			doc.Blocks = append(doc.Blocks, &Heading{
				Level: level,
				Runs:  RichDocInlines(root.Content, loader),
			})

		case model.NodeTypePageBreak:
			doc.Blocks = append(doc.Blocks, &PageBreak{})

		case model.NodeTypeAnswerSpace:
			doc.Blocks = append(doc.Blocks, answerSpaceOf(root))

		case model.NodeTypeQuestion:
			blocks := assembleQuestion(root, in, loader, counter, in.Content == ContentAll)
			doc.Blocks = append(doc.Blocks, blocks...)

			// scope=before：解析紧跟本题。
			if in.Content == ContentAll {
				if item, ok := anchored[root.ID]; ok {
					doc.Blocks = append(doc.Blocks, assembleAnswerItem(&item, in, loader, sourceQuestionNumber(&item, in, counter.current))...)
				}
			}

		case model.NodeTypeQuestionDetails:
			if in.Content != ContentAll {
				continue
			}
			// scope=all：整卷汇总，全部排在模块所在位置。
			items := children[root.ID]
			allAnchored := len(items) > 0
			for j := range items {
				if items[j].AnchorBeforeNodeID == nil {
					allAnchored = false
					break
				}
			}
			if allAnchored {
				continue // 已逐题输出过
			}
			if len(items) > 0 {
				doc.Blocks = append(doc.Blocks, &Heading{
					Level: 2,
					Runs:  []Inline{&Text{Value: "参考答案与解析", Bold: true}},
					Align: AlignCenter,
				})
			}
			for j := range items {
				item := &items[j]
				doc.Blocks = append(doc.Blocks, assembleAnswerItem(item, in, loader, sourceQuestionNumber(item, in, numberFromProps(item.Props)))...)
			}
		}
	}

	return doc
}

// 答题区样式。取值来自节点 props.style，与参考实现一致，不要自造新词。
const (
	AnswerStyleBlank = "blank"
	AnswerStyleLined = "lined"
)

const (
	// defaultAnswerLines 未指定行数时的默认值。
	defaultAnswerLines = 4
	// maxAnswerLines 行数上限。与参考实现的 1..50 对齐，
	// 同时兜住前端被改坏时的离谱值——否则一个 nodes=100000 的节点
	// 会让一次导出写出十万个段落，导出接口直接被拖垮。
	maxAnswerLines = 50
	// legacyAnswerLineHeightPx 旧版把留白高度写在 content.height 里，
	// 按每行 30 像素折算成行数。120/30 = 4 行，与 defaultAnswerLines 一致，
	// 所以老卷子迁移过来时版面不会出现"跳了一截"。
	legacyAnswerLineHeightPx = 30
)

// answerSpaceOf 读出一个答题区块。
//
// 契约只有一个：props.lines + props.style。
//
// 曾经的坑是前端把高度写在 content.height、这里却只读 props.lines，
// 于是用户的设置被静默丢弃、永远退化成 4 个空段落。因此对没有 props.lines
// 的老节点，这里按像素折算把高度翻译回行数，而不是直接忽略。
func answerSpaceOf(node *model.PaperNode) *AnswerSpace {
	lines := attrIntFromJSON(node.Props, "lines")
	if lines <= 0 {
		lines = legacyAnswerLines(node.Content)
	}
	lines = clampAnswerLines(lines)
	return &AnswerSpace{
		Lines: lines,
		Lined: attrStringFromJSON(node.Props, "style") == AnswerStyleLined,
	}
}

// legacyAnswerLines 把旧版 content.height（像素）折算成行数。
func legacyAnswerLines(content model.JSON) int {
	height := attrFloatFromJSON(content, "height")
	if height <= 0 {
		return defaultAnswerLines
	}
	return int(math.Round(height / legacyAnswerLineHeightPx))
}

func clampAnswerLines(n int) int {
	if n < 1 {
		return 1
	}
	if n > maxAnswerLines {
		return maxAnswerLines
	}
	return n
}

// counter 题号计数器。
//
// 题号在这里计算**只是兜底**：节点 props.number 有值时以它为准（见
// questionNumber）。保留计数器是为了让"没填过题号"的老数据继续保持
// 插入删除后自动连续的行为——若连兜底都去掉，老卷子会集体丢题号。
type counter struct {
	current int
}

func newCounter() *counter { return &counter{} }

// maxQuestionNumber 节点 props.number 的长度上限，与前端一致。
// 不做上限的话，一个几百字的"题号"能把整行题干顶到下一行去。
const maxQuestionNumber = 16

// numberLabelFromJSON 读题目节点上记录的题号。
//
// 存在的原因是题号不再只能是整数：按二级标题分组时需要 "1.1" 这种十进制
// 编号，而整数表达不了。题号是教师显式填的（或前端"自动填充题号"写进去的），
// 因此导出时原样采用，不再由这里计算。
func numberLabelFromJSON(props model.JSON) string {
	label := strings.TrimSpace(attrStringFromJSON(props, "number"))
	if label == "" {
		return ""
	}
	// 按 rune 截断而不是按字节：中文题号（"一之三"之类）按字节切会切出半个字。
	runes := []rune(label)
	if len(runes) > maxQuestionNumber {
		label = string(runes[:maxQuestionNumber])
	}
	return label
}

// numberFromProps 读取解析条目上记录的题号。
func numberFromProps(props model.JSON) int {
	return attrIntFromJSON(props, "number")
}

// questionNumber 决定一道题在卷面上显示的题号。
//
// 优先级：题目节点 props.number > fallback（位置计数）。
// 前端编辑器用同一套优先级渲染题号输入框，两边必须一致，
// 否则界面上写着 1.1、导出来却是 3.。
func questionNumber(props model.JSON, fallback int) string {
	if label := numberLabelFromJSON(props); label != "" {
		return label
	}
	if fallback > 0 {
		return strconv.Itoa(fallback)
	}
	return ""
}

// sourceQuestionNumber 解析条目对应的题号。
//
// 条目自己 props.number 上记的是服务端按位置算的整数，对不上 "1.1" 这类
// 编号；真正的题号在它指向的题目块的 props.number 上。所以先回查题目块，
// 查不到才用整数序号兜底——答案页与题干页的题号必须对得上，
// 对不上老师就没法拿答案页对着答题卡讲。
func sourceQuestionNumber(node *model.PaperNode, in Input, fallback int) string {
	if node.SourceQuestionNodeID != nil {
		for i := range in.Nodes {
			n := &in.Nodes[i]
			if n.ID != *node.SourceQuestionNodeID {
				continue
			}
			if label := numberLabelFromJSON(n.Props); label != "" {
				return label
			}
			break
		}
	}
	if fallback > 0 {
		return strconv.Itoa(fallback)
	}
	return ""
}

// assembleQuestion 渲染一道题。
func assembleQuestion(node *model.PaperNode, in Input, loader ImageLoader, c *counter, includeExtras bool) []Block {
	var ref domain.QuestionRef
	if !node.Content.IsEmpty() {
		_ = node.Content.Into(&ref)
	}
	qv, ok := in.Questions[ref.QuestionID]
	if !ok {
		// 题目已被删除：明确标注而不是留空，否则导出结果会静默缺题。
		return []Block{&Paragraph{
			Runs:  []Inline{&Text{Value: "（此题目已不存在，引用失效）", Italic: true}},
			Align: AlignCenter,
		}}
	}

	c.current++
	var blocks []Block

	// 题干前置"题号 + 分值"。
	var prefix []Inline
	if in.NumberingEnabled {
		if label := questionNumber(node.Props, c.current); label != "" {
			prefix = append(prefix, &Text{Value: fmt.Sprintf("%s．", label), Bold: true})
		}
	}
	if in.ScoringEnabled {
		if score := attrFloatFromJSON(node.Props, "score"); score > 0 {
			prefix = append(prefix, &Text{Value: fmt.Sprintf("（%.4g 分）", score)})
		}
	}

	stem := RichDocBlocks(qv.Stem, loader)
	if len(prefix) > 0 {
		stem = prependRuns(stem, prefix)
	}
	blocks = append(blocks, stem...)

	// 选项。
	if len(qv.Options) > 0 {
		blocks = append(blocks, assembleOptions(qv.Options, loader)...)
	}

	// 按显示开关附上解析、思路、小结。
	if includeExtras {
		blocks = append(blocks, assembleExtras(qv, in.Display, loader)...)
	}
	return blocks
}

// appendAnswers 只输出每题答案、解析、思路与小结。
func appendAnswers(
	doc *Document,
	in Input,
	roots []model.PaperNode,
	children map[string][]model.PaperNode,
	loader ImageLoader,
) {
	itemsBySource := make(map[string]model.PaperNode)
	for _, root := range roots {
		if root.NodeType != model.NodeTypeQuestionDetails {
			continue
		}
		for _, child := range children[root.ID] {
			if child.SourceQuestionNodeID != nil {
				itemsBySource[*child.SourceQuestionNodeID] = child
			}
		}
	}

	blocks := make([]Block, 0)
	counter := newCounter()
	for i := range roots {
		root := &roots[i]
		if root.NodeType != model.NodeTypeQuestion {
			continue
		}
		counter.current++
		number := ""
		if in.NumberingEnabled {
			number = questionNumber(root.Props, counter.current)
		}
		qv, ok := questionViewOf(root, in)
		if !ok {
			continue
		}
		if item, ok := itemsBySource[root.ID]; ok {
			blocks = append(blocks, assembleAnswerItem(&item, in, loader, number)...)
		} else {
			blocks = append(blocks, assembleSyntheticAnswer(qv, number, loader)...)
		}
		blocks = append(blocks, assembleExtraFields(qv, loader)...)
	}
	if len(blocks) == 0 {
		return
	}
	doc.Blocks = append(doc.Blocks, &Heading{
		Level: 2,
		Runs:  []Inline{&Text{Value: "参考答案与解析", Bold: true}},
		Align: AlignCenter,
	})
	doc.Blocks = append(doc.Blocks, blocks...)
}

func questionViewOf(node *model.PaperNode, in Input) (QuestionView, bool) {
	var ref domain.QuestionRef
	if node.Content.IsEmpty() || node.Content.Into(&ref) != nil {
		return QuestionView{}, false
	}
	qv, ok := in.Questions[ref.QuestionID]
	return qv, ok
}

func assembleSyntheticAnswer(qv QuestionView, number string, loader ImageLoader) []Block {
	prefix := "【答案】"
	if number != "" {
		prefix = fmt.Sprintf("%s．【答案】", number)
	}
	out := make([]Block, 0, 2)
	if answer := AnswerInlines(qv); len(answer) > 0 {
		out = append(out, &Paragraph{Runs: append([]Inline{&Text{Value: prefix, Bold: true}}, answer...)})
	}
	if !qv.Analysis.IsEmpty() {
		out = append(out, prependRuns(RichDocBlocks(qv.Analysis, loader), []Inline{&Text{Value: "【解析】", Bold: true}})...)
	}
	return out
}

func assembleExtraFields(qv QuestionView, loader ImageLoader) []Block {
	var out []Block
	if !qv.Solution.IsEmpty() {
		out = append(out, prependRuns(RichDocBlocks(qv.Solution, loader), []Inline{&Text{Value: "【思路】", Bold: true}})...)
	}
	if !qv.Summary.IsEmpty() {
		out = append(out, prependRuns(RichDocBlocks(qv.Summary, loader), []Inline{&Text{Value: "【小结】", Bold: true}})...)
	}
	return out
}

// assembleOptions 渲染选项。
//
// 列数按内容长度自适应：短选项并排（省版面、便于对比），
// 长选项或含公式/图片的选项独占一行（并排会挤成乱码）。
func assembleOptions(options []model.Option, loader ImageLoader) []Block {
	cols := optionColumns(options)

	if cols <= 1 {
		var out []Block
		for i := range options {
			out = append(out, optionParagraph(&options[i], loader, 0))
		}
		return out
	}

	rows := (len(options) + cols - 1) / cols
	tbl := &Table{Borderless: true}
	// 定宽保证 A/B 两列对齐，避免长短不一导致参差。
	widths := make([]int, cols)
	for i := range widths {
		widths[i] = 1
	}
	tbl.Widths = widths

	for r := 0; r < rows; r++ {
		cells := make([]Cell, 0, cols)
		for cidx := 0; cidx < cols; cidx++ {
			idx := r*cols + cidx
			if idx >= len(options) {
				// 空位补空单元格，保持列对齐。
				cells = append(cells, Cell{Blocks: []Block{&Paragraph{}}})
				continue
			}
			cells = append(cells, Cell{Blocks: []Block{optionParagraph(&options[cidx+r*cols], loader, 0)}})
		}
		tbl.Rows = append(tbl.Rows, cells)
	}
	return []Block{tbl}
}

// optionColumns 决定选项排布列数。
func optionColumns(options []model.Option) int {
	if len(options) < 2 {
		return 1
	}
	maxLen := 0
	hasBlockContent := false
	for i := range options {
		var doc struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
			} `json:"content"`
		}
		text := plainTextOf(options[i].Content)
		if n := utf8.RuneCountInString(text); n > maxLen {
			maxLen = n
		}
		if !options[i].Content.IsEmpty() {
			_ = options[i].Content.Into(&doc)
			for _, blk := range doc.Content {
				// 选项里有表格、公式块或图片时不适合并排。
				if blk.Type == "table" || blk.Type == "blockMath" || blk.Type == "image" {
					hasBlockContent = true
				}
			}
		}
	}

	switch {
	case hasBlockContent || maxLen > 24:
		return 1
	case maxLen <= 8 && len(options) >= 4:
		return 4
	default:
		return 2
	}
}

func optionParagraph(o *model.Option, loader ImageLoader, indent float64) *Paragraph {
	runs := []Inline{&Text{Value: strings.TrimSpace(o.Label) + "．"}}
	runs = append(runs, RichDocInlines(o.Content, loader)...)
	return &Paragraph{Runs: runs, IndentLeft: indent, SpaceBefore: 1, SpaceAfter: 1}
}

// assembleExtras 按显示开关追加解析/思路/小结。
func assembleExtras(qv QuestionView, display model.QuestionDisplay, loader ImageLoader) []Block {
	var out []Block
	appendSection := func(label string, raw model.JSON) {
		if raw.IsEmpty() {
			return
		}
		blocks := RichDocBlocks(raw, loader)
		if len(blocks) == 0 {
			return
		}
		blocks = prependRuns(blocks, []Inline{&Text{Value: label, Bold: true}})
		out = append(out, blocks...)
	}

	if display.ShowAnalysis {
		appendSection("【解析】", qv.Analysis)
	}
	if display.ShowSolution {
		appendSection("【思路】", qv.Solution)
	}
	if display.ShowSummary {
		appendSection("【小结】", qv.Summary)
	}
	return out
}

// assembleAnswerItem 渲染一个解析条目。
//
// number 已经是解析好的题号文本（可能带 "1.1" 这类分组编号），
// 调用方负责按"题目块 props.number 优先、整数序号兜底"的顺序解析，
// 这里的 prefix 逻辑对两种形式一视同仁。
func assembleAnswerItem(node *model.PaperNode, in Input, loader ImageLoader, number string) []Block {
	// 解析条目优先渲染自己填的内容（教师手写的解析）。
	own := RichDocBlocks(node.Content, loader)
	if len(own) > 0 {
		prefix := "【解析】"
		if number != "" {
			prefix = fmt.Sprintf("%s．【解析】", number)
		}
		return prependRuns(own, []Inline{&Text{Value: prefix, Bold: true}})
	}

	// 否则回落到题库里的解析字段。
	var ref domain.QuestionRef
	_ = nodeContentQuestionRef(node, in, &ref)
	qv, ok := in.Questions[ref.QuestionID]
	if !ok {
		return nil
	}
	var out []Block
	prefix := "【答案】"
	if number != "" {
		prefix = fmt.Sprintf("%s．【答案】", number)
	}
	if ans := AnswerInlines(qv); len(ans) > 0 {
		out = append(out, &Paragraph{Runs: append([]Inline{&Text{Value: prefix, Bold: true}}, ans...)})
	}
	if !qv.Analysis.IsEmpty() {
		out = append(out, prependRuns(RichDocBlocks(qv.Analysis, loader), []Inline{&Text{Value: "【解析】", Bold: true}})...)
	}
	return out
}

// nodeContentQuestionRef 取解析条目对应的题目引用。
// 解析条目自身没有 question_id，需要通过 source_question_node_id 反查题目块。
func nodeContentQuestionRef(node *model.PaperNode, in Input, out *domain.QuestionRef) error {
	if node.SourceQuestionNodeID == nil {
		return fmt.Errorf("解析条目缺少来源题目节点")
	}
	for i := range in.Nodes {
		n := &in.Nodes[i]
		if n.ID != *node.SourceQuestionNodeID || n.Content.IsEmpty() {
			continue
		}
		return n.Content.Into(out)
	}
	return fmt.Errorf("找不到来源题目节点 %s", *node.SourceQuestionNodeID)
}

// AnswerInlines 把答案结构渲染为行内元素。
//
// 不同题型的答案表示完全不同，这里统一转成"人看得懂"的形式：
// 选择题给选项标签、判断题给对/错、填空题给可接受答案、解答题给参考答案。
func AnswerInlines(qv QuestionView) []Inline {
	switch qv.Answer.Kind {
	case "single", "multiple":
		labels := optionLabels(qv.Options, qv.Answer.Correct)
		if len(labels) == 0 {
			return nil
		}
		sep := ""
		if qv.Answer.Kind == "multiple" {
			sep = "、"
		}
		return []Inline{&Text{Value: strings.Join(labels, sep), Bold: true}}

	case "boolean":
		if qv.Answer.BoolAnswer == nil {
			return nil
		}
		if *qv.Answer.BoolAnswer {
			return []Inline{&Text{Value: "正确", Bold: true}}
		}
		return []Inline{&Text{Value: "错误", Bold: true}}

	case "blanks":
		var parts []string
		for _, b := range qv.Answer.Blanks {
			var alts []string
			for _, group := range b.Accepts {
				var ands []string
				for _, item := range group {
					if txt := plainTextOf(item); txt != "" {
						ands = append(ands, txt)
					}
				}
				if len(ands) > 0 {
					alts = append(alts, strings.Join(ands, " "))
				}
			}
			if len(alts) > 0 {
				parts = append(parts, strings.Join(alts, " 或 "))
			}
		}
		if len(parts) == 0 {
			return nil
		}
		return []Inline{&Text{Value: strings.Join(parts, "；"), Bold: true}}

	case "reference":
		return RichDocInlines(qv.Answer.Reference, nil)
	}
	return nil
}

// optionLabels 把选项 id 映射回标签（A/B/C），并保持原始顺序。
func optionLabels(options []model.Option, ids []string) []string {
	byID := make(map[string]string, len(options))
	order := make(map[string]int, len(options))
	for i := range options {
		byID[options[i].ID] = options[i].Label
		order[options[i].ID] = i
	}

	type pair struct {
		label string
		idx   int
	}
	var picked []pair
	for _, id := range ids {
		if label, ok := byID[id]; ok {
			picked = append(picked, pair{label: label, idx: order[id]})
		}
	}
	// 按选项在卷面上的顺序输出，而不是答案里给出的顺序。
	for i := 0; i < len(picked); i++ {
		for j := i + 1; j < len(picked); j++ {
			if picked[j].idx < picked[i].idx {
				picked[i], picked[j] = picked[j], picked[i]
			}
		}
	}
	out := make([]string, 0, len(picked))
	for _, p := range picked {
		out = append(out, p.label)
	}
	return out
}

// prependRuns 把行内元素插到第一个段落/标题的最前面。
//
// 目的：让"1．（10 分）"与题干第一行连排，而不是单独占一行。
func prependRuns(blocks []Block, prefix []Inline) []Block {
	for i := range blocks {
		switch n := blocks[i].(type) {
		case *Paragraph:
			n.Runs = append(append([]Inline{}, prefix...), n.Runs...)
			return blocks
		case *Heading:
			n.Runs = append(append([]Inline{}, prefix...), n.Runs...)
			return blocks
		}
	}
	// 没有任何可插入的块（题干为空）：单独成段，避免前缀丢失。
	return append([]Block{&Paragraph{Runs: prefix}}, blocks...)
}

func sortByPosition(nodes []model.PaperNode) {
	for i := 0; i < len(nodes); i++ {
		for j := i + 1; j < len(nodes); j++ {
			if nodes[j].Position < nodes[i].Position {
				nodes[i], nodes[j] = nodes[j], nodes[i]
			}
		}
	}
}

// plainTextOf 提取富文本的纯文本。
func plainTextOf(raw model.JSON) string {
	if raw.IsEmpty() {
		return ""
	}
	var root rdNode
	if err := raw.Into(&root); err != nil {
		return ""
	}
	return plainOf(root.Content)
}

// RichDocInlines 把富文本压平成行内元素（用于标题、选项等单行场景）。
func RichDocInlines(raw model.JSON, loader ImageLoader) []Inline {
	if raw.IsEmpty() {
		return nil
	}
	var root rdNode
	if err := raw.Into(&root); err != nil {
		return nil
	}
	var out []Inline
	for i := range root.Content {
		out = append(out, inlinesOf(root.Content[i].Content, loader, 0)...)
	}
	return out
}

// attrFloatFromJSON 从 JSON 属性里取数值。
func attrFloatFromJSON(raw model.JSON, key string) float64 {
	if raw.IsEmpty() {
		return 0
	}
	var m map[string]any
	if err := raw.Into(&m); err != nil {
		return 0
	}
	return attrFloat(m, key)
}

// attrIntFromJSON 从 JSON 属性里取整数。
func attrIntFromJSON(raw model.JSON, key string) int {
	return int(attrFloatFromJSON(raw, key))
}

// attrStringFromJSON 从 JSON 属性里取字符串。
func attrStringFromJSON(raw model.JSON, key string) string {
	if raw.IsEmpty() {
		return ""
	}
	var m map[string]any
	if err := raw.Into(&m); err != nil {
		return ""
	}
	return attrString(m, key)
}
