// 本文件实现**确定性（无 AI）**的标签解析器。
//
// 为什么必须有它：模型路径意味着"没配 AI 就一道题也录不进来"，
// 而且一次导入要等 30 秒到 3 分钟。对已经按标签整理过的材料
// （老师自己维护的题库、本项目导出的题库再导入），一套规则就够了：
// 可复现、零成本、不依赖网络，AI 挂了也不影响。
//
// 两条解析策略，按文档实际长相二选一：
//   - 标签模式：文档里出现【题目】/【试题】/【题干】时启用，切题边界由标签决定；
//   - 题号模式：没有任何题目标签时启用，按「1.」「(1)」「一、」等行首题号切题。
//
// 绝不因为"看不懂"就丢内容：解析不了的部分记进该题的 warnings，
// 交给审核界面的用户去定，而不是悄悄少录一道题。
//
// 输出的 Draft 与模型路径完全同形（共用 normalizeOne），
// 因此审核界面、知识点建议、入库流程都不需要区分来源。
package importing

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/NanamiKyoka/questionmind/internal/ai"
)

// ---- 抽取方式 ----

// Method 抽取方式：模型 or 规则模板。
type Method string

const (
	// MethodAI 调用模型抽取。默认方式，行为与加这个特性之前完全一致。
	MethodAI Method = "ai"
	// MethodTemplate 走确定性标签解析器，**不需要任何 AI 配置**。
	MethodTemplate Method = "template"
)

// Valid 是否为受支持的取值。
func (m Method) Valid() bool { return m == MethodAI || m == MethodTemplate }

// ParseMethod 解析调用方指定的抽取方式。
//
// 留空返回 MethodAI：老前端不带这个字段，行为必须与从前完全一致。
// 第二个返回值表示取值是否合法，非法时由 service 层报明确的参数错误。
func ParseMethod(s string) (Method, bool) {
	switch m := Method(strings.ToLower(strings.TrimSpace(s))); m {
	case "":
		return MethodAI, true
	case MethodAI, MethodTemplate:
		return m, true
	default:
		return MethodAI, false
	}
}

// ---- 标签表 ----

// fieldTag 标签归并后的字段。未知标签一律按正文处理，
// 宁可把【考点】当普通文字留在题干里，也不要凭空造出一个字段。
type fieldTag string

const (
	fieldContent    fieldTag = "content"  // 题干（同时是切题边界）
	fieldQType      fieldTag = "q_type"   // 题型
	fieldOptions    fieldTag = "options"  // 选项（成块出现的形式）
	fieldAnswer     fieldTag = "answer"   // 答案
	fieldAnalysis   fieldTag = "analysis" // 解析
	fieldThinking   fieldTag = "thinking" // 思路
	fieldSummary    fieldTag = "summary"  // 小结
	fieldDifficulty fieldTag = "difficulty"
	fieldKnowledge  fieldTag = "knowledge" // 知识点
)

// tagAliases 标签别名 → 字段。
//
// 用白名单而不是"凡是【…】都当标签"：中文试卷里【考点】【超纲】
// 这类括注非常多，放进来会让题目的字段边界变得不可预测。
var tagAliases = map[string]fieldTag{
	"题目": fieldContent, "试题": fieldContent, "题干": fieldContent,
	"question": fieldContent, "title": fieldContent,

	"题型": fieldQType, "类型": fieldQType, "type": fieldQType,

	"选项": fieldOptions, "options": fieldOptions,

	"答案": fieldAnswer, "参考答案": fieldAnswer, "answer": fieldAnswer,

	"解析": fieldAnalysis, "详解": fieldAnalysis, "解答": fieldAnalysis, "analysis": fieldAnalysis,
	"思路": fieldThinking, "分析": fieldThinking, "thinking": fieldThinking,

	"小结": fieldSummary, "总结": fieldSummary, "summary": fieldSummary,

	"难度": fieldDifficulty, "difficulty": fieldDifficulty,

	"知识点": fieldKnowledge, "knowledge_point": fieldKnowledge,
}

// sectionAliases 文档级分区标签。
//
// 与逐题字段标签分属**不同命名空间**：它们只可能出现在题块之外，
// 标记之后的内容是「全卷统一答案区」，按题号回填而不是塞给某一道题。
var sectionAliases = map[string]bool{
	"答案区": true, "答案表": true, "统一答案": true, "答案速查": true, "答案汇总": true,
}

// answerHeadings 文末答案区的**无标签**写法。
//
// 中文试卷极少写【答案区】，绝大多数是光秃秃一行"参考答案"。
// 只有在已经切出至少一道题之后出现的这些标题才被认定为答案区——
// 否则正文里一句"答案"也会把后面半张卷子吞进答案区。
var answerHeadings = map[string]bool{
	"答案": true, "参考答案": true, "答案速查": true, "统一答案": true, "答案区": true,
	"答案与解析": true, "答案及解析": true, "参考答案与解析": true, "答案和解析": true,
	"参考答案表": true, "答案汇总": true, "评分标准与答案": true,
}

// tagLinePattern 行首标签：可选题号 + 【标签】或 [标签] + 可选冒号 + 行内内容。
//
// 题号是**可选**的：带标签的卷子未必每题都编号（"【题目】……"很常见），
// 题号写死成必需会让这类卷子一题都切不出来。
var tagLinePattern = regexp.MustCompile(`^\s*(?:\*\*)?[（(]?(\d+(?:[.．]\d+)?)?[)）]?\s*[.、．]?\s*` +
	`[【\[]\s*([^】\]]+?)\s*[】\]]\s*[:：]?\s*(.*?)\s*$`)

