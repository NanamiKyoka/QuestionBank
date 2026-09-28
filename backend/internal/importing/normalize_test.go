package importing

import (
	"strings"
	"testing"

	"github.com/NanamiKyoka/questionmind/internal/ai"
	"github.com/NanamiKyoka/questionmind/internal/model"
)

func TestCoerceQuestionType(t *testing.T) {
	cases := map[string]model.QuestionType{
		"single_choice":   model.QuestionTypeSingleChoice,
		"single":          model.QuestionTypeSingleChoice,
		"single choice":   model.QuestionTypeSingleChoice,
		"single-choice":   model.QuestionTypeSingleChoice,
		"单选题":             model.QuestionTypeSingleChoice,
		"multiple_choice": model.QuestionTypeMultipleChoice,
		"多选":              model.QuestionTypeMultipleChoice,
		"fill_blank":      model.QuestionTypeFillBlank,
		"fill_in_blank":   model.QuestionTypeFillBlank,
		"填空题":             model.QuestionTypeFillBlank,
		"true_false":      model.QuestionTypeTrueFalse,
		"判断":              model.QuestionTypeTrueFalse,
		"essay":           model.QuestionTypeEssay,
		"解答题":             model.QuestionTypeEssay,
		"":                "", // 无法识别 → 交给推断
		"unknown_type":    "",
	}
	for raw, want := range cases {
		if got := coerceQuestionType(raw); got != want {
			t.Fatalf("coerceQuestionType(%q) = %q，期望 %q", raw, got, want)
		}
	}
}

// 用精确映射而不是子串匹配：参考实现里 `Contains(s,"multiple")` 这类判断
// 会让 "single_choice_multiple" 落到错误分支。
func TestCoerceQuestionTypeIsNotSubstringBased(t *testing.T) {
	if got := coerceQuestionType("single_choice_multiple"); got != "" {
		t.Fatalf("混合描述应无法识别而不是误判，实际 %q", got)
	}
	if got := coerceQuestionType("not_essay_at_all"); got != "" {
		t.Fatalf("包含 essay 子串不应被判成解答题，实际 %q", got)
	}
}

func TestExtractChoiceLetters(t *testing.T) {
	labels := []string{"A", "B", "C", "D"}

	valid := map[string][]string{
		"B":     {"B"},
		"b":     {"B"},
		"BD":    {"B", "D"},
		"B,D":   {"B", "D"},
		"B、D":   {"B", "D"},
		"B D":   {"B", "D"},
		"B/D":   {"B", "D"},
		"（B）":   {"B"},
		"(B)":   {"B"},
		"故选B":   {"B"},
		"答案：BD": {"B", "D"},
		"B B":   {"B"}, // 去重
	}
	for raw, want := range valid {
		got := extractChoiceLetters(raw, labels)
		if strings.Join(got, "") != strings.Join(want, "") {
			t.Fatalf("extractChoiceLetters(%q) = %v，期望 %v", raw, got, want)
		}
	}

	// 这些都不该被解析成选项：答案里混了解释文字，或引用了不存在的选项。
	invalid := []string{
		"因为 B 是错的",
		"BD，理由是定义域",
		"Z", // 不在选项集合内
		"1.5",
		"",
	}
	for _, raw := range invalid {
		if got := extractChoiceLetters(raw, labels); len(got) > 0 {
			t.Fatalf("extractChoiceLetters(%q) 不该解析出选项，实际 %v", raw, got)
		}
	}
}

func TestSplitOptionsFromStem(t *testing.T) {
	stem, opts := splitOptionsFromStem("下列哪个是质数？ A. 4 B. 7 C. 9 D. 15")
	if len(opts) != 4 {
		t.Fatalf("应切出 4 个选项，实际 %d：%+v", len(opts), opts)
	}
	if !strings.Contains(stem, "下列哪个是质数") {
		t.Fatalf("题干应保留，实际 %q", stem)
	}
	wantLabels := []string{"A", "B", "C", "D"}
	for i, l := range wantLabels {
		if opts[i].Label != l {
			t.Fatalf("第 %d 个选项标签应为 %s，实际 %s", i, l, opts[i].Label)
		}
	}
	// 选项内容里的标签前缀应被剥掉（这里内容本身就是纯文本）。
	if got := renderDoc(opts[1].Content); got != "7" {
		t.Fatalf("选项内容解析错误: %q", got)
	}
}

func TestSplitOptionsFromStemSupportsCJKSeparators(t *testing.T) {
	_, opts := splitOptionsFromStem("选择正确答案 A、甲 B、乙 C、丙")
	if len(opts) != 3 {
		t.Fatalf("中文顿号分隔应切出 3 个选项，实际 %d", len(opts))
	}
}

