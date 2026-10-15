package importing

import (
	"regexp"
	"strings"

	"github.com/NanamiKyoka/questionmind/internal/ai"
	"github.com/NanamiKyoka/questionmind/internal/domain"
	"github.com/NanamiKyoka/questionmind/internal/model"
)

// Draft 一题的结构化草稿。
//
// 刻意保留 StemMarkdown 原文：审核界面需要展示模型原始输出，
// 人工修正时也以原文为基准，而不是让用户面对已经丢失格式的纯文本。
type Draft struct {
	TempID       string `json:"temp_id,omitempty"`
	ParentTempID string `json:"parent_temp_id,omitempty"`

	// SourceNumber 原卷里的题号。
	//
	// 刻意**不是**草稿数组的下标：审核界面同时打印「第 N 题」和
	//「原卷第 N 题」，两个数字相等时后者是句假话——原卷从「三、」起算、
	// 每个大题重新编号、或中间跳号时，下标和题号根本不是一回事。
	// 规则解析时由切题时捕获的题号决定；模型路径则从题干开头的
	// 「3.」里取，取不到才退回下标。
	SourceNumber int `json:"source_number,omitempty"`

	// SourceFile 这道题来自哪个文件。
	//
	// 一次 zip 导入会覆盖多个文件，界面要靠它把草稿按来源分组；
	// 单文件导入时是那一个文件名，粘贴文本时是服务生成的可读描述。
	SourceFile string `json:"source_file"`

	QuestionType model.QuestionType `json:"question_type"`
	TypeLabel    string             `json:"type_label"`

	Stem         model.JSON `json:"stem"`
	StemMarkdown string     `json:"stem_markdown"`
	StemText     string     `json:"stem_text"`

	Options []model.Option   `json:"options"`
	Answer  model.AnswerSpec `json:"answer"`

	Analysis model.JSON `json:"analysis"`
	Solution model.JSON `json:"solution"`
	Summary  model.JSON `json:"summary"`

	Difficulty int `json:"difficulty"`

	KnowledgePoints []string `json:"knowledge_points"`
	Tags            []string `json:"tags"`

	// SuggestedKnowledgePoints 向量检索给出的知识点建议。
	//
	// 与 KnowledgePoints 的区别：后者是**模型自己说**的，前者是
	// 用题干做语义检索从现有知识点里**找出来**的候选。
	// 两者都只是建议，用户确认后才会真正关联——知识点归属错乱
	// 比没有知识点更麻烦，事后清理成本很高。
	SuggestedKnowledgePoints []KnowledgeSuggestion `json:"suggested_knowledge_points,omitempty"`

	// Warnings 归一化过程中发现的问题。审核界面据此高亮提示，
	// 让用户知道"哪一题需要重点看"。
	Warnings []string `json:"warnings"`
}

// KnowledgeSuggestion 知识点匹配建议。
type KnowledgeSuggestion struct {
	KnowledgePointID uint    `json:"knowledge_point_id"`
	Name             string  `json:"name"`
	Path             string  `json:"path"`
	Score            float32 `json:"score"`
}

// Normalize 把模型抽取结果转换为结构化草稿。
//
// 整体策略是"宽进严出"：模型输出的格式千奇百怪，这里尽力兼容；
// 但输出必须是严格的结构，供后续校验与入库使用。
// 无法确定的地方不猜——降级为 free_response 并附上 warning，
// 让用户来定，比猜错导致答案错位要好。
//
// mode 决定"这道题本来该不该有答案"：仅录题模式下模型按要求不求解，
// 于是缺答案不是抽取出错，草稿会直接标成待补（见 parseAnswer）。
func Normalize(extracted []ai.ExtractedQuestion, mode Mode) []Draft {
	out := make([]Draft, 0, len(extracted))
	for i := range extracted {
		out = append(out, normalizeOne(&extracted[i], i+1, mode))
	}
	linkParents(out)
	return out
}

