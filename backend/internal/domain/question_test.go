package domain

import (
	"strings"
	"testing"

	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
)

// doc 构造一个最简 RichDoc：单个段落，包含给定文本。
func doc(t *testing.T, text string) RichDoc {
	t.Helper()
	return RichDoc{
		Type: "doc",
		Content: []Node{{
			Type:    "paragraph",
			Content: []Node{{Type: "text", Text: text}},
		}},
	}
}

// docWithBlanks 构造含 n 个空位的题干。
func docWithBlanks(t *testing.T, text string, n int) RichDoc {
	t.Helper()
	inner := []Node{{Type: "text", Text: text}}
	for i := 0; i < n; i++ {
		inner = append(inner, Node{Type: "blank"})
	}
	return RichDoc{Type: "doc", Content: []Node{{Type: "paragraph", Content: inner}}}
}

func jsonOf(t *testing.T, v any) model.JSON {
	t.Helper()
	j, err := model.JSONOf(v)
	if err != nil {
		t.Fatalf("构造 JSON 失败: %v", err)
	}
	return j
}

func options(t *testing.T, labels ...string) []model.Option {
	t.Helper()
	opts := make([]model.Option, 0, len(labels))
	for _, l := range labels {
		opts = append(opts, model.Option{Label: l, Content: jsonOf(t, doc(t, "内容"+l))})
	}
	normalized, err := NormalizeOptions(opts)
	if err != nil {
		t.Fatalf("归一化选项失败: %v", err)
	}
	return normalized
}

func TestOptionIDIsStableAndContentSensitive(t *testing.T) {
	a := OptionID(0, "A", "内容")
	b := OptionID(0, "A", "内容")
	if a != b {
		t.Fatalf("相同输入应得到相同 id: %s != %s", a, b)
	}

	if !strings.HasPrefix(a, "opt_") || len(a) != len("opt_")+8 {
		t.Fatalf("id 格式应为 opt_ + 8 位十六进制，实际 %q", a)
	}

	if OptionID(1, "A", "内容") == a {
		t.Fatal("序号不同应产生不同 id")
	}
	if OptionID(0, "B", "内容") == a {
		t.Fatal("标签不同应产生不同 id")
	}
	if OptionID(0, "A", "别的内容") == a {
		t.Fatal("内容不同应产生不同 id")
	}
}

func TestNormalizeOptionsRejectsInvalidLabels(t *testing.T) {
	if _, err := NormalizeOptions([]model.Option{{Label: "A"}, {Label: "A"}}); err == nil {
		t.Fatal("重复标签应被拒绝")
	}
	if _, err := NormalizeOptions([]model.Option{{Label: "  "}}); err == nil {
		t.Fatal("空标签应被拒绝")
	}
}

// 选项 id 由内容哈希决定，因此"重新保存同一道题"不会让答案引用失效。
func TestNormalizeOptionsIsIdempotent(t *testing.T) {
	first := options(t, "A", "B", "C")
	second := options(t, "A", "B", "C")
	for i := range first {
		if first[i].ID != second[i].ID {
			t.Fatalf("第 %d 个选项 id 不稳定: %s vs %s", i, first[i].ID, second[i].ID)
		}
	}
}

func TestResolveCorrectLabels(t *testing.T) {
	opts := options(t, "A", "B", "C")

	t.Run("标签解析为 id", func(t *testing.T) {
		ans := model.AnswerSpec{Kind: "single", Correct: []string{"b"}}
		if err := ResolveCorrectLabels(opts, &ans); err != nil {
			t.Fatalf("不应报错: %v", err)
		}
		if ans.Correct[0] != opts[1].ID {
			t.Fatalf("期望 %s，实际 %s", opts[1].ID, ans.Correct[0])
		}
	})

	t.Run("已经是 id 时保持不变", func(t *testing.T) {
		ans := model.AnswerSpec{Kind: "single", Correct: []string{opts[2].ID}}
		if err := ResolveCorrectLabels(opts, &ans); err != nil {
			t.Fatalf("不应报错: %v", err)
		}
		if ans.Correct[0] != opts[2].ID {
			t.Fatal("id 不应被改动")
		}
	})

	t.Run("未知引用被拒绝", func(t *testing.T) {
		ans := model.AnswerSpec{Kind: "single", Correct: []string{"Z"}}
		err := ResolveCorrectLabels(opts, &ans)
		if err == nil {
			t.Fatal("未知引用应被拒绝")
		}
		if !apperr.Is(err, apperr.CodeAnswerMismatch) {
			t.Fatalf("期望 422 语义错误，实际 %v", err)
		}
	})
}