// 关键：只有标签连续时才认为是选项。
// 否则一句普通说明里的 "A." 会把文字拆成假选项，而且很难被发现。
func TestSplitOptionsFromStemRejectsNonConsecutiveLabels(t *testing.T) {
	texts := []string{
		"已知点 A. 与点 B. 的距离",
		"若 x = A. 则结果为 C.",
		"选项只有 A. 一个",
		"这是一段没有任何选项标记的说明文字",
	}
	for _, text := range texts {
		if _, opts := splitOptionsFromStem(text); len(opts) > 0 {
			t.Fatalf("不应从 %q 切出选项，实际得到 %d 个", text, len(opts))
		}
	}
}

func TestInferQuestionType(t *testing.T) {
	opts := []model.Option{{Label: "A"}, {Label: "B"}, {Label: "C"}, {Label: "D"}}

	cases := []struct {
		name   string
		stem   string
		opts   []model.Option
		answer string
		want   model.QuestionType
	}{
		{"有选项单答案", "题目", opts, "B", model.QuestionTypeSingleChoice},
		{"有选项多答案", "题目", opts, "BD", model.QuestionTypeMultipleChoice},
		{"判断题答案对", "题目", nil, "对", model.QuestionTypeTrueFalse},
		{"判断题答案错", "题目", nil, "错误", model.QuestionTypeTrueFalse},
		{"判断题答案符号", "题目", nil, "√", model.QuestionTypeTrueFalse},
		{"题干有下划线空位", "求 ___ 的值", nil, "", model.QuestionTypeFillBlank},
		{"题干有全角括号空位", "函数在（  ）上单调递增", nil, "", model.QuestionTypeFillBlank},
		{"其余兜底为解答题", "证明：三角形内角和为 180 度", nil, "略", model.QuestionTypeEssay},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := inferQuestionType(tc.stem, tc.opts, tc.answer); got != tc.want {
				t.Fatalf("推断题型为 %s，期望 %s", got, tc.want)
			}
		})
	}
}

func TestParseBlankAnswers(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		wantCount int
		// 期望第 idx 个空的可接受答案数量
		altCounts []int
	}{
		{"单个空", "3", 1, []int{1}},
		{"分号分隔", "3；4", 2, []int{1, 1}},
		{"换行分隔", "3\n4", 2, []int{1, 1}},
		{"竖线分隔", "3 | 4", 2, []int{1, 1}},
		{"或表示多个可接受答案", "3 或 三", 1, []int{2}},
		{"斜杠表示多个可接受答案", "3/三", 1, []int{2}},
		{"带编号前缀", "(1) 3；(2) 4", 2, []int{1, 1}},
		{"带答案前缀", "答案：3；4", 2, []int{1, 1}},
		{"全角顿号编号", "① 3；② 4", 2, []int{1, 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			blanks := parseBlankAnswers(tc.raw)
			if len(blanks) != tc.wantCount {
				t.Fatalf("应解析出 %d 个空，实际 %d：%+v", tc.wantCount, len(blanks), blanks)
			}
			for i, wantAlts := range tc.altCounts {
				if i >= len(blanks) {
					break
				}
				if len(blanks[i].Accepts) != wantAlts {
					t.Fatalf("第 %d 个空应有 %d 组可接受答案，实际 %d",
						i+1, wantAlts, len(blanks[i].Accepts))
				}
			}
			// 空位 id 必须按顺序生成，供前端与题干空位对应。
			for i := range blanks {
				if blanks[i].ID != blankID(i+1) {
					t.Fatalf("第 %d 个空的 id 应为 %s，实际 %s", i+1, blankID(i+1), blanks[i].ID)
				}
			}
		})
	}
}