// linkParents 校正母子关系：统一临时 id 的空白，并给**无法解析**的母题引用附上警告。
//
// temp_id / parent_temp_id 是模型给出的临时编号，最终要换成数据库主键，
// 两边必须逐字符相等才能配上——一个多余空格就会让整组材料题在入库时散架，
// 而这个错误用户完全看不见（题都建出来了，只是彼此不认识）。
//
// 指向批次外的母题（模型只抽出了小问、没抽出母题）和自引用同理：入库时不可能
// 建立父子关系，与其静默按独立题处理，不如在这里就明确告诉用户"这一组没保住"。
func linkParents(drafts []Draft) {
	ids := make(map[string]int, len(drafts))
	for i := range drafts {
		drafts[i].TempID = strings.TrimSpace(drafts[i].TempID)
		drafts[i].ParentTempID = strings.TrimSpace(drafts[i].ParentTempID)
		if drafts[i].TempID != "" {
			if _, exists := ids[drafts[i].TempID]; !exists {
				ids[drafts[i].TempID] = i
			}
		}
	}
	for i := range drafts {
		if drafts[i].ParentTempID == "" {
			continue
		}
		if drafts[i].ParentTempID == drafts[i].TempID {
			drafts[i].ParentTempID = ""
			drafts[i].Warnings = append(drafts[i].Warnings, "母题引用指向自身，将作为独立题目导入")
			continue
		}
		if _, ok := ids[drafts[i].ParentTempID]; !ok {
			drafts[i].ParentTempID = ""
			drafts[i].Warnings = append(drafts[i].Warnings,
				"本次抽取结果中没有对应母题，该小问将作为独立题目导入")
		}
	}
	for i := range drafts {
		parentIndex, ok := ids[drafts[i].ParentTempID]
		if !ok || parentIndex == i {
			continue
		}
		if parent := drafts[parentIndex].SourceNumber; parent > 0 {
			drafts[i].SourceNumber = parent
		}
	}
}

func normalizeOne(q *ai.ExtractedQuestion, index int, mode Mode) Draft {
	// 题号优先从题干开头的「3.」里取：模型经常原样保留原卷编号，
	// 取不到（下标是最常见的实际情况）才退回数组下标。
	sourceNumber := q.SourceNumber
	if sourceNumber == 0 {
		sourceNumber = captureSourceNumber(q.Stem)
	}
	if sourceNumber == 0 {
		sourceNumber = index
	}

	d := Draft{
		TempID:          q.TempID,
		ParentTempID:    q.ParentTempID,
		SourceNumber:    sourceNumber,
		StemMarkdown:    strings.TrimSpace(q.Stem),
		Difficulty:      normalizeDifficulty(q.Difficulty),
		KnowledgePoints: cleanStrings(q.KnowledgePoints),
		Tags:            cleanStrings(q.Tags),
		Warnings:        []string{},
	}

	// 1) 选项：优先用模型单独给出的 options；没有再尝试从题干里切出来。
	opts := optionsFromExtracted(q.Options)
	if len(opts) == 0 {
		var stemRest string
		stemRest, opts = splitOptionsFromStem(d.StemMarkdown)
		if len(opts) > 0 {
			d.StemMarkdown = stemRest
			d.Warnings = append(d.Warnings, "选项从题干中自动切分，请核对")
		}
	}

	// 2) 题型：模型给的类型优先，但要做别名归并；缺失或无法识别时推断。
	qtype := coerceQuestionType(q.QuestionType)
	if qtype == "" {
		qtype = inferQuestionType(d.StemMarkdown, opts, q.Answer)
		d.Warnings = append(d.Warnings, "题型由内容推断，请核对")
	}
	d.QuestionType = qtype
	d.TypeLabel = qtype.Label()

	// 3) 归一化选项 id（答案解析要用到）。
	normalized, err := domain.NormalizeOptions(opts)
	if err != nil {
		d.Warnings = append(d.Warnings, "选项标签重复或为空，已忽略选项")
	}
	// 非选择题的选项是空数组而不是 nil：草稿卡按 options.length 渲染，
	// nil 会序列化成 null，让整张审核卡抛异常。
	if normalized == nil {
		normalized = []model.Option{}
	}
	d.Options = normalized
	if (qtype == model.QuestionTypeSingleChoice || qtype == model.QuestionTypeMultipleChoice) &&
		len(normalized) == 0 && isSentenceBreakQuestion(d.StemMarkdown) {
		qtype = model.QuestionTypeFillBlank
		d.Warnings = append(d.Warnings, "断句题未提供选项，题型已按填空题处理")
	}
	d.QuestionType = qtype
	d.TypeLabel = qtype.Label()

	// 4) 题干与解析转富文本。
	d.Stem = MarkdownToRichDoc(d.StemMarkdown)
	d.StemText = plainTextOfRichDoc(d.Stem)
	d.Analysis = markdownOrNil(q.Analysis)

	// 5) 答案：按题型解析。
	answer, warns := parseAnswer(qtype, q.Answer, normalized, d.StemMarkdown, mode)
	d.Answer = answer
	d.Warnings = append(d.Warnings, warns...)

	// 6) 交叉校验：术语上说不通的地方标出来。
	// 答案待补时跳过：拿"没有答案"去比对题干空位，报出来的只会是"不一致"。
	if qtype == model.QuestionTypeFillBlank && answer.Kind != model.AnswerKindPending {
		if n := CountBlanksInMarkdown(d.StemMarkdown); n > 0 && n != len(answer.Blanks) {
			d.Warnings = append(d.Warnings,
				"题干空位数量与答案个数不一致，请核对")
		}
	}

	return d
}