// blockOpenerPrefixPattern 题号前缀：空、"1."、"3、"、"（2）" 这类。
//
// 用来判断"这个标签前面是不是只有一个题号"——是的话就不能在标签前切行。
var blockOpenerPrefixPattern = regexp.MustCompile(`^[\s（(]*\d+(?:[.．、)）])?[\s、．.]*$`)

// optionTagPattern 逐个选项的标签，如【选项A】。
var optionTagPattern = regexp.MustCompile(`^(?:选项|选项\(|option|opt)\s*([A-Za-z])$`)

// headingMarkupPattern 去掉 Markdown 标题记号与加粗星号，只留可读标题文字。
var headingMarkupPattern = regexp.MustCompile(`^[\s#*>]+|[\s*：:]+$`)

// cnNumPattern 中文数字（仅用于识别"一、"这种题号，不做数值换算）。
var cnNumPattern = regexp.MustCompile(`[一二三四五六七八九十百零]`)

// ParseTemplate 用规则解析带标签（或带题号）的文本，返回与模型路径同形的草稿。
//
// sourceFile 是这份内容来自哪个文件，会写进每道题的 SourceFile，
// 让界面能把一次多文件导入按来源分组。
//
// 第二个返回值是**整篇文档级**的提示（不是某一道题的问题），
// 例如"文末答案区有 5 道题找不到对应题目"。它只记日志，不污染单题 warnings。
func ParseTemplate(text, sourceFile string) ([]Draft, []string) {
	lines := preprocessLines(text)

	blocks, section, docWarn := scanDocument(lines)

	answers, analyses := parseAnswerSection(section)

	drafts := buildDrafts(blocks, answers, analyses)

	// 补一句"这些题是从哪个文件来的"：多文件导入时界面要靠它分组。
	for i := range drafts {
		drafts[i].SourceFile = sourceFile
	}
	return drafts, docWarn
}

// preprocessLines 规范化行：统一换行、去掉引用前缀、
// 把挤在一行里的多个标签拆到各自独立的行上。
//
// 拆行是必须的：中文试卷极常见 "……正确的是（ ）。【答案】B【解析】因为……"，
// 不拆的话后半段会被当成题干正文，答案与解析全部丢失。
func preprocessLines(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")

	var out []string
	for _, line := range strings.Split(text, "\n") {
		line = blockquotePrefix.ReplaceAllString(line, "")
		for _, part := range splitInlineTags(line) {
			part = strings.TrimRight(part, " \t")
			if strings.TrimSpace(part) == "" {
				continue
			}
			out = append(out, part)
		}
	}
	return out
}

var blockquotePrefix = regexp.MustCompile(`^\s*(?:>\s*)+`)

// splitInlineTags 在字段标签**前**切行，让标签与它后面的内容留在同一行。
//
// 切点只有这一个方向，原因有两个，方向相反：
//   - 【答案】B 绝不能拆成 "【答案】" 和 "B"：答案标签一空，
//     后面那个 B 就变成没有标签的正文行，答案直接丢失；
//   - 但 "1.【题目】题干" 也不能把题号和【题目】分开——
//     tagLinePattern 要求两者相邻，拆开后 "1." 与 "【题目】" 各自都匹配不上，
//     整份卷子会解析出 0 道题。
//
// 所以：首个切题标签若前面只剩一个题号，就在它**之后**继续找，
// 把 "1.【题目】题干" 整段留在一行里。
func splitInlineTags(line string) []string {
	type cut struct{ idx, end int }

	var cuts []cut
	for pos := 0; ; {
		i, e := earliestInlineTag(line[pos:])
		if i < 0 {
			break
		}
		cuts = append(cuts, cut{pos + i, pos + e})
		pos = pos + e
	}
	if len(cuts) == 0 {
		return []string{line}
	}

	var out []string
	prev := 0
	for k, c := range cuts {
		// 切题标签与题号同行：跳过这个切点，prev 保持不动，
		// 下一个切点自然会把 "1.【题目】题干" 整段取出来。
		if k == 0 && blockOpenerPrefixPattern.MatchString(line[:c.idx]) {
			continue
		}
		if strings.TrimSpace(line[prev:c.idx]) != "" {
			out = append(out, line[prev:c.idx])
		}
		prev = c.idx
	}
	if rest := line[prev:]; strings.TrimSpace(rest) != "" {
		out = append(out, rest)
	}
	return out
}

// earliestInlineTag 找出行内第一个**字段标签**（不含切题标签）的起止位置。
// 返回的 end 是开区间；idx < 0 表示这一行没有字段标签。
func earliestInlineTag(line string) (idx, end int) {
	for i := 0; i < len(line); i++ {
		if line[i] != '[' && !strings.HasPrefix(line[i:], "【") {
			continue
		}
		open, closeCh := "【", "】"
		if line[i] == '[' {
			open, closeCh = "[", "]"
		}
		j := strings.Index(line[i+len(open):], closeCh)
		if j < 0 {
			continue
		}
		name := strings.TrimSpace(line[i+len(open) : i+len(open)+j])
		if _, ok := tagAliases[strings.ToLower(name)]; !ok {
			continue
		}
		return i, i + len(open) + j + len(closeCh)
	}
	return -1, -1
}

// ---- 扫描 ----

