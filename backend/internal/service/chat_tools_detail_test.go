package service

import (
	"strings"
	"testing"

	"github.com/NanamiKyoka/questionmind/internal/importing"
	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/schema"
)

// 喂给 AI 助手的题目必须带公式。
//
// get_question 此前用一个只认 text 字段的手写递归提取文本，而公式的文字
// 藏在 attrs.latex 里、整段取不到。模型看到的是"若 ，则…"，四个选项全是
// 空串——它据此给出的判断多半是错的，而错因在界面上完全看不出来。
func TestMarkdownFromJSONKeepsMath(t *testing.T) {
	doc := importing.MarkdownToRichDoc("若 $f(x)=x^2$，则 $f(2)=$（  ）")
	got := markdownFromJSON(doc)

	if !strings.Contains(got, "$f(x)=x^2$") {
		t.Fatalf("公式应保留 $ 定界符与 LaTeX 原文，实际 %q", got)
	}
	if strings.Contains(got, "若 ，则") {
		t.Fatalf("公式位置留下了空洞，实际 %q", got)
	}
}

// 选项里的公式同样不能丢：选项变空串等于把整道选择题变成了一句话题。
func TestQuestionDetailResultKeepsOptionMath(t *testing.T) {
	detail := &schema.QuestionDetail{
		QuestionBrief: schema.QuestionBrief{
			ID:           7,
			QuestionType: model.QuestionTypeSingleChoice,
			StemDoc:      importing.MarkdownToRichDoc("已知平面向量 $\\boldsymbol{a}$ 不共线"),
		},
		Stem: importing.MarkdownToRichDoc("已知平面向量 $\\boldsymbol{a}$ 不共线"),
		Options: []model.Option{
			{ID: "opt_1", Label: "A", Content: importing.MarkdownToRichDoc("$\\boldsymbol{a}$ 与 $\\boldsymbol{b}$ 共线")},
			{ID: "opt_2", Label: "B", Content: importing.MarkdownToRichDoc("$x = 1$")},
		},
	}

	res := questionDetailResult(detail)
	if !strings.Contains(res.Content, "\\boldsymbol{a}") {
		t.Fatalf("题干里的公式不应消失：%s", res.Content)
	}
	// 公式带 $ 定界符，模型才知道这是数学而不是散文。
	if !strings.Contains(res.Content, "$\\\\boldsymbol{a}$") {
		t.Fatalf("题干公式应保留 $ 定界符：%s", res.Content)
	}
	if !strings.Contains(res.Content, "$x = 1$") {
		t.Fatalf("选项里的公式不应消失：%s", res.Content)
	}
}