// ---- 题型 ----

// typeAliases 模型可能返回的题型写法到内部枚举的映射。
//
// 用精确映射而不是子串匹配：参考实现里用 `strings.Contains(s, "multiple")`
// 判断题型，结果 "single_choice_multiple" 这类串会命中错误的分支。
// 宁可多写几行别名表，也不要靠子串猜。
var typeAliases = map[string]model.QuestionType{
	"single_choice": model.QuestionTypeSingleChoice,
	"single":        model.QuestionTypeSingleChoice,
	"单选":            model.QuestionTypeSingleChoice,
	"单选题":           model.QuestionTypeSingleChoice,
	"单项选择题":         model.QuestionTypeSingleChoice,

	"multiple_choice": model.QuestionTypeMultipleChoice,
	"multiple":        model.QuestionTypeMultipleChoice,
	"multi":           model.QuestionTypeMultipleChoice,
	"多选":              model.QuestionTypeMultipleChoice,
	"多选题":             model.QuestionTypeMultipleChoice,
	"多项选择题":           model.QuestionTypeMultipleChoice,

	"fill_blank":    model.QuestionTypeFillBlank,
	"fill_in_blank": model.QuestionTypeFillBlank,
	"fill":          model.QuestionTypeFillBlank,
	"blank":         model.QuestionTypeFillBlank,
	"填空":            model.QuestionTypeFillBlank,
	"填空题":           model.QuestionTypeFillBlank,

	"true_false": model.QuestionTypeTrueFalse,
	"truefalse":  model.QuestionTypeTrueFalse,
	"boolean":    model.QuestionTypeTrueFalse,
	"judge":      model.QuestionTypeTrueFalse,
	"判断":         model.QuestionTypeTrueFalse,
	"判断题":        model.QuestionTypeTrueFalse,

	"essay":         model.QuestionTypeEssay,
	"free_response": model.QuestionTypeEssay,
	"short_answer":  model.QuestionTypeEssay,
	"subjective":    model.QuestionTypeEssay,
	"解答":            model.QuestionTypeEssay,
	"解答题":           model.QuestionTypeEssay,
	"简答题":           model.QuestionTypeEssay,
	"计算题":           model.QuestionTypeEssay,
	"证明题":           model.QuestionTypeEssay,
}

// coerceQuestionType 把模型的题型描述归并到内部枚举。
// 无法识别时返回空串，由调用方推断。
func coerceQuestionType(raw string) model.QuestionType {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return ""
	}
	// 去掉空白与连字符，容忍 "single choice" / "single-choice"。
	compact := strings.NewReplacer(" ", "_", "-", "_").Replace(s)

	if t, ok := typeAliases[compact]; ok {
		return t
	}
	if t, ok := typeAliases[s]; ok {
		return t
	}
	// 已经是合法枚举值。
	if t := model.QuestionType(compact); t.Valid() {
		return t
	}
	return ""
}