func TestNormalizeFullQuestion(t *testing.T) {
	drafts := Normalize([]ai.ExtractedQuestion{{
		QuestionType: "single_choice",
		Stem:         "下列哪个数是质数？",
		Options: []ai.ExtractedOption{
			{Label: "A", Content: "4"},
			{Label: "B", Content: "7"},
			{Label: "C", Content: "9"},
			{Label: "D", Content: "15"},
		},
		Answer:          "B",
		Analysis:        "7 只有 1 和它本身两个因数。",
		Difficulty:      2,
		KnowledgePoints: []string{"质数"},
		Tags:            []string{"基础"},
	}})

	if len(drafts) != 1 {
		t.Fatalf("应产出 1 个草稿，实际 %d", len(drafts))
	}
	d := drafts[0]

	if d.QuestionType != model.QuestionTypeSingleChoice {
		t.Fatalf("题型错误: %s", d.QuestionType)
	}
	if len(d.Options) != 4 {
		t.Fatalf("应保留 4 个选项，实际 %d", len(d.Options))
	}
	// 选项 id 应由内容哈希生成。
	for i := range d.Options {
		if !strings.HasPrefix(d.Options[i].ID, "opt_") {
			t.Fatalf("选项 id 格式错误: %s", d.Options[i].ID)
		}
	}
	if d.Answer.Kind != "single" {
		t.Fatalf("答案类型错误: %s", d.Answer.Kind)
	}
	if len(d.Answer.Correct) != 1 || d.Answer.Correct[0] != "B" {
		t.Fatalf("答案应为标签 B，实际 %v", d.Answer.Correct)
	}
	if d.Difficulty != 2 {
		t.Fatalf("难度应为 2，实际 %d", d.Difficulty)
	}
	if got := renderDoc(d.Stem); got != "下列哪个数是质数？" {
		t.Fatalf("题干转换错误: %q", got)
	}
	if d.StemText == "" || strings.Contains(d.StemText, "A.") {
		t.Fatalf("题干纯文本应是题目本身，实际 %q", d.StemText)
	}
	if !strings.Contains(renderDoc(d.Analysis), "因数") {
		t.Fatal("解析内容丢失")
	}
	if len(d.Warnings) != 0 {
		t.Fatalf("正常情况下不应有警告，实际 %v", d.Warnings)
	}
}

// 模型只给题干（选项嵌在里面）时，要能切出来并提示用户核对。
func TestNormalizeSplitsOptionsAndWarns(t *testing.T) {
	drafts := Normalize([]ai.ExtractedQuestion{{
		Stem:   "下列哪个是偶数？ A. 3 B. 8 C. 5 D. 7",
		Answer: "B",
	}})
	d := drafts[0]

	if len(d.Options) != 4 {
		t.Fatalf("应切出 4 个选项，实际 %d", len(d.Options))
	}
	if d.QuestionType != model.QuestionTypeSingleChoice {
		t.Fatalf("应推断为单选，实际 %s", d.QuestionType)
	}
	if !hasWarning(d.Warnings, "自动切分") {
		t.Fatalf("切分选项应给出提示，实际 %v", d.Warnings)
	}
}

// 无法确定的答案不要瞎猜：标记为待人工处理，并给出警告。
func TestNormalizeWarnsOnUnparseableAnswer(t *testing.T) {
	drafts := Normalize([]ai.ExtractedQuestion{{
		QuestionType: "single_choice",
		Stem:         "题目",
		Options: []ai.ExtractedOption{
			{Label: "A", Content: "甲"}, {Label: "B", Content: "乙"},
		},
		Answer: "因为乙更符合题意", // 混了解释，不该被解析成字母
	}})
	d := drafts[0]

	if len(d.Answer.Correct) != 0 {
		t.Fatalf("不该猜出答案，实际 %v", d.Answer.Correct)
	}
	if !hasWarning(d.Warnings, "未能识别答案") {
		t.Fatalf("应提示需要人工选择，实际 %v", d.Warnings)
	}
}

func TestNormalizeWarnsOnBlankCountMismatch(t *testing.T) {
	drafts := Normalize([]ai.ExtractedQuestion{{
		QuestionType: "fill_blank",
		Stem:         "求 ___ 与 ___ 的和",
		Answer:       "3",
	}})
	d := drafts[0]

	if len(d.Answer.Blanks) != 1 {
		t.Fatalf("应解析出 1 个空，实际 %d", len(d.Answer.Blanks))
	}
	if !hasWarning(d.Warnings, "空位数量") {
		t.Fatalf("空位数量不一致应给出警告，实际 %v", d.Warnings)
	}
}

func TestNormalizeTrueFalseVariants(t *testing.T) {
	cases := map[string]bool{
		"对": true, "正确": true, "√": true, "是": true, "T": true, "true": true,
		"错": false, "错误": false, "×": false, "否": false, "F": false, "false": false,
	}
	for raw, want := range cases {
		drafts := Normalize([]ai.ExtractedQuestion{{
			QuestionType: "true_false", Stem: "题目", Answer: raw,
		}})
		d := drafts[0]
		if d.Answer.BoolAnswer == nil {
			t.Fatalf("答案 %q 应被解析为布尔值", raw)
		}
		if *d.Answer.BoolAnswer != want {
			t.Fatalf("答案 %q 应解析为 %v", raw, want)
		}
	}
}

