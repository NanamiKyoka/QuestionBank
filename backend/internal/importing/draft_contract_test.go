package importing

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/NanamiKyoka/questionmind/internal/ai"
)

// 非选择题草稿的选项必须是空数组而不是 null。
//
// 草稿卡按 options.length 渲染，null 会让整张审核卡抛异常——
// 用户看到的是"导入完了但审核界面打不开"，而不是"这道题没选项"。
func TestDraftsSerializeEmptyOptions(t *testing.T) {
	cases := []struct {
		name  string
		input ai.ExtractedQuestion
	}{
		{
			name:  "解答题",
			input: ai.ExtractedQuestion{QuestionType: "essay", Stem: "证明 x^2>=2x-1", Answer: "配方即可"},
		},
		{
			name:  "填空题",
			input: ai.ExtractedQuestion{QuestionType: "fill_blank", Stem: "3 的平方是（  ）", Answer: "9"},
		},
		{
			name:  "判断题",
			input: ai.ExtractedQuestion{QuestionType: "true_false", Stem: "0 是自然数", Answer: "正确"},
		},
		{
			name:  "选项标签重复被丢弃",
			input: ai.ExtractedQuestion{QuestionType: "single_choice", Stem: "题目", Answer: "A", Options: []ai.ExtractedOption{{Label: "A", Content: "甲"}, {Label: "A", Content: "乙"}}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			drafts := Normalize([]ai.ExtractedQuestion{tc.input}, ModeExtract)
			if len(drafts) != 1 {
				t.Fatalf("应产出 1 道草稿，实际 %d", len(drafts))
			}
			if drafts[0].Options == nil {
				t.Errorf("Options 是 nil，应该是非 nil 的空切片")
			}

			raw, err := json.Marshal(drafts[0])
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

// 选择题的选项仍然要照常返回。
func TestDraftsKeepChoiceOptions(t *testing.T) {
	drafts := Normalize([]ai.ExtractedQuestion{{
		QuestionType: "single_choice",
		Stem:         "已知 f(x)=x^2，则 f(2)=（  ）",
		Options: []ai.ExtractedOption{
			{Label: "A", Content: "2"},
			{Label: "B", Content: "4"},
			{Label: "C", Content: "8"},
			{Label: "D", Content: "16"},
		},
		Answer: "B",
	}}, ModeExtract)

	d := drafts[0]
	if len(d.Options) != 4 {
		t.Fatalf("应保留 4 个选项，实际 %d", len(d.Options))
	}
	for i := range d.Options {
		if !strings.HasPrefix(d.Options[i].ID, "opt_") {
			t.Fatalf("选项 id 格式错误: %s", d.Options[i].ID)
		}
	}
}