// trueFalseMarkers 判断题的答案写法。
var trueFalseMarkers = map[string]bool{
	"对": true, "正确": true, "√": true, "是": true, "t": true, "true": true,
	"yes": true, "y": true, "1": true, "а": false, // 占位，避免误命中
}

var falseMarkers = map[string]bool{
	"错": true, "错误": true, "×": true, "x": true, "否": true, "f": true,
	"false": true, "no": true, "n": true, "0": true,
}

// inferQuestionType 从内容推断题型。
func inferQuestionType(stem string, opts []model.Option, answerRaw string) model.QuestionType {
	answer := strings.TrimSpace(answerRaw)

	// 有选项：按正确答案的个数区分单选/多选。
	if len(opts) >= 2 {
		letters := extractChoiceLetters(answer, labelsOf(opts))
		if len(letters) > 1 {
			return model.QuestionTypeMultipleChoice
		}
		return model.QuestionTypeSingleChoice
	}

	// 答案是判断类表述。
	lower := strings.ToLower(answer)
	if trueFalseMarkers[lower] || falseMarkers[lower] {
		return model.QuestionTypeTrueFalse
	}

	// 题干里有空位标记。
	if CountBlanksInMarkdown(stem) > 0 || strings.Contains(stem, "（）") || strings.Contains(stem, "（  ）") {
		return model.QuestionTypeFillBlank
	}

	// 其余一律按解答题处理：这是最安全的兜底——
	// 判成填空/选择一旦错了会导致答案错位，判成解答只是少了个自动判分。
	return model.QuestionTypeEssay
}

func isSentenceBreakQuestion(stem string) bool {
	return strings.Contains(stem, "断句") || strings.Contains(stem, "标号涂黑")
}

func normalizeDifficulty(v int) int {
	if v < 1 || v > 5 {
		return 3
	}
	return v
}

// ---- 选项 ----

func optionsFromExtracted(opts []ai.ExtractedOption) []model.Option {
	out := make([]model.Option, 0, len(opts))
	for _, o := range opts {
		label := strings.TrimSpace(o.Label)
		content := strings.TrimSpace(o.Content)
		if label == "" && content == "" {
			continue
		}
		// 模型可能把标签也写进内容里（"A. 内容"），去掉重复前缀。
		content = stripLabelPrefix(content, label)
		out = append(out, model.Option{
			Label:   label,
			Content: MarkdownToRichDoc(content),
		})
	}
	return out
}

// stripLabelPrefix 去掉内容前重复出现的选项标签。
func stripLabelPrefix(content, label string) string {
	if label == "" || content == "" {
		return content
	}
	trimmed := strings.TrimSpace(content)
	for _, sep := range []string{".", "、", "．", ")", "）", ":", "："} {
		if strings.HasPrefix(trimmed, label+sep) {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, label+sep))
		}
	}
	return content
}

// optionMarkerPattern 匹配选项起始标记，如 "A." "B、" "C．" "D)"。
// 要求标记前是行首或空白，避免匹配到单词内部的字母。
var optionMarkerPattern = regexp.MustCompile(`(?:^|\s)([A-Ha-h])\s*[.、．)）:：]\s*`)

// minEmbeddedOptions 从题干里切分选项时要求的最少选项数。
//
// 取 3 而不是 2 是刻意的：正文里"点 A…点 B…"这类表述天然构成两个连续标签，
// 只要求 2 个会把大量说明文字误切成选项。而真实选择题几乎都是三选项
// （A/B/C）或四选项（A/B/C/D），两选项的极少。
//
// 注意这只是**兜底**路径：模型正常会把选项单独给出，那条路不经过这里。
const minEmbeddedOptions = 3

// maxEmbeddedOptionLen 选项内容的最大长度（字符）。
// 超过这个长度说明切出来的多半是句子而不是选项。
const maxEmbeddedOptionLen = 60