// rawBlock 一道题（一个题块）的原始行。
type rawBlock struct {
	lines []string
	// number 切题时捕获的题号：标签前题号（"3.【题目】"）或题号模式下的行首题号。
	number int
	// tagged 是否由【题目】类标签切出。
	tagged bool
}

// scanDocument 把整篇文本切成题块 + 文末答案区。
func scanDocument(lines []string) (blocks []rawBlock, answerSection []string, docWarn []string) {
	tagged := hasContentTag(lines)

	var cur *rawBlock
	// orphan 记下当前没有题块时经过的行：既用于兜底，也用于说明
	// 为什么开头的几行没有被当成题目。
	var orphan []string
	flush := func() {
		if cur != nil {
			blocks = append(blocks, *cur)
			cur = nil
		}
	}

	for i := 0; i < len(lines); i++ {
		line := lines[i]

		// 1) 题目标签：切题边界。
		if f, _, num, ok := matchTagLine(line); ok && f == fieldContent {
			flush()
			cur = &rawBlock{lines: []string{line}, number: num, tagged: true}
			continue
		}

		// 2) 答案区（两种写法）。
		// seen 必须是"已经切出至少一道题"，而 blocks 只在 flush 时增长——
		// 正在写的题块同样算数，否则答案区标题会被吞进最后一道题里。
		seen := len(blocks) > 0 || cur != nil
		if section, ok := matchAnswerSection(lines, i, seen); ok {
			flush()
			return blocks, section, docWarn
		}

		// 3) 大题标题：是版面结构，不是题目，结束当前题块。
		if isSectionHeading(line) {
			flush()
			continue
		}

		// 4) 无标签模式：按行首题号切题。
		if !tagged {
			if num, ok := matchQuestionOpener(line); ok {
				flush()
				cur = &rawBlock{lines: []string{line}, number: num}
				continue
			}
		}

		if cur != nil {
			cur.lines = append(cur.lines, line)
		} else {
			orphan = append(orphan, line)
		}
		// 题块之外的散文（封面、说明、注意事项）不产生题目，也不报错：
		// 试卷开头那几行说明文字本来就不是题。
	}
	flush()

	// 无标签、且一个切题点都没找到时的兜底：整段当作一道题。
	//
	// 触发场景是 "2.5 mol 的 NaOH 溶液浓度是多少" 这类卷子——行首的数字是
	// 数值不是题号，切题点一个也认不出来，于是整份卷子一道题都不产出。
	// 一道题都不产出比"切得不准"糟糕得多：用户看到的是空白加一句
	// 「没有解析出题目」，完全无从下手。把整段当一题交给用户去改，
	// 至少还有个可编辑的起点（且会带上"仅识别到题干"之类的提示）。
	//
	// 只在**一个题块都没切出来**时启用；已经切出过题块时，开头那几行
	// 就确实是卷头说明，不该硬凑成题目。
	if !tagged && len(blocks) == 0 && len(orphan) > 0 {
		blocks = append(blocks, rawBlock{lines: orphan})
	}
	return blocks, nil, docWarn
}

// hasContentTag 判断文档是否使用了题目标签。
func hasContentTag(lines []string) bool {
	for _, line := range lines {
		if f, _, _, ok := matchTagLine(line); ok && f == fieldContent {
			return true
		}
	}
	return false
}

// matchTagLine 识别行首标签。返回字段、行内内容、标签前的题号、是否命中。
func matchTagLine(line string) (fieldTag, string, int, bool) {
	m := tagLinePattern.FindStringSubmatch(line)
	if m == nil {
		return "", "", 0, false
	}
	name := strings.TrimSpace(m[2])
	if _, ok := tagAliases[strings.ToLower(name)]; !ok {
		return "", "", 0, false
	}
	return tagAliases[strings.ToLower(name)], m[3], leadingInt(m[1]), true
}

// matchAnswerSection 判断第 i 行是否开启文末答案区。
//
// 返回的切片是答案区的全部行；ok=false 表示这里不是答案区。
//
// 只有在已经切出至少一道题之后才可能成立：否则卷子开头一句
// "答案见文末"就会把整份卷子当成答案区，最后一道题都录不出来。
func matchAnswerSection(lines []string, i int, seenQuestion bool) (section []string, ok bool) {
	if !seenQuestion {
		return nil, false
	}
	line := lines[i]

	// 写法一：【答案区】/【答案速查】等分区标签。标签后面若还跟着内容，
	// 那一段也算答案区的第一行（"【答案区】1.B 2.C" 这种紧凑写法）。
	if _, end, hit := matchSectionTag(line); hit {
		section = make([]string, 0, len(lines)-i)
		if tail := strings.TrimSpace(line[end:]); tail != "" {
			section = append(section, tail)
		}
		return append(section, lines[i+1:]...), true
	}

	// 写法二：光秃秃一行"参考答案"。
	if answerHeadings[headingMarkupPattern.ReplaceAllString(strings.TrimSpace(line), "")] {
		return append([]string{}, lines[i+1:]...), true
	}
	return nil, false
}

// matchSectionTag 在行内找分区标签，返回标签起点与终点（终点为开区间）。
func matchSectionTag(line string) (start, end int, ok bool) {
	for idx := 0; idx < len(line); idx++ {
		if line[idx] != '[' && !strings.HasPrefix(line[idx:], "【") {
			continue
		}
		open, closeCh := "【", "】"
		if line[idx] == '[' {
			open, closeCh = "[", "]"
		}
		j := strings.Index(line[idx+len(open):], closeCh)
		if j < 0 {
			return 0, 0, false
		}
		if sectionAliases[strings.TrimSpace(line[idx+len(open):idx+len(open)+j])] {
			return idx, idx + len(open) + j + len(closeCh), true
		}
	}
	return 0, 0, false
}