func TestNormalizeEssayWithoutReference(t *testing.T) {
	drafts := Normalize([]ai.ExtractedQuestion{{
		QuestionType: "essay", Stem: "证明题", Answer: "",
	}})
	d := drafts[0]
	if d.Answer.Kind != "reference" {
		t.Fatalf("解答题答案类型应为 reference，实际 %s", d.Answer.Kind)
	}
	if !hasWarning(d.Warnings, "未给出参考答案") {
		t.Fatalf("应提示缺少参考答案，实际 %v", d.Warnings)
	}
}

func TestNormalizeInfersTypeWhenModelOmitsIt(t *testing.T) {
	drafts := Normalize([]ai.ExtractedQuestion{{
		Stem:   "求 ___ 的值",
		Answer: "42",
	}})
	d := drafts[0]
	if d.QuestionType == "" {
		t.Fatal("题型不应为空")
	}
	if !hasWarning(d.Warnings, "推断") {
		t.Fatalf("推断出的题型应提示用户核对，实际 %v", d.Warnings)
	}
}

// 难度超出 1-5 时回落为默认值，而不是原样落库。
func TestNormalizeDifficultyClamped(t *testing.T) {
	for _, in := range []int{0, -1, 99} {
		drafts := Normalize([]ai.ExtractedQuestion{{Stem: "x", Difficulty: in}})
		if got := drafts[0].Difficulty; got != 3 {
			t.Fatalf("难度 %d 应回落为 3，实际 %d", in, got)
		}
	}
	for _, in := range []int{1, 5} {
		drafts := Normalize([]ai.ExtractedQuestion{{Stem: "x", Difficulty: in}})
		if got := drafts[0].Difficulty; got != in {
			t.Fatalf("合法难度 %d 应保留，实际 %d", in, got)
		}
	}
}

// 非法或重复的选项标签不应导致整题丢失。
func TestNormalizeHandlesBadOptions(t *testing.T) {
	drafts := Normalize([]ai.ExtractedQuestion{{
		QuestionType: "single_choice",
		Stem:         "题目",
		Options: []ai.ExtractedOption{
			{Label: "A", Content: "甲"},
			{Label: "A", Content: "乙"}, // 重复标签
		},
		Answer: "A",
	}})
	d := drafts[0]
	if len(d.Options) != 0 {
		t.Fatalf("重复标签应被忽略，实际保留 %d 个选项", len(d.Options))
	}
	if !hasWarning(d.Warnings, "选项标签重复") {
		t.Fatalf("应给出警告，实际 %v", d.Warnings)
	}
	// 题目本身仍在，用户可以在界面上修正。
	if renderDoc(d.Stem) != "题目" {
		t.Fatal("题干不应丢失")
	}
}

// 端到端：模拟一次真实的模型返回。
func TestNormalizeRealisticBatch(t *testing.T) {
	drafts := Normalize([]ai.ExtractedQuestion{
		{
			TempID:       "q1",
			QuestionType: "单选题",
			Stem:         "已知函数 $f(x)=x^2$，则 $f(2)=$（  ）",
			Options: []ai.ExtractedOption{
				{Label: "A", Content: "2"},
				{Label: "B", Content: "4"},
				{Label: "C", Content: "8"},
				{Label: "D", Content: "16"},
			},
			Answer: "B", Analysis: "代入得 $f(2)=4$。", Difficulty: 1,
		},
		{
			TempID:       "q2",
			QuestionType: "填空题",
			Stem:         "三角形的内角和为 ______ 度。",
			Answer:       "180",
		},
		{
			TempID:       "q3",
			QuestionType: "判断题",
			Stem:         "所有的质数都是奇数。",
			Answer:       "错",
			Analysis:     "2 是质数但不是奇数。",
		},
	})

	if len(drafts) != 3 {
		t.Fatalf("应产出 3 个草稿，实际 %d", len(drafts))
	}
	if drafts[0].QuestionType != model.QuestionTypeSingleChoice {
		t.Fatalf("第 1 题题型错误: %s", drafts[0].QuestionType)
	}
	if got := renderDoc(drafts[0].Stem); !strings.Contains(got, "$f(x)=x^2$") {
		t.Fatalf("题干里的公式应被保留: %q", got)
	}
	if drafts[1].QuestionType != model.QuestionTypeFillBlank || len(drafts[1].Answer.Blanks) != 1 {
		t.Fatalf("第 2 题解析错误: type=%s blanks=%d",
			drafts[1].QuestionType, len(drafts[1].Answer.Blanks))
	}
	if drafts[2].Answer.BoolAnswer == nil || *drafts[2].Answer.BoolAnswer {
		t.Fatal("第 3 题答案应为错误")
	}
}

func hasWarning(warnings []string, substr string) bool {
	for _, w := range warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}