// splitOptionsFromStem 从题干里切出选项，返回剩余题干与选项列表。
//
// 两道防线把误判率压下来：
//  1. 标签必须**连续**（A、B、C…），零星的 "A." 不算；
//  2. 连续标签至少 3 个，且每段内容都足够短。
func splitOptionsFromStem(text string) (string, []model.Option) {
	matches := optionMarkerPattern.FindAllStringSubmatchIndex(text, -1)
	if len(matches) < minEmbeddedOptions {
		return text, nil
	}

	// 找出最长的连续标签序列。
	bestStart, bestLen := -1, 0
	runStart, runLen := 0, 1
	for i := 1; i <= len(matches); i++ {
		cont := false
		if i < len(matches) {
			prev := strings.ToUpper(text[matches[i-1][2]:matches[i-1][3]])
			cur := strings.ToUpper(text[matches[i][2]:matches[i][3]])
			cont = len(prev) == 1 && len(cur) == 1 && cur[0] == prev[0]+1
		}
		if cont {
			runLen++
			continue
		}
		if runLen > bestLen {
			bestStart, bestLen = runStart, runLen
		}
		runStart, runLen = i, 1
	}
	if bestLen < minEmbeddedOptions {
		return text, nil
	}

	chosen := matches[bestStart : bestStart+bestLen]
	stem := strings.TrimSpace(text[:chosen[0][0]])
	if stem == "" {
		// 没有题干只剩选项，说明这更像一段正文枚举而不是选项列表。
		return text, nil
	}

	opts := make([]model.Option, 0, len(chosen))
	for i := range chosen {
		label := strings.ToUpper(text[chosen[i][2]:chosen[i][3]])
		start := chosen[i][1]
		// 内容到下一个标记之前，或到文本结尾。
		end := len(text)
		if i+1 < len(chosen) {
			end = chosen[i+1][0]
		}
		content := strings.TrimSpace(text[start:end])
		if content == "" {
			continue
		}
		if len([]rune(content)) > maxEmbeddedOptionLen {
			// 有一段明显是句子：整体放弃切分，保留原题干让用户处理。
			return text, nil
		}
		opts = append(opts, model.Option{
			Label:   label,
			Content: MarkdownToRichDoc(content),
		})
	}
	if len(opts) < 2 {
		return text, nil
	}
	return stem, opts
}

func labelsOf(opts []model.Option) []string {
	out := make([]string, 0, len(opts))
	for i := range opts {
		out = append(out, opts[i].Label)
	}
	return out
}

// ---- 答案 ----

// choiceLettersPattern 匹配答案里的选项字母。
// 只在答案整体是"字母 + 分隔符"的形式时才解析，避免从解释文字里刮字母。
var choiceLettersPattern = regexp.MustCompile(`^[A-Ha-h][A-Ha-h\s,，、/|]*$`)

// extractChoiceLetters 从答案文本里取出选项标签。
//
// labels 是题干实际出现的选项标签；只有当答案中的字母全部落在该集合内，
// 才认为是有效解析。这一约束能挡掉"答案写的是某个变量名"这类误判。
func extractChoiceLetters(answer string, labels []string) []string {
	raw := strings.TrimSpace(answer)
	if raw == "" {
		return nil
	}
	// 去掉常见包装：括号、全角括号、"故选"之类的前后缀。
	for _, affix := range []string{"（", "）", "(", ")", "【", "】", "答案：", "答案:", "故选", "选"} {
		raw = strings.ReplaceAll(raw, affix, "")
	}
	raw = strings.TrimSpace(raw)

	if !choiceLettersPattern.MatchString(raw) {
		return nil
	}

	valid := make(map[string]bool, len(labels))
	for _, l := range labels {
		valid[strings.ToUpper(strings.TrimSpace(l))] = true
	}

	seen := map[string]bool{}
	var out []string
	for _, r := range strings.ToUpper(raw) {
		if r < 'A' || r > 'H' {
			continue
		}
		letter := string(r)
		if !valid[letter] || seen[letter] {
			continue
		}
		seen[letter] = true
		out = append(out, letter)
	}
	return out
}