// ---- 题号与标题 ----

// numberPrefixPattern 行首题号：1. / 1、/ (1) / （1）/ 3.1
var numberPrefixPattern = regexp.MustCompile(`^\s*[（(]?(\d+(?:[.．]\d+)?)[)）]?\s*[.、．]\s*`)

// splitLeadingNumber 拆出行首题号，返回题号、剩余正文与是否命中。
//
// 必须挡掉"数字后紧跟数字"的情况：「2.5 mol 的溶液」以题号的形式开头，
// 却是一道化学题的开头。误判的后果是把一道题劈成两半，
// 而界面只看到"多了一道题、题干少了一半"，根本猜不到是这个原因。
func splitLeadingNumber(line string) (int, string, bool) {
	m := numberPrefixPattern.FindStringSubmatchIndex(line)
	if m == nil {
		return 0, line, false
	}
	rest := line[m[1]:]
	if strings.TrimSpace(rest) == "" {
		return 0, line, false
	}
	r := []rune(rest)[0]
	if r >= '0' && r <= '9' {
		return 0, line, false
	}
	return leadingInt(line[m[2]:m[3]]), rest, true
}

// cnQuestionOpenerPattern 中文数字题号：一、/ 二．
var cnQuestionOpenerPattern = regexp.MustCompile(`^\s*([一二三四五六七八九十百零]{1,3})\s*[、．.]\s*\S`)

// sectionHeadingPattern 大题标题：序号 + 题型词 + 题，如「一、选择题」「二、填空题」。
var sectionHeadingPattern = regexp.MustCompile(
	`^\s*(?:#{1,6}\s*)?(?:[一二三四五六七八九十百]+|\d+)\s*[、\.．]\s*` +
		`[^\n]{0,12}?(?:单选|多选|选择|填空|判断|解答|简答|计算|综合|论述|作文|阅读|改错|` +
		`完形|应用|实验|作图|说理|案例|简记|默写|翻译|听力)[^\n]{0,8}?题`)

// scorePattern 分值/考试说明：出现在带序号的行里时说明它是版面说明而不是题目。
var scorePattern = regexp.MustCompile(`每\s*(?:题|小题|空|问)\s*\d+\s*分|共\s*\d+\s*分|满分|考试时间|满分：|时间：\s*\d+`)

// isSectionHeading 判断该行是否是大题标题（应从题目流里剔除）。
func isSectionHeading(line string) bool {
	t := strings.TrimSpace(line)
	if t == "" {
		return false
	}
	if sectionHeadingPattern.MatchString(t) {
		return true
	}
	// 「一、」（后面没有题型词）但整行在讲分值/考试要求 —— 同样是版面说明。
	if cnQuestionOpenerPattern.MatchString(t) {
		return scorePattern.MatchString(t)
	}
	if _, _, ok := splitLeadingNumber(t); ok {
		return scorePattern.MatchString(t)
	}
	return false
}

// matchQuestionOpener 识别无标签模式下的切题题号。
func matchQuestionOpener(line string) (int, bool) {
	t := strings.TrimSpace(line)
	if t == "" || isSectionHeading(t) {
		return 0, false
	}
	if n, _, ok := splitLeadingNumber(t); ok {
		return n, true
	}
	if m := cnQuestionOpenerPattern.FindStringSubmatch(t); m != nil {
		// 中文数字题号无法可靠换算成 int，用 0 表示"有编号但取不到值"：
		// 题干里的题号仍然原样保留，界面上也能看到。
		_ = cnNumPattern.MatchString(m[1])
		return 0, true
	}
	return 0, false
}