func TestValidateShape(t *testing.T) {
	opts := options(t, "A", "B")

	cases := []struct {
		name    string
		qtype   model.QuestionType
		opts    []model.Option
		answer  model.AnswerSpec
		wantErr bool
	}{
		{"合法题型通过", model.QuestionTypeSingleChoice, opts, model.AnswerSpec{Kind: "single"}, false},
		{"非法题型", model.QuestionType("unknown"), opts, model.AnswerSpec{Kind: "single"}, true},
		{"选择题 0 选项", model.QuestionTypeSingleChoice, nil, model.AnswerSpec{Kind: "single"}, true},
		{"选择题 1 选项", model.QuestionTypeSingleChoice, opts[:1], model.AnswerSpec{Kind: "single"}, true},
		{"非选择题不应带选项", model.QuestionTypeEssay, opts, model.AnswerSpec{Kind: "reference"}, true},
		{"答案种类不符", model.QuestionTypeSingleChoice, opts, model.AnswerSpec{Kind: "boolean"}, true},
		{"unresolved 被拒绝", model.QuestionTypeSingleChoice, opts, model.AnswerSpec{Kind: "unresolved"}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateShape(tc.qtype, tc.opts, tc.answer)
			if tc.wantErr && err == nil {
				t.Fatal("期望报错但没有")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("不应报错: %v", err)
			}
		})
	}
}