// ParseSolvedAnswer 把模型给出的答案文本按**题目原有的题型**解析成答案结构。
//
// 与抽取路径的关键差别：这里题型是已知的（题目已经在题库里），
// 不需要让模型回答"这是什么题型"，也就不会因为它判断失误而把一道好题改写坏。
// 选项标签同样以题库里那份为准，模型写 B 就对应题库的 B。
//
// 返回的第二个值是警告（例如"未能识别答案"），由调用方决定是写库还是报错。
func ParseSolvedAnswer(qtype model.QuestionType, raw string, opts []model.Option, stemText string) (model.AnswerSpec, []string) {
	return parseAnswer(qtype, raw, opts, stemText, ModeSolve)
}

// parseAnswer 按题型解析答案文本，返回答案结构与警告。
func parseAnswer(qtype model.QuestionType, raw string, opts []model.Option, stem string, mode Mode) (model.AnswerSpec, []string) {
	raw = strings.TrimSpace(raw)
	var warns []string

	// 仅录题：模型被要求只记录题目，答案本就不该有。
	// 这种情况不是"没抽出来"，所以不给 warning——满屏"请手动选择正确选项"
	// 只会让审核界面看起来像坏了。标成 pending，入库后由老师补答案。
	// 材料里本来就有答案时照常解析：模型给了就用，没人会想把它丢掉。
	if mode == ModeRecord && raw == "" {
		return model.AnswerSpec{Kind: model.AnswerKindPending}, nil
	}

	switch qtype {
	case model.QuestionTypeSingleChoice, model.QuestionTypeMultipleChoice:
		letters := extractChoiceLetters(raw, labelsOf(opts))
		if len(letters) == 0 {
			warns = append(warns, "未能识别答案，请手动选择正确选项")
			kind := "single"
			if qtype == model.QuestionTypeMultipleChoice {
				kind = "multiple"
			}
			return model.AnswerSpec{Kind: kind, Correct: []string{}}, warns
		}
		if qtype == model.QuestionTypeSingleChoice && len(letters) > 1 {
			warns = append(warns, "识别到多个正确答案，题型已按多选处理")
		}
		if qtype == model.QuestionTypeMultipleChoice && len(letters) == 1 {
			warns = append(warns, "只识别到一个正确答案，请确认是否为多选题")
		}
		// 这里填标签，入库时由 domain 层统一解析为选项 id。
		return model.AnswerSpec{Kind: kindFor(qtype), Correct: letters}, warns

	case model.QuestionTypeTrueFalse:
		lower := strings.ToLower(strings.TrimSpace(strings.NewReplacer(
			"（", "", "）", "", "(", "", ")", "", " ", "").Replace(raw)))
		switch {
		case trueFalseMarkers[lower]:
			v := true
			return model.AnswerSpec{Kind: "boolean", BoolAnswer: &v}, warns
		case falseMarkers[lower]:
			v := false
			return model.AnswerSpec{Kind: "boolean", BoolAnswer: &v}, warns
		}
		warns = append(warns, "未能识别判断结果，请手动选择对或错")
		return model.AnswerSpec{Kind: "boolean"}, warns

	case model.QuestionTypeFillBlank:
		blanks := parseBlankAnswers(raw)
		if len(blanks) == 0 {
			warns = append(warns, "未能识别填空答案，请手动补充")
			return model.AnswerSpec{Kind: "blanks", Blanks: []model.Blank{}}, warns
		}
		return model.AnswerSpec{Kind: "blanks", Blanks: blanks}, warns

	case model.QuestionTypeEssay:
		if raw == "" {
			warns = append(warns, "模型未给出参考答案")
			return model.AnswerSpec{Kind: "reference"}, warns
		}
		return model.AnswerSpec{Kind: "reference", Reference: MarkdownToRichDoc(raw)}, warns
	}

	return model.AnswerSpec{Kind: "unresolved", Raw: raw}, []string{"无法识别的题型"}
}

func kindFor(t model.QuestionType) string {
	if t == model.QuestionTypeMultipleChoice {
		return "multiple"
	}
	return "single"
}

// blankPatternAnswer 答案里各空之间的分隔符。
var blankAnswerSep = regexp.MustCompile(`[;；\n]|(?:\s*\|\s*)`)