// leadingInt 把捕获到的题号取成整数。
//
// 「3.1」这类子题号只取整数部分：Draft.SourceNumber 是 int，
// 强行塞 31 反而会造出一个原卷上不存在的题号。
func leadingInt(s string) int {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, ".．"); i > 0 {
		s = s[:i]
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

// stemLeadingNumberPattern 题干正文开头的题号（"【题目】3. 下列……"）。
// captureSourceNumber 取题干正文开头的题号。
func captureSourceNumber(stem string) int {
	n, _, ok := splitLeadingNumber(stem)
	if !ok {
		return 0
	}
	return n
}

// ---- 题块 → 草稿 ----

// blockFields 一个题块里各字段的原始文本。
type blockFields struct {
	content    []string
	answer     []string
	analysis   []string
	thinking   []string
	summary    []string
	difficulty []string
	qType      []string
	knowledge  []string
	options    []string
	// optionItems 逐个选项的标签形式，优先级高于 options 整块。
	optionItems map[string][]string
}

func newBlockFields() *blockFields {
	return &blockFields{optionItems: map[string][]string{}}
}

// buildDrafts 把题块转成 Draft。
func buildDrafts(blocks []rawBlock, answers, analyses map[int]string) []Draft {
	extracted := make([]ai.ExtractedQuestion, 0, len(blocks))
	numbers := make([]int, 0, len(blocks))

	// 整篇都没有题号时，答案区里的「1 | A」就只能按位置对应。
	// 判据是"全篇都没编号"而不是"这一题没编号"：一份卷子若部分带号，
	// 给没号的那几道题硬套位置号，多半会把答案安到别的题头上——
	// 宁可留空让用户自己核，也不能安错。
	anyNumber := false
	for i := range blocks {
		if blocks[i].number > 0 {
			anyNumber = true
			break
		}
	}

	for i := range blocks {
		f := collectFields(&blocks[i])
		eq := f.toExtracted()
		num := blocks[i].number
		if num == 0 {
			num = captureSourceNumber(eq.Stem)
		}
		if num == 0 && !anyNumber {
			num = i + 1
		}
		numbers = append(numbers, num)
		extracted = append(extracted, eq)
	}

	// 答案区回填必须在归一化**之前**：题型是靠"选项 + 答案"推断的，
	// 先归一化再补答案就会得到"有选项但没答案"的自相矛盾结果。
	for i := range extracted {
		n := numbers[i]
		if n == 0 {
			continue
		}
		if strings.TrimSpace(extracted[i].Answer) == "" {
			if a, ok := answers[n]; ok {
				extracted[i].Answer = a
			}
		}
		if strings.TrimSpace(extracted[i].Analysis) == "" {
			if a, ok := analyses[n]; ok {
				extracted[i].Analysis = a
			}
		}
	}

	drafts := Normalize(extracted)
	for i := range drafts {
		// 题块上捕获的题号优先于题干里的：标签前的「3.【题目】」是原卷题号，
		// 而题干正文里那个 3 有可能是小问编号。
		if numbers[i] > 0 {
			drafts[i].SourceNumber = numbers[i]
		}
		// 模板解析特有的检查放在最前面：它描述的是"这道题缺了什么"，
		// 比归一化阶段的"答案没识别出来"更需要用户先看一眼。
		if w := templateWarnings(&extracted[i]); len(w) > 0 {
			drafts[i].Warnings = append(w, drafts[i].Warnings...)
		}
	}
	return drafts
}

// collectFields 把题块里的行分配到各字段。
func collectFields(b *rawBlock) *blockFields {
	f := newBlockFields()

	// 无标签模式：靠行首选项标记切出选项块，其余归题干。
	if !b.tagged {
		for _, line := range b.lines {
			if optionLinePattern.MatchString(line) {
				f.options = append(f.options, line)
				continue
			}
			if len(f.options) > 0 {
				// 选项块之后的「答案：」「【解析】」是本题的答案与解析。
				// 只在这个位置生效：题干里出现"答案是"是常事，
				// 无差别地吞掉会把半句题干变成答案。
				if rest, kind := matchLooseAnswerLine(line); kind != "" {
					if kind == fieldAnswer {
						f.answer = append(f.answer, rest)
					} else {
						f.analysis = append(f.analysis, rest)
					}
					continue
				}
			}
			f.content = append(f.content, line)
		}
		return f
	}

	var cur fieldTag
	var curOpt string
	for _, line := range b.lines {
		if letter := matchOptionTag(line); letter != "" {
			cur = fieldOptions
			curOpt = letter
			if _, ok := f.optionItems[letter]; !ok {
				f.optionItems[letter] = nil
			}
			if rest := optionTagRest(line); rest != "" {
				f.optionItems[letter] = append(f.optionItems[letter], rest)
			}
			continue
		}
		if fld, inline, _, ok := matchTagLine(line); ok {
			cur = fld
			curOpt = ""
			if strings.TrimSpace(inline) != "" {
				f.append(fld, inline)
			}
			continue
		}
		if cur == fieldOptions && curOpt != "" {
			// 【选项A】后面另起一行的续行仍属于 A。
			f.optionItems[curOpt] = append(f.optionItems[curOpt], line)
			continue
		}
		if cur == fieldOptions {
			// 选项块里出现非选项行，说明选项已经结束，后面又回到题干
			//（多行选项的排版里并不罕见）。退回题干，别把它当成选项吞掉。
			if !optionLinePattern.MatchString(line) {
				cur = fieldContent
			} else {
				f.options = append(f.options, line)
				continue
			}
		}
		// 带标签的卷子里，选项也常常是裸行：
		//
		//	【题目】下列说法正确的是
		//	A. 甲
		//	B. 乙
		//	【答案】A
		//
		// 没有【选项】标签时这些行会一路落进题干，最后变成
		// 「题干 A. 甲 B. 乙」——选项凭空消失，题型也退化成问答题。
		// 见到符合选项格式的行就切进选项块；一旦又出现非选项行，
		// 退回题干状态，这样"选项之后还有续行"也不会被误吞。
		if cur == fieldContent && optionLinePattern.MatchString(line) {
			cur = fieldOptions
			f.options = append(f.options, line)
			continue
		}
		if cur != "" {
			f.append(cur, line)
			continue
		}
		// 题块内第一个标签之前的行：仍然算题干（例如「1.【题目】」之前的内容）。
		f.content = append(f.content, line)
	}
	return f
}

func (f *blockFields) append(field fieldTag, line string) {
	switch field {
	case fieldContent:
		f.content = append(f.content, line)
	case fieldAnswer:
		f.answer = append(f.answer, line)
	case fieldAnalysis:
		f.analysis = append(f.analysis, line)
	case fieldThinking:
		f.thinking = append(f.thinking, line)
	case fieldSummary:
		f.summary = append(f.summary, line)
	case fieldDifficulty:
		f.difficulty = append(f.difficulty, line)
	case fieldQType:
		f.qType = append(f.qType, line)
	case fieldKnowledge:
		f.knowledge = append(f.knowledge, line)
	case fieldOptions:
		f.options = append(f.options, line)
	}
}

// optionLinePattern 行首选项标记，如 "A. "、"B、"、"C）"。
var optionLinePattern = regexp.MustCompile(`^\s*([A-Ha-h])\s*[.、．)）]\s*`)

// looseAnswerPrefixes 无标签模式下的答案/解析行前缀。
var looseAnswerPrefixes = []struct {
	prefix string
	kind   fieldTag
}{
	{"【答案】", fieldAnswer}, {"[答案]", fieldAnswer}, {"答案：", fieldAnswer}, {"答案:", fieldAnswer},
	{"【解析】", fieldAnalysis}, {"【详解】", fieldAnalysis}, {"[解析]", fieldAnalysis},
	{"解析：", fieldAnalysis}, {"解析:", fieldAnalysis}, {"详解：", fieldAnalysis}, {"详解:", fieldAnalysis},
}

// matchLooseAnswerLine 识别「答案：B」「【解析】因为…」这类行，返回正文与字段。
func matchLooseAnswerLine(line string) (string, fieldTag) {
	t := strings.TrimSpace(line)
	for _, p := range looseAnswerPrefixes {
		if strings.HasPrefix(t, p.prefix) {
			return strings.TrimSpace(strings.TrimPrefix(t, p.prefix)), p.kind
		}
	}
	return "", ""
}

// matchOptionTag 匹配【选项A】这类逐选项标签，返回大写字母。
func matchOptionTag(line string) string {
	m := tagLinePattern.FindStringSubmatch(line)
	if m == nil {
		return ""
	}
	name := strings.TrimSpace(m[2])
	om := optionTagPattern.FindStringSubmatch(name)
	if om == nil {
		return ""
	}
	return strings.ToUpper(om[1])
}

// optionTagRest 取【选项A】后面的行内内容。
func optionTagRest(line string) string {
	m := tagLinePattern.FindStringSubmatch(line)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[3])
}

