package importing

import (
	"strings"
	"testing"

	"github.com/NanamiKyoka/questionmind/internal/ai"
	"github.com/NanamiKyoka/questionmind/internal/model"
)

// 回归：数学题的 stem_text 必须带上公式。
//
// 这是 2026 全国 1 卷实测出来的 bug。模型的输出完全正确——
// "样本数据 $6,8,4,5,12$ 的中位数为（　）" 里数据一个不少，
// stem_markdown 与 stem（富文本）也都完好，但 stem_text 变成了
// "样本数据 的中位数为（　）"：纯文本投影漏了 inlineMath 分支。
//
// 而审核列表与题库列表显示的恰恰是 stem_text，于是界面上看起来像
// "PDF 里的公式凭空消失了"，实际丢的只是一个摘要字段。
// 更麻烦的是它无法从现象反推原因——数据明明在库里。
func TestStemTextKeepsInlineMath(t *testing.T) {
	drafts := Normalize([]ai.ExtractedQuestion{{
		QuestionType: "single_choice",
		Stem:         "样本数据 $6,8,4,5,12$ 的中位数为（　）",
		Options: []ai.ExtractedOption{
			{Label: "A", Content: "$5$"},
			{Label: "B", Content: "$6$"},
		},
		Answer: "",
	}}, ModeRecord)

	if len(drafts) != 1 {
		t.Fatalf("应产出 1 份草稿，实际 %d", len(drafts))
	}
	d := drafts[0]

	if !strings.Contains(d.StemText, "6,8,4,5,12") {
		t.Fatalf("题干摘要必须含公式内容，实际 %q", d.StemText)
	}
	// 摘要与原文指向的必须是同一份内容：原文有的，摘要不能没有。
	if !strings.Contains(d.StemText, "中位数") {
		t.Fatalf("题干摘要应保留正文，实际 %q", d.StemText)
	}
	// 反向也要成立：摘要不该凭空多出 Markdown 定界符。
	if strings.Contains(d.StemText, "$") {
		t.Fatalf("摘要里不应残留 $ 定界符，实际 %q", d.StemText)
	}
}

// 块级公式同理：同样藏在 attrs 里，不显式处理就是凭空消失。
func TestStemTextKeepsBlockMath(t *testing.T) {
	drafts := Normalize([]ai.ExtractedQuestion{{
		QuestionType: "essay",
		Stem:         "求 $\\int_0^1 x\\,dx$ 的值。",
		Answer:       "",
	}}, ModeRecord)

	got := drafts[0].StemText
	if !strings.Contains(got, `\int_0^1 x\,dx`) {
		t.Fatalf("块级公式应保留 LaTeX 原文，实际 %q", got)
	}
}

// 选项里的公式走的是另一条投影（选项内容也要能显示）。
func TestOptionContentKeepsInlineMath(t *testing.T) {
	drafts := Normalize([]ai.ExtractedQuestion{{
		QuestionType: "single_choice",
		Stem:         "下列哪个是 $2$ 的平方根？",
		Options: []ai.ExtractedOption{
			{Label: "A", Content: "$\\sqrt{2}$"},
			{Label: "B", Content: "$2$"},
		},
		Answer: "",
	}}, ModeRecord)

	if len(drafts[0].Options) != 2 {
		t.Fatalf("应保留 2 个选项，实际 %d", len(drafts[0].Options))
	}
	if drafts[0].Options[0].Label != "A" {
		t.Fatalf("选项标签应保留，实际 %q", drafts[0].Options[0].Label)
	}
}

// 只要答案待补充，就不该因为"没读到公式"而生出错配的警告。
func TestRecordModeWithMathHasNoWarnings(t *testing.T) {
	drafts := Normalize([]ai.ExtractedQuestion{{
		QuestionType: "single_choice",
		Stem:         "已知集合 $A=\\{1,2\\}$，则 $A\\cap B=$（　）",
		Options: []ai.ExtractedOption{
			{Label: "A", Content: "$\\{1\\}$"},
			{Label: "B", Content: "$\\{2\\}$"},
		},
		Answer: "",
	}}, ModeRecord)

	if len(drafts[0].Warnings) != 0 {
		t.Fatalf("仅录题且公式完整时不该有警告，实际 %v", drafts[0].Warnings)
	}
	if drafts[0].Answer.Kind != model.AnswerKindPending {
		t.Fatalf("答案应待补充，实际 %s", drafts[0].Answer.Kind)
	}
}