// altPattern 同一空内多个可接受答案之间的分隔符。
var altPattern = regexp.MustCompile(`\s*(?:或|/|\\|\|)\s*`)

// parseBlankAnswers 解析填空题答案。
//
// 约定：空之间用分号/换行/竖线分隔；同一个空内的多个可接受答案用
// "或"或斜杠分隔。这与国内试卷答案的常见写法一致。
func parseBlankAnswers(raw string) []model.Blank {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	// 去掉"答案："之类的前缀。
	for _, p := range []string{"答案：", "答案:", "参考答案：", "参考答案:"} {
		raw = strings.TrimPrefix(raw, p)
	}

	parts := blankAnswerSep.Split(strings.TrimSpace(raw), -1)
	var blanks []model.Blank

	for i, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		// 去掉可能残留的空位编号，如 "(1)" "①" "第1空"。
		part = stripBlankNumbering(part)

		alts := altPattern.Split(part, -1)
		group := make([][]model.JSON, 0, len(alts))
		for _, alt := range alts {
			alt = strings.TrimSpace(alt)
			if alt == "" {
				continue
			}
			group = append(group, []model.JSON{MarkdownToRichDoc(alt)})
		}
		if len(group) == 0 {
			continue
		}
		blanks = append(blanks, model.Blank{
			ID:      blankID(len(blanks) + 1),
			Accepts: group,
		})
		_ = i
	}
	return blanks
}

var blankNumberingPattern = regexp.MustCompile(`^\s*(?:\(?\d+\)?[.、)）]\s*|第\s*\d+\s*空\s*[:：]?|[①②③④⑤⑥⑦⑧⑨⑩]\s*)`)

func stripBlankNumbering(s string) string {
	return strings.TrimSpace(blankNumberingPattern.ReplaceAllString(s, ""))
}

func blankID(n int) string {
	return "blk_" + itoa(n)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// ---- 小工具 ----

func markdownOrNil(s string) model.JSON {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return MarkdownToRichDoc(s)
}

func cleanStrings(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// plainTextOfRichDoc 从富文本 JSON 提取纯文本（与 domain.PlainText 等价，
// 但这里只依赖自己的结构，避免为了一行文本引入跨包依赖）。
//
// 公式必须显式处理，这一点是从线上事故里学到的：数学题的题干里
// 数字与变量几乎全在 inlineMath 里（"样本数据 $6,8,4,5,12$ 的中位数"），
// 只走 Text 分支就会把它们全丢掉，得到"样本数据 的中位数为（　）"。
// 数据其实一直好好地存在 stem_markdown 与 stem 里，丢的只有这个**摘要**，
// 而审核列表与题库列表显示的正是它——于是界面上看着像公式凭空消失了，
// 实际是投影函数漏了一种节点类型。
func plainTextOfRichDoc(raw model.JSON) string {
	var doc richNode
	if err := raw.Into(&doc); err != nil {
		return ""
	}
	var sb strings.Builder
	var walk func(nodes []richNode)
	walk = func(nodes []richNode) {
		for i := range nodes {
			if nodes[i].Text != "" {
				sb.WriteString(nodes[i].Text)
			}
			switch nodes[i].Type {
			case nodeBlank:
				sb.WriteString("________")
			case nodeInlineMath:
				// 用 LaTeX 原文：既保住内容，也让用户能按 "x^2" 这样的写法搜索。
				sb.WriteString(latexOf(nodes[i].Attrs))
			case nodeBlockMath:
				sb.WriteString(" ")
				sb.WriteString(latexOf(nodes[i].Attrs))
				sb.WriteString(" ")
			case nodeParagraph, nodeHeading:
				// 段落之间不加分隔符，保持与显示文本一致。
				_ = i
			}
			walk(nodes[i].Content)
		}
	}
	walk(doc.Content)
	return strings.TrimSpace(sb.String())
}

// latexOf 从公式节点的 attrs 里取 LaTeX 原文。
func latexOf(attrs map[string]any) string {
	if attrs == nil {
		return ""
	}
	if latex, ok := attrs["latex"].(string); ok {
		return latex
	}
	return ""
}