// inlineOptionPattern 整块选项里的行内标记，如 "A. 甲 B. 乙 C. 丙"。
var inlineOptionPattern = regexp.MustCompile(`(?:^|\s)([A-Za-z])\s*[.、．)）]\s*`)

// toExtracted 把字段转成与模型输出同形的中间结构。
func (f *blockFields) toExtracted() ai.ExtractedQuestion {
	eq := ai.ExtractedQuestion{
		Stem:     joinLines(f.content),
		Answer:   joinLines(f.answer),
		Analysis: joinLines(f.analysis),
	}
	if s := joinLines(f.summary); s != "" {
		eq.Analysis = appendIfEmpty(eq.Analysis, s)
	}
	if s := joinLines(f.thinking); s != "" {
		eq.Analysis = appendIfEmpty(eq.Analysis, s)
	}
	if t := joinLines(f.qType); t != "" {
		eq.QuestionType = firstLine(t)
	}
	eq.Difficulty = parseDifficulty(joinLines(f.difficulty))
	if kp := joinLines(f.knowledge); kp != "" {
		eq.KnowledgePoints = splitList(kp)
	}
	eq.Options = f.buildOptions()
	return eq
}

// buildOptions 组装选项：逐选项标签优先于整块【选项】。
func (f *blockFields) buildOptions() []ai.ExtractedOption {
	if len(f.optionItems) > 0 {
		letters := make([]string, 0, len(f.optionItems))
		for k := range f.optionItems {
			letters = append(letters, k)
		}
		sortStrings(letters)
		out := make([]ai.ExtractedOption, 0, len(letters))
		for _, l := range letters {
			text := joinLines(f.optionItems[l])
			if text == "" {
				continue
			}
			out = append(out, ai.ExtractedOption{Label: l, Content: text})
		}
		if len(out) >= 2 {
			return out
		}
	}

	block := joinLines(f.options)
	if strings.TrimSpace(block) == "" {
		return nil
	}
	opts := splitOptionBlock(block)
	if len(opts) < 2 {
		// 只有一个"选项"多半是把正文里的 "A." 误认了，交给归一化阶段处理更安全。
		return nil
	}
	return opts
}

func appendIfEmpty(cur, extra string) string {
	if strings.TrimSpace(cur) != "" {
		return cur
	}
	return extra
}

func joinLines(lines []string) string {
	parts := make([]string, 0, len(lines))
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if t == "" {
			continue
		}
		parts = append(parts, t)
	}
	return strings.Join(parts, "\n")
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

// splitOptionBlock 把一整块选项文本切成多个选项。
func splitOptionBlock(text string) []ai.ExtractedOption {
	locs := inlineOptionPattern.FindAllStringSubmatchIndex(text, -1)
	if len(locs) < 2 {
		return nil
	}
	out := make([]ai.ExtractedOption, 0, len(locs))
	for i, loc := range locs {
		letter := strings.ToUpper(text[loc[2]:loc[3]])
		if i > 0 {
			prev := strings.ToUpper(text[locs[i-1][2]:locs[i-1][3]])
			// 标签必须递增（A→B→C）：正文中随手一个 "B." 就能骗过单点匹配。
			if prev >= letter {
				continue
			}
		}
		end := len(text)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		content := strings.TrimSpace(text[loc[1]:end])
		if content == "" {
			continue
		}
		out = append(out, ai.ExtractedOption{Label: letter, Content: content})
	}
	return out
}

