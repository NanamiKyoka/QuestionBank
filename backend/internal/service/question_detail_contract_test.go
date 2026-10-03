package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/NanamiKyoka/questionmind/internal/importing"
	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/schema"
)

// 非选择题的选项必须是空数组而不是 null。
//
// 这个契约一旦破掉，前端读 detail.options.length 会抛
// "Cannot read properties of null"，题库里除了选择题以外的题
// 全部打不开详情、也进不了编辑——而选择题是好的，
// 于是看起来像"按题型坏了"，很难往序列化上想。
func TestNonChoiceQuestionDetailSerializesEmptyOptions(t *testing.T) {
	fx := newReviewFixture(t, 1)
	ctx := context.Background()

	cases := []struct {
		name     string
		qtype    model.QuestionType
		answer   model.AnswerSpec
		stemText string
	}{
		{
			name:     "解答题",
			qtype:    model.QuestionTypeEssay,
			answer:   model.AnswerSpec{Kind: "reference", Reference: importing.MarkdownToRichDoc("见解析")},
			stemText: "证明 x^2>=2x-1",
		},
		{
			name:  "填空题",
			qtype: model.QuestionTypeFillBlank,
			answer: model.AnswerSpec{Kind: "blanks", Blanks: []model.Blank{
				{ID: "blk_1", Accepts: [][]model.JSON{{importing.MarkdownToRichDoc("3")}}},
			}},
			stemText: "3 的平方是（  ）",
		},
		{
			name:     "判断题",
			qtype:    model.QuestionTypeTrueFalse,
			answer:   model.AnswerSpec{Kind: "boolean", BoolAnswer: boolPtr(true)},
			stemText: "0 是自然数",
		},
		{
			name:     "仅录题留下的空答案",
			qtype:    model.QuestionTypeEssay,
			answer:   model.AnswerSpec{Kind: model.AnswerKindPending},
			stemText: "求数列前 n 项和",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			created, err := fx.svc.Create(ctx, fx.editor, &schema.QuestionCreateRequest{
				SubjectID:    fx.subject.ID,
				QuestionType: tc.qtype,
				Stem:         importing.MarkdownToRichDoc(tc.stemText),
				Answer:       tc.answer,
			})
			if err != nil {
				t.Fatalf("建题失败: %v", err)
			}

			detail, err := fx.svc.Get(ctx, fx.editor, created.ID)
			if err != nil {
				t.Fatalf("读详情失败: %v", err)
			}
			if detail.Options == nil {
				t.Errorf("Options 是 nil，应该是非 nil 的空切片")
			}
			if len(detail.Options) != 0 {
				t.Errorf("非选择题不该有选项，实际 %d 个", len(detail.Options))
			}

			raw, err := json.Marshal(detail)
			if err != nil {
				t.Fatalf("序列化失败: %v", err)
			}
			if strings.Contains(string(raw), `"options":null`) {
				t.Errorf("响应体里出现了 \"options\":null：%s", raw)
			}
			if !strings.Contains(string(raw), `"options":[]`) {
				t.Errorf("响应体里没有 \"options\":[]：%s", raw)
			}
		})
	}
}

// 选择题的选项仍然要照常返回，别让空切片约定把真选项也吞了。
func TestChoiceQuestionDetailKeepsOptions(t *testing.T) {
	fx := newReviewFixture(t, 1)
	ctx := context.Background()

	created, err := fx.svc.Create(ctx, fx.editor, &schema.QuestionCreateRequest{
		SubjectID:    fx.subject.ID,
		QuestionType: model.QuestionTypeSingleChoice,
		Stem:         importing.MarkdownToRichDoc("已知 f(x)=x^2，则 f(2)=（  ）"),
		Options: []schema.OptionInput{
			{Label: "A", Content: importing.MarkdownToRichDoc("2")},
			{Label: "B", Content: importing.MarkdownToRichDoc("4")},
		},
		Answer: model.AnswerSpec{Kind: "single", Correct: []string{"B"}},
	})
	if err != nil {
		t.Fatalf("建题失败: %v", err)
	}

	detail, err := fx.svc.Get(ctx, fx.editor, created.ID)
	if err != nil {
		t.Fatalf("读详情失败: %v", err)
	}
	if len(detail.Options) != 2 {
		t.Fatalf("选项数 = %d，期望 2", len(detail.Options))
	}
	if detail.Options[0].Label != "A" || detail.Options[1].Label != "B" {
		t.Errorf("选项内容不对: %+v", detail.Options)
	}
}

// 试卷投影走的是另一条路（冻结快照），但对外暴露同一个 options 契约。
func TestPaperProjectionSerializesEmptyOptions(t *testing.T) {
	projection := projectionFromFrozen(frozenQuestion{
		ID:                7,
		QuestionType:      model.QuestionTypeEssay,
		QuestionTypeLabel: "解答题",
		Stem:              importing.MarkdownToRichDoc("证明题"),
		Answer:            model.MustJSONOf(model.AnswerSpec{Kind: "reference"}),
	}, map[uint]int64{}, nil)

	if projection.Options == nil {
		t.Fatalf("Options 是 nil，应该是非 nil 的空切片")
	}
	raw, err := json.Marshal(projection)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	if strings.Contains(string(raw), `"options":null`) {
		t.Errorf("试卷投影里出现了 \"options\":null：%s", raw)
	}
}