func TestValidateQuestion(t *testing.T) {
	opts := options(t, "A", "B", "C")
	yes := true

	t.Run("单选恰好一个正确选项", func(t *testing.T) {
		if err := ValidateQuestion(model.QuestionTypeSingleChoice, doc(t, "x"),
			opts, model.AnswerSpec{Kind: "single", Correct: []string{opts[0].ID}}); err != nil {
			t.Fatalf("不应报错: %v", err)
		}
	})

	t.Run("单选两个正确选项被拒绝", func(t *testing.T) {
		err := ValidateQuestion(model.QuestionTypeSingleChoice, doc(t, "x"),
			opts, model.AnswerSpec{Kind: "single", Correct: []string{opts[0].ID, opts[1].ID}})
		if !apperr.Is(err, apperr.CodeInvalidAnswer) {
			t.Fatalf("期望 CodeInvalidAnswer，实际 %v", err)
		}
	})

	t.Run("单选引用不在选项中的 id", func(t *testing.T) {
		err := ValidateQuestion(model.QuestionTypeSingleChoice, doc(t, "x"),
			opts, model.AnswerSpec{Kind: "single", Correct: []string{"opt_deadbeef"}})
		if !apperr.Is(err, apperr.CodeAnswerMismatch) {
			t.Fatalf("期望 CodeAnswerMismatch，实际 %v", err)
		}
	})

	t.Run("多选重复选项被拒绝", func(t *testing.T) {
		err := ValidateQuestion(model.QuestionTypeMultipleChoice, doc(t, "x"),
			opts, model.AnswerSpec{Kind: "multiple", Correct: []string{opts[0].ID, opts[0].ID}})
		if !apperr.Is(err, apperr.CodeInvalidAnswer) {
			t.Fatalf("期望 CodeInvalidAnswer，实际 %v", err)
		}
	})

	t.Run("判断题缺答案", func(t *testing.T) {
		err := ValidateQuestion(model.QuestionTypeTrueFalse, doc(t, "x"), nil, model.AnswerSpec{Kind: "boolean"})
		if !apperr.Is(err, apperr.CodeInvalidAnswer) {
			t.Fatalf("期望 CodeInvalidAnswer，实际 %v", err)
		}
	})

	t.Run("判断题正常", func(t *testing.T) {
		if err := ValidateQuestion(model.QuestionTypeTrueFalse, doc(t, "x"), nil,
			model.AnswerSpec{Kind: "boolean", BoolAnswer: &yes}); err != nil {
			t.Fatalf("不应报错: %v", err)
		}
	})

	t.Run("填空答案数与题干空位数不一致", func(t *testing.T) {
		stem := docWithBlanks(t, "求 x：", 3)
		err := ValidateQuestion(model.QuestionTypeFillBlank, stem, nil,
			model.AnswerSpec{Kind: "blanks", Blanks: []model.Blank{
				{ID: "blk_1", Accepts: [][]model.JSON{{jsonOf(t, doc(t, "1"))}}},
			}})
		if !apperr.Is(err, apperr.CodeAnswerMismatch) {
			t.Fatalf("期望 CodeAnswerMismatch，实际 %v", err)
		}
	})

	t.Run("填空数量一致则通过", func(t *testing.T) {
		stem := docWithBlanks(t, "求 x：", 2)
		err := ValidateQuestion(model.QuestionTypeFillBlank, stem, nil,
			model.AnswerSpec{Kind: "blanks", Blanks: []model.Blank{
				{ID: "blk_1", Accepts: [][]model.JSON{{jsonOf(t, doc(t, "1"))}}},
				{ID: "blk_2", Accepts: [][]model.JSON{{jsonOf(t, doc(t, "2"))}}},
			}})
		if err != nil {
			t.Fatalf("不应报错: %v", err)
		}
	})

	t.Run("填空空组被拒绝", func(t *testing.T) {
		err := ValidateQuestion(model.QuestionTypeFillBlank, doc(t, "x"), nil,
			model.AnswerSpec{Kind: "blanks", Blanks: []model.Blank{{ID: "blk_1", Accepts: [][]model.JSON{{}}}}})
		if !apperr.Is(err, apperr.CodeInvalidAnswer) {
			t.Fatalf("期望 CodeInvalidAnswer，实际 %v", err)
		}
	})

	t.Run("解答题缺参考答案", func(t *testing.T) {
		err := ValidateQuestion(model.QuestionTypeEssay, doc(t, "x"), nil, model.AnswerSpec{Kind: "reference"})
		if !apperr.Is(err, apperr.CodeInvalidAnswer) {
			t.Fatalf("期望 CodeInvalidAnswer，实际 %v", err)
		}
	})

	t.Run("解答题正常", func(t *testing.T) {
		err := ValidateQuestion(model.QuestionTypeEssay, doc(t, "x"), nil,
			model.AnswerSpec{Kind: "reference", Reference: jsonOf(t, doc(t, "参考答案"))})
		if err != nil {
			t.Fatalf("不应报错: %v", err)
		}
	})
}

func TestPlainText(t *testing.T) {
	d := RichDoc{Type: "doc", Content: []Node{
		{Type: "paragraph", Content: []Node{{Type: "text", Text: "第一段"}}},
		{Type: "table", Content: []Node{
			{Type: "tableRow", Content: []Node{
				{Type: "tableCell", Content: []Node{{Type: "text", Text: "甲"}}},
				{Type: "tableCell", Content: []Node{{Type: "text", Text: "乙"}}},
			}},
		}},
		{Type: "paragraph", Content: []Node{
			{Type: "text", Text: "换行"},
			{Type: "hardBreak"},
			{Type: "text", Text: "之后"},
		}},
	}}

	got := PlainText(d)
	for _, want := range []string{"第一段", "甲", "乙", "换行", "之后"} {
		if !strings.Contains(got, want) {
			t.Fatalf("纯文本应包含 %q，实际 %q", want, got)
		}
	}
}

func TestCountBlanks(t *testing.T) {
	if n := CountBlanks(docWithBlanks(t, "x", 3)); n != 3 {
		t.Fatalf("期望 3 个空位，实际 %d", n)
	}
	if n := CountBlanks(doc(t, "没有空位")); n != 0 {
		t.Fatalf("期望 0 个空位，实际 %d", n)
	}
}