// cnDifficulty 中文难度词 → 1..5。
var cnDifficulty = map[string]int{
	"容易": 1, "简单": 1, "很简单": 1,
	"较易": 2, "容易题": 2, "偏易": 2,
	"中等": 3, "普通": 3, "一般": 3, "适中": 3,
	"较难": 4, "偏难": 4, "中等偏难": 4,
	"困难": 5, "难": 5, "很难": 5, "较难以上": 5,
}

// parseDifficulty 解析难度：优先取数字（"难度：3"），否则查中文词表。
func parseDifficulty(raw string) int {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			continue
		}
		j := i
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		n, err := strconv.Atoi(s[i:j])
		if err == nil && n >= 1 && n <= 5 {
			return n
		}
		i = j
	}
	if n, ok := cnDifficulty[s]; ok {
		return n
	}
	return 0
}

var listSep = regexp.MustCompile(`[、，,;；/｜]`)

func splitList(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range listSep.Split(s, -1) {
		p = strings.TrimSpace(strings.Trim(strings.TrimSpace(p), "：:"))
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// sortStrings 极小升序排序：选项字母最多 8 个，不值得为它引 sort 包再分配一次。
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// ---- 文末答案区 ----

// parseAnswerSection 解析答案区，返回 题号→答案 与 题号→解析。
//
// 支持两种极常见写法：
//  1. 表格。横表「题号|1|2|3 / 答案|B|B|C」与竖表「题号|答案 / 1|B / 2|C」都认；
//  2. 编号段落「1．B 因为……」「2．A」，开头的字母是答案，其余是解析。
//
// 这两种写法都是国内试卷的标准答案版式，不解析就等于把整卷的答案丢掉。
func parseAnswerSection(lines []string) (answers, analyses map[int]string) {
	answers = map[int]string{}
	analyses = map[int]string{}
	if len(lines) == 0 {
		return answers, analyses
	}

	var tableRows [][]string
	records := map[int][]string{}
	var recordOrder []int
	curNum := -1

	for _, line := range lines {
		if isTableRow(line) {
			tableRows = append(tableRows, splitTableCells(line))
			curNum = -1
			continue
		}
		if n, rest, ok := matchNumberedItem(line); ok {
			curNum = n
			recordOrder = append(recordOrder, n)
			if strings.TrimSpace(rest) != "" {
				records[n] = append(records[n], rest)
			}
			continue
		}
		if curNum >= 0 && strings.TrimSpace(line) != "" {
			records[curNum] = append(records[curNum], line)
		}
	}

	for k, v := range answerTablePairs(tableRows) {
		answers[k] = v
	}

	for _, n := range recordOrder {
		parts := records[n]
		taggedAnswer, taggedAnalysis := splitTaggedAnswer(parts)
		text := joinLines(parts)

		answer := strings.TrimSpace(taggedAnswer)
		if answer == "" {
			answer = strings.TrimSpace(answers[n])
		}
		analysis := taggedAnalysis
		if analysis == "" {
			rest := stripLeadingAnswer(text, answer)
			if answer == "" {
				// 表格里没有答案时，编号段落开头的字母就是答案
				// （"1．B 因为……"）；实在认不出才把整段当解析。
				if a, tail := splitLeadingLetters(text); a != "" {
					answer, rest = a, tail
				}
			}
			analysis = rest
		}
		if answer != "" {
			answers[n] = answer
		}
		if strings.TrimSpace(analysis) != "" {
			analyses[n] = analysis
		}
	}
	return answers, analyses
}

// isTableRow 判断是否为 Markdown 管道表格行。
func isTableRow(line string) bool {
	t := strings.TrimSpace(line)
	return strings.HasPrefix(t, "|") && strings.Count(t, "|") >= 2
}

func splitTableCells(line string) []string {
	t := strings.TrimSpace(line)
	t = strings.TrimPrefix(t, "|")
	t = strings.TrimSuffix(t, "|")
	parts := strings.Split(t, "|")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, strings.TrimSpace(p))
	}
	return out
}

var tableSepCell = regexp.MustCompile(`^:?-{1,}:?$`)

func isTableSepRow(cells []string) bool {
	for _, c := range cells {
		if c == "" {
			continue
		}
		if !tableSepCell.MatchString(c) {
			return false
		}
	}
	return true
}

func stripCellMarkup(s string) string {
	return strings.TrimSpace(strings.Trim(strings.TrimSpace(s), "* "))
}

// answerTablePairs 从表格里读出 题号→答案。
//
// 横表：表头含「题号」，下一行是答案行，按列号配对；
// 竖表：表头就是「题号|答案」两列，后续每行是一对。
// answerTablePairs 从表格里取出「题号 → 答案」。
//
// 先认版式再逐行取值：竖表每行都是一组「题号 | 答案」，
// 横表则是表头一行题号、数据行一行答案。
// 认出版式之后**必须把剩下的行都处理完**——早先写成处理完第一行就 return，
// 于是「1|A / 2|B」这样的竖表只能回填出第 1 题的答案，
// 而且因为行号恰好是 1，表面看完全正常，只有第 2 题开始悄悄丢答案。
func answerTablePairs(rows [][]string) map[int]string {
	out := map[int]string{}
	var header []string
	vertical := false

	for _, row := range rows {
		if isTableSepRow(row) {
			continue
		}
		if header == nil {
			if containsAny(row, "题号") {
				header = row
				vertical = len(row) >= 2 && hasHeaderLabel(row, 0, "题号")
			}
			continue
		}

		if vertical {
			if len(row) < 2 {
				continue
			}
			n := leadingInt(stripCellMarkup(row[0]))
			ans := stripCellMarkup(row[1])
			if n > 0 && ans != "" {
				out[n] = ans
			}
			continue
		}

		// 横表：这一行是答案，按列号与表头配对。
		if containsAny(row, "答案") {
			for i, cell := range row {
				if i >= len(header) {
					break
				}
				n := leadingInt(stripCellMarkup(header[i]))
				ans := stripCellMarkup(cell)
				if n > 0 && ans != "" {
					out[n] = ans
				}
			}
		}
	}
	return out
}

