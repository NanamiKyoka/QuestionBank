package importing

import (
	"strings"
	"testing"

	"github.com/NanamiKyoka/questionmind/internal/ai"
	"github.com/NanamiKyoka/questionmind/internal/model"
)

// stemTextOf 取草稿的纯文本题干，便于断言。
func stemTextOf(d Draft) string {
	if d.StemText != "" {
		return d.StemText
	}
	return d.StemMarkdown
}

func labelsOfDraft(d Draft) []string {
	out := make([]string, 0, len(d.Options))
	for i := range d.Options {
		out = append(out, d.Options[i].Label)
	}
	return out
}

func optionTexts(d Draft) []string {
	out := make([]string, 0, len(d.Options))
	for i := range d.Options {
		var n richNode
		_ = d.Options[i].Content.Into(&n)
		out = append(out, plainTextOfRichDoc(d.Options[i].Content))
		_ = n
	}
	return out
}

func containsAll(hay []string, needles ...string) bool {
	for _, n := range needles {
		found := false
		for _, h := range hay {
			if strings.Contains(h, n) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func draftHasWarning(d Draft, sub string) bool {
	return hasWarning(d.Warnings, sub)
}

// 标签版式：题号在标签前、字段各归其位、题型与难度显式给出。
func TestParseTemplateTagLayout(t *testing.T) {
	text := `# 期末复习卷

1.【题目】下列函数中，在 $(0,+\infty)$ 上单调递增的是（　）
A. $y=x^2$
B. $y=-x^2$
C. $y=1/x$
D. $y=x^3$
【答案】A
【解析】$y=x^2$ 的导数 $2x>0$，故在定义域上递增
【难度】3
【知识点】导数与单调性

2.【题目】已知集合 $A=\{1,2\}$，则 $A$ 的真子集共有（　）个
【选项】A. 1  B. 3  C. 4  D. 7
【答案】B
【题型】单选
【难度】较易
`

	drafts, warns := ParseTemplate(text, "期末复习卷.md")
	if len(warns) != 0 {
		t.Fatalf("文档级提示应为空，实际 %v", warns)
	}
	if len(drafts) != 2 {
		t.Fatalf("应解析出 2 道题，实际 %d：%+v", len(drafts), drafts)
	}

	d0 := drafts[0]
	if d0.QuestionType != model.QuestionTypeSingleChoice {
		t.Fatalf("第 1 题题型应推断为单选，实际 %q", d0.QuestionType)
	}
	if d0.SourceNumber != 1 {
		t.Fatalf("第 1 题的原卷题号应为 1，实际 %d", d0.SourceNumber)
	}
	if d0.SourceFile != "期末复习卷.md" {
		t.Fatalf("SourceFile 应为文件名，实际 %q", d0.SourceFile)
	}
	if got := d0.Difficulty; got != 3 {
		t.Fatalf("【难度】3 应解析为 3，实际 %d", got)
	}
	if len(d0.Options) != 4 {
		t.Fatalf("第 1 题应有 4 个行内选项，实际 %d", len(d0.Options))
	}
	if got := labelsOfDraft(d0); !containsAll(got, "A", "B", "C", "D") {
		t.Fatalf("第 1 题选项标签应为 A/B/C/D，实际 %v", got)
	}
	if len(d0.Answer.Correct) != 1 || d0.Answer.Correct[0] != "A" {
		t.Fatalf("第 1 题答案应为 A，实际 %+v", d0.Answer)
	}
	if !strings.Contains(plainTextOfRichDoc(d0.Analysis), "导数") {
		t.Fatalf("第 1 题解析应保留原文，实际 %q", plainTextOfRichDoc(d0.Analysis))
	}
	if len(d0.KnowledgePoints) != 1 || d0.KnowledgePoints[0] != "导数与单调性" {
		t.Fatalf("第 1 题知识点解析错误：%v", d0.KnowledgePoints)
	}
	// 公式必须以 $...$ 保留下来，否则导出 Word 时会变成一串字面量。
	if !strings.Contains(d0.StemMarkdown, "$(0,+\\infty)$") {
		t.Fatalf("题干里的行内公式应原样保留，实际 %q", d0.StemMarkdown)
	}

	d1 := drafts[1]
	if d1.SourceNumber != 2 {
		t.Fatalf("第 2 题的原卷题号应为 2，实际 %d", d1.SourceNumber)
	}
	if len(d1.Options) != 4 {
		t.Fatalf("第 2 题【选项】整块应切出 4 个选项，实际 %d：%v", len(d1.Options), optionTexts(d1))
	}
	if got := optionTexts(d1); !containsAll(got, "1", "3", "4", "7") {
		t.Fatalf("第 2 题选项内容错误：%v", got)
	}
	if d1.Answer.Kind != "single" || len(d1.Answer.Correct) != 1 || d1.Answer.Correct[0] != "B" {
		t.Fatalf("第 2 题答案应为 B，实际 %+v", d1.Answer)
	}
	if d1.Difficulty != 2 {
		t.Fatalf("【难度】较易应解析为 2，实际 %d", d1.Difficulty)
	}
}

// 逐选项标签，以及每个选项的续行。
func TestParseTemplatePerOptionTags(t *testing.T) {
	text := `【题目】下列说法正确的是
【选项A】第一行
第二行属于 A
【选项B】乙
【选项C】丙
【选项D】丁
【答案】B
`
	drafts, _ := ParseTemplate(text, "a.md")
	if len(drafts) != 1 {
		t.Fatalf("应解析出 1 道题，实际 %d", len(drafts))
	}
	d := drafts[0]
	if len(d.Options) != 4 {
		t.Fatalf("应有 4 个选项，实际 %d", len(d.Options))
	}
	if got := labelsOfDraft(d); !containsAll(got, "A", "B", "C", "D") {
		t.Fatalf("选项标签错误：%v", got)
	}
	if !strings.Contains(strings.Join(optionTexts(d), "|"), "第二行属于 A") {
		t.Fatalf("A 的续行应并入 A 选项，实际 %v", optionTexts(d))
	}
	if strings.Contains(d.StemMarkdown, "第一行") {
		t.Fatalf("逐选项标签的内容不应混进题干，实际 %q", d.StemMarkdown)
	}
}

// 块状选项：选项各占一行，且没有【选项】标签。
func TestParseTemplateBlockOptions(t *testing.T) {
	text := `3. 下列各式中，正确的是（　）
A. $2^3=8$
B. $3^2=6$
C. $\\sqrt{9}=3$（错）
D. $1^0=1$

4. 下面说法错误的是
A．甲
B．乙
C．丙
D．丁
`
	drafts, _ := ParseTemplate(text, "b.txt")
	if len(drafts) != 2 {
		t.Fatalf("题号模式应切出 2 道题，实际 %d：%+v", len(drafts), drafts)
	}
	for i, d := range drafts {
		if len(d.Options) != 4 {
			t.Fatalf("第 %d 题应有 4 个块状选项，实际 %d：%v", i+1, len(d.Options), optionTexts(d))
		}
		if d.QuestionType != model.QuestionTypeSingleChoice {
			t.Fatalf("第 %d 题应按选择题处理，实际 %q", i+1, d.QuestionType)
		}
		if d.SourceNumber != i+3 {
			t.Fatalf("第 %d 题的原卷题号应为 %d，实际 %d", i+1, i+3, d.SourceNumber)
		}
	}
	// 块状选项不能混进题干。
	if strings.Contains(drafts[0].StemMarkdown, "2^3=8") {
		t.Fatalf("选项不应留在题干里，实际 %q", drafts[0].StemMarkdown)
	}
}

// 「2.5 mol」不能被当成题号：否则一道化学题会被劈成两半。
func TestParseTemplateNumberLikeValueIsNotQuestionNumber(t *testing.T) {
	text := `2.5 mol 的 NaOH 溶液浓度是多少
A. 1 mol/L
B. 2 mol/L
C. 3 mol/L
D. 4 mol/L
`
	drafts, _ := ParseTemplate(text, "chem.txt")
	if len(drafts) != 1 {
		t.Fatalf("应以 1 道题结束，实际 %d：%+v", len(drafts), drafts)
	}
	if !strings.Contains(drafts[0].StemMarkdown, "2.5 mol") {
		t.Fatalf("题干不应被题号切掉开头，实际 %q", drafts[0].StemMarkdown)
	}
}

// 大题标题不是题目。
func TestParseTemplateSectionHeadingIsNotAQuestion(t *testing.T) {
	text := `一、选择题（每小题 2 分，共 20 分）
1. 下列说法正确的是
A. 甲
B. 乙
C. 丙
D. 丁

二、填空题（每空 2 分）
2. 中国的首都是 ______。
`
	drafts, _ := ParseTemplate(text, "paper.md")
	if len(drafts) != 2 {
		t.Fatalf("大题标题不应被当作题目，实际 %d：%+v", len(drafts), drafts)
	}
	for i, d := range drafts {
		if strings.Contains(stemTextOf(d), "每小题") || strings.Contains(stemTextOf(d), "每空") {
			t.Fatalf("第 %d 题的题干里混进了大题标题：%q", i+1, d.StemMarkdown)
		}
	}
	if drafts[1].QuestionType != model.QuestionTypeFillBlank {
		t.Fatalf("第 2 题应按填空题处理，实际 %q", drafts[1].QuestionType)
	}
}

// 文末答案区（横表 + 编号段落）。
func TestParseTemplateTrailingAnswerSectionTable(t *testing.T) {
	text := `1. 下列说法正确的是（　）
A. 甲
B. 乙
C. 丙
D. 丁

2. 下列说法错误的是（　）
A. 甲
B. 乙
C. 丙
D. 丁

参考答案

| 题号 | 1 | 2 |
|:---:|:---:|:---:|
| 答案 | B | D |

1．B 因为乙项的定义就是如此
2．D 甲项显然不成立
`
	drafts, _ := ParseTemplate(text, "answers.md")
	if len(drafts) != 2 {
		t.Fatalf("应解析出 2 道题，实际 %d", len(drafts))
	}
	if got := drafts[0].Answer.Correct; len(got) != 1 || got[0] != "B" {
		t.Fatalf("第 1 题答案应从文末答案表回填为 B，实际 %+v", drafts[0].Answer)
	}
	if got := drafts[1].Answer.Correct; len(got) != 1 || got[0] != "D" {
		t.Fatalf("第 2 题答案应从文末答案表回填为 D，实际 %+v", drafts[1].Answer)
	}
	a0 := plainTextOfRichDoc(drafts[0].Analysis)
	if !strings.Contains(a0, "因为乙项") {
		t.Fatalf("第 1 题解析应来自编号段落，实际 %q", a0)
	}
	// 解析段落开头重复抄写的答案必须去掉，否则解析第一句会变成「BB 因为……」。
	if strings.HasPrefix(a0, "B B") {
		t.Fatalf("解析里重复的答案没有被去掉：%q", a0)
	}
}

// 文末答案区（竖表：题号|答案）。
func TestParseTemplateTrailingAnswerSectionVerticalTable(t *testing.T) {
	text := `【题目】第一题
【选项】A. 甲 B. 乙
【题目】第二题
【选项】A. 甲 B. 乙

【答案速查】
| 题号 | 答案 |
|:---:|:---:|
| 1 | A |
| 2 | B |
`
	drafts, _ := ParseTemplate(text, "v.md")
	if len(drafts) != 2 {
		t.Fatalf("应解析出 2 道题，实际 %d：%+v", len(drafts), drafts)
	}
	if drafts[0].Answer.Correct[0] != "A" || drafts[1].Answer.Correct[0] != "B" {
		t.Fatalf("竖表答案回填错误：%v / %v", drafts[0].Answer, drafts[1].Answer)
	}
}

// 文末答案区（纯编号段落，没有表格）。
func TestParseTemplateTrailingAnswerSectionParagraphsOnly(t *testing.T) {
	text := `1. 甲题
A. 甲
B. 乙
2. 乙题
A. 甲
B. 乙
3. 丙题
A. 甲
B. 乙

答案
1．A
2．B
3．A
`
	drafts, _ := ParseTemplate(text, "p.md")
	if len(drafts) != 3 {
		t.Fatalf("应解析出 3 道题，实际 %d：%+v", len(drafts), drafts)
	}
	want := []string{"A", "B", "A"}
	for i, w := range want {
		if len(drafts[i].Answer.Correct) != 1 || drafts[i].Answer.Correct[0] != w {
			t.Fatalf("第 %d 题答案应为 %s，实际 %+v", i+1, w, drafts[i].Answer)
		}
	}
}

// 答案区里的显式标签优先于从表格里猜出来的答案。
func TestParseTemplateAnswerSectionExplicitTagWins(t *testing.T) {
	text := `1. 甲题
A. 甲
B. 乙
2. 乙题
A. 甲
B. 乙

参考答案
| 题号 | 1 | 2 |
| 答案 | A | A |
1．【答案】B 【解析】显式标签更可靠
2．A 甲项成立
`
	drafts, _ := ParseTemplate(text, "e.md")
	if len(drafts) != 2 {
		t.Fatalf("应解析出 2 道题，实际 %d", len(drafts))
	}
	if got := drafts[0].Answer.Correct; len(got) != 1 || got[0] != "B" {
		t.Fatalf("显式【答案】应覆盖表格答案，实际 %+v", drafts[0].Answer)
	}
	if !strings.Contains(plainTextOfRichDoc(drafts[0].Analysis), "更可靠") {
		t.Fatalf("显式【解析】未生效：%q", plainTextOfRichDoc(drafts[0].Analysis))
	}
	if got := drafts[1].Answer.Correct; len(got) != 1 || got[0] != "A" {
		t.Fatalf("第 2 题答案应为 A，实际 %+v", drafts[1].Answer)
	}
}

// 解析不了的东西要变成警告，而不是被悄悄丢掉。
func TestParseTemplateKeepsUnparsableQuestionsWithWarnings(t *testing.T) {
	text := `【题目】只有题干没有选项也没有答案
`
	drafts, _ := ParseTemplate(text, "w.md")
	if len(drafts) != 1 {
		t.Fatalf("不应丢题，实际 %d", len(drafts))
	}
	if !draftHasWarning(drafts[0], "题干") {
		t.Fatalf("应给出提示，实际警告：%v", drafts[0].Warnings)
	}
	if drafts[0].QuestionType == "" {
		t.Fatalf("题型兜底不应为空")
	}
}

// 抽取方式解析：留空按 ai，未知值必须报错。
func TestParseMethod(t *testing.T) {
	if m, ok := ParseMethod(""); !ok || m != MethodAI {
		t.Fatalf("留空应按 ai 处理，实际 %q ok=%v", m, ok)
	}
	if m, ok := ParseMethod("Template"); !ok || m != MethodTemplate {
		t.Fatalf("应大小写不敏感地接受 template，实际 %q ok=%v", m, ok)
	}
	if _, ok := ParseMethod("gpt"); ok {
		t.Fatalf("未知取值必须被判为非法")
	}
}

// 规则路径产出的 Draft 必须与模型路径同形：每题都有 Stem/Answer/Warnings 等字段。
func TestParseTemplateDraftShapeMatchesModelPath(t *testing.T) {
	text := `【题目】题干
【选项】A. 甲 B. 乙
【答案】A
`
	fromTemplate, _ := ParseTemplate(text, "x.md")
	fromModel := Normalize([]ai.ExtractedQuestion{{
		Stem:    "题干",
		Options: []ai.ExtractedOption{{Label: "A", Content: "甲"}, {Label: "B", Content: "乙"}},
		Answer:  "A",
	}})
	if len(fromTemplate) != 1 || len(fromModel) != 1 {
		t.Fatalf("两条路径都应产出 1 道草稿")
	}
	a, b := fromTemplate[0], fromModel[0]
	if a.QuestionType != b.QuestionType || a.TypeLabel != b.TypeLabel ||
		len(a.Options) != len(b.Options) || a.Answer.Kind != b.Answer.Kind {
		t.Fatalf("两条路径的草稿形状不一致：%+v vs %+v", a, b)
	}
	// 两条路径都经过同一套归一化，因此警告也应当一致。
	// 早先这里断言"模型路径没有警告"，但归一化阶段本来就会对
	// 缺答案、题型靠推断的题给出提示，模型路径同样会拿到——
	// 断言它为空等于要求两条路径走不同的收尾逻辑。
	if len(a.Warnings) == 0 {
		t.Fatalf("规则路径应给出提示，实际无警告：%+v", a)
	}
	if strings.Join(a.Warnings, "|") != strings.Join(b.Warnings, "|") {
		t.Fatalf("两条路径的警告应一致：%v vs %v", a.Warnings, b.Warnings)
	}
}

// AI 路径也要用真实题号：模型常常把原卷编号原样留在题干开头。
func TestNormalizeCapturesSourceNumberFromStem(t *testing.T) {
	drafts := Normalize([]ai.ExtractedQuestion{
		{Stem: "下列说法正确的是", Answer: "A"},
		{Stem: "12. 下列说法错误的是", Answer: "B"},
	})
	if drafts[0].SourceNumber != 1 {
		t.Fatalf("没有题号时应退回下标 1，实际 %d", drafts[0].SourceNumber)
	}
	if drafts[1].SourceNumber != 12 {
		t.Fatalf("题干里的题号 12 应被采用，实际 %d", drafts[1].SourceNumber)
	}
}
