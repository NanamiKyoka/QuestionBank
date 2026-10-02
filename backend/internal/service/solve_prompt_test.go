package service

import (
	"context"
	"strings"
	"testing"

	"github.com/NanamiKyoka/questionmind/internal/importing"
	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/schema"
)

// 题库内 AI 解题发给模型的**题目本身**必须完整：题型、题干、选项一个都不能少。
//
// 这组断言针对的是一个具体的故障：用户点「AI 解题」，模型给出的答案像是
// 没看过选项。提示词里的选项段一旦被改坏或漏掉，没有任何东西会报错——
// 只会在几秒后变成一道答错或"未能识别答案"的题。
func TestSolvePromptCarriesWholeQuestion(t *testing.T) {
	fx := newSolveFixture(t, solvedSingleChoice)
	ctx := context.Background()

	q, err := fx.repos.Questions.Get(ctx, fx.detail.ID)
	if err != nil {
		t.Fatalf("读题失败: %v", err)
	}
	var options []model.Option
	if err := q.Options.Into(&options); err != nil {
		t.Fatalf("解析选项失败: %v", err)
	}

	content := solvePromptContent(q, options, nil)

	wants := []string{
		"题型：",                  // 题型是库里已确定的事实，不该让模型猜
		"已知函数",                 // 题干正文
		"$f(x)=x^2$",           // 公式要带 $ 定界符，否则模型把它当散文
		"选项",                   // 选项段本身
		"A. 2", "B. 4", "C. 8", // 每个选项的标签与内容
		"只写选项字母", // 选择题的答案格式约定
	}
	for _, want := range wants {
		if !strings.Contains(content, want) {
			t.Fatalf("发给模型的题目里缺少 %q，实际内容：\n%s", want, content)
		}
	}
}

// 材料题的题目本体挂在小问上。只把母题发给模型，模型拿到的是一段
// 只有背景介绍的文字——它会给出一个凭空编的答案，而且看不出错在哪。
func TestSolvePromptCarriesChildrenOfParent(t *testing.T) {
	fx := newSolveFixture(t, solvedSingleChoice)
	ctx := context.Background()

	childReq := &schema.QuestionCreateRequest{
		SubjectID:    fx.subject.ID,
		ParentID:     &fx.detail.ID,
		QuestionType: model.QuestionTypeFillBlank,
		Stem:         importing.MarkdownToRichDoc("求 $\\lim_{x\\to 0}$ 的值"),
		Answer:       model.AnswerSpec{Kind: model.AnswerKindPending},
	}
	if _, err := NewQuestionService(fx.repos).Create(ctx, fx.editor, childReq); err != nil {
		t.Fatalf("建小问失败: %v", err)
	}

	parent, err := fx.repos.Questions.Get(ctx, fx.detail.ID)
	if err != nil {
		t.Fatalf("读母题失败: %v", err)
	}
	children, err := fx.repos.Questions.Children(ctx, parent.ID)
	if err != nil {
		t.Fatalf("读小问失败: %v", err)
	}

	content := solvePromptContent(parent, nil, children)
	if !strings.Contains(content, "小问") || !strings.Contains(content, "\\lim_{x\\to 0}") {
		t.Fatalf("母题的小问应作为上下文发给模型，实际内容：\n%s", content)
	}
}

// 列表项必须带富文本题干，否则题库里显示的就是 \boldsymbol{a} 这种源码。
func TestQuestionBriefCarriesRichStem(t *testing.T) {
	fx := newSolveFixture(t, solvedSingleChoice)
	ctx := context.Background()

	q, err := fx.repos.Questions.Get(ctx, fx.detail.ID)
	if err != nil {
		t.Fatalf("读题失败: %v", err)
	}
	brief := schema.NewQuestionBrief(q, nil, nil, 0)

	if brief.StemDoc.IsEmpty() {
		t.Fatal("列表项必须带富文本题干，否则列表里公式显示为 LaTeX 源码")
	}
	if !strings.Contains(string(brief.StemDoc), "inlineMath") {
		t.Fatalf("列表项的题干里应保留公式节点，实际 %s", brief.StemDoc)
	}
	if brief.StemText == "" {
		t.Fatal("纯文本投影要留着，它是关键词检索的检索列")
	}
}

// 详情自带 Stem，brief 里那份是给列表用的，重复一份只是撑大响应体。
func TestQuestionDetailDoesNotDuplicateStem(t *testing.T) {
	fx := newSolveFixture(t, solvedSingleChoice)
	ctx := context.Background()

	detail, err := NewQuestionService(fx.repos).Get(ctx, fx.editor, fx.detail.ID)
	if err != nil {
		t.Fatalf("读详情失败: %v", err)
	}
	if !detail.StemDoc.IsEmpty() {
		t.Fatal("详情响应里 stem 已有，brief 不该再带一份 stem_doc")
	}
	if detail.Stem.IsEmpty() {
		t.Fatal("详情必须带题干")
	}
}