func hasHeaderLabel(header []string, idx int, label string) bool {
	return idx < len(header) && strings.Contains(stripCellMarkup(header[idx]), label)
}

func containsAny(row []string, sub string) bool {
	for _, c := range row {
		if strings.Contains(stripCellMarkup(c), sub) {
			return true
		}
	}
	return false
}

// numberedItemPattern 答案区里的编号段落，如 "1．B 因为……"。
var numberedItemPattern = regexp.MustCompile(`^\s*(\d+)\s*[.、．)）]\s*`)

func matchNumberedItem(line string) (int, string, bool) {
	m := numberedItemPattern.FindStringSubmatch(line)
	if m == nil {
		return 0, "", false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		return 0, "", false
	}
	return n, line[len(m[0]):], true
}

// splitTaggedAnswer 在答案区段落里找显式的【答案】/【解析】标签。
//
// 显式标签的可靠性等同逐题解析：答案区里手写标签，说明出题人自己标记过，
// 应当优先于"从表格里猜"。
func splitTaggedAnswer(lines []string) (answer, analysis string) {
	var a, an []string
	cur := ""
	for _, line := range lines {
		f, inline, _, ok := matchTagLine(line)
		if !ok {
			if cur != "" && strings.TrimSpace(line) != "" {
				switch cur {
				case string(fieldAnswer):
					a = append(a, line)
				case string(fieldAnalysis):
					an = append(an, line)
				}
			}
			continue
		}
		switch f {
		case fieldAnswer:
			cur = string(fieldAnswer)
			if strings.TrimSpace(inline) != "" {
				a = append(a, inline)
			}
		case fieldAnalysis:
			cur = string(fieldAnalysis)
			if strings.TrimSpace(inline) != "" {
				an = append(an, inline)
			}
		default:
			cur = ""
		}
	}
	return joinLines(a), joinLines(an)
}

// stripLeadingAnswer 去掉解析段落开头重复抄写的答案。
func stripLeadingAnswer(text, answer string) string {
	t := strings.TrimSpace(text)
	if answer == "" {
		return t
	}
	if len(t) >= len(answer) && strings.EqualFold(t[:len(answer)], answer) {
		return strings.TrimSpace(t[len(answer):])
	}
	return t
}

// leadingLettersPattern 段落开头的选项字母，如 "B 因为…" 里的 B、"AB" 里的 AB。
var leadingLettersPattern = regexp.MustCompile(`^([A-Ha-h](?:\s*[、,，]?\s*[A-Ha-h])*)`)

// splitLeadingLetters 取段落开头的答案字母。
func splitLeadingLetters(text string) (answer, rest string) {
	m := leadingLettersPattern.FindStringSubmatch(strings.TrimSpace(text))
	if m == nil {
		return "", text
	}
	ans := strings.NewReplacer(" ", "", "、", "", ",", "", "，", "").Replace(m[1])
	ans = strings.ToUpper(ans)
	rest = strings.TrimSpace(strings.TrimSpace(text)[len(m[0]):])
	// "AB" 这种没有分隔符的写法有歧义：整段都在字母表内时它多半是答案，
	// 否则只取第一个字母。
	if !choiceLettersPattern.MatchString(ans) {
		ans = string(ans[0])
	}
	return ans, rest
}

// ---- 与模型路径共用的收尾 ----

// templateWarnings 补上模板解析特有的提示。
//
// 归一化阶段只能看到"一道题"，看不到整份试卷；这些检查必须在题块层面做，
// 否则"选择题没有选项"这种一眼可见的问题会被静默带进审核界面。
func templateWarnings(eq *ai.ExtractedQuestion) []string {
	var warns []string
	if strings.TrimSpace(eq.Stem) == "" {
		warns = append(warns, "未识别到题干，请手工补充")
	}
	if len(eq.Options) == 0 && looksLikeChoiceAnswer(eq.Answer) {
		warns = append(warns, "答案像是选项字母，但题目里没有选项，请核对")
	}
	// 只剩题干：选项与答案都没解析出来。
	// 条件是"两者都没有"而不是"没有选项"——简答题本来就没有选项，
	// 但它一定带参考答案；只有连答案也一起丢了，才说明这块版式没认出来。
	if len(eq.Options) == 0 && strings.TrimSpace(eq.Answer) == "" {
		warns = append(warns, "仅识别到题干，选项与答案均缺失，请核对")
	}
	return warns
}

// looksLikeChoiceAnswer 答案是否形如 "B" / "AB" / "（B）"。
func looksLikeChoiceAnswer(ans string) bool {
	s := strings.TrimSpace(ans)
	if s == "" {
		return false
	}
	s = strings.NewReplacer("（", "", "）", "", "(", "", ")", "").Replace(s)
	return choiceLettersPattern.MatchString(strings.ReplaceAll(s, " ", ""))
}
