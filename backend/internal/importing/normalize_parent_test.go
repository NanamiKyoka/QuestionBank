package importing

import (
	"strings"
	"testing"

	"github.com/NanamiKyoka/questionmind/internal/ai"
	"github.com/NanamiKyoka/questionmind/internal/model"
)

// 临时 id 带空白是模型常见的输出习惯；两边都带一个空格时，
// 入库阶段就会因为逐字符不相等而把整组材料题散架，且没有任何提示。
func TestNormalizeTrimsTempIDs(t *testing.T) {
	drafts := Normalize([]ai.ExtractedQuestion{
		{TempID: " q1 ", Stem: "母题", Answer: "42", Analysis: "略"},
		{TempID: " c1 ", ParentTempID: " q1 ", Stem: "小问", Answer: "42", Analysis: "略"},
	}, ModeExtract)
	if len(drafts) != 2 {
		t.Fatalf("应得到 2 道草稿，实际 %d", len(drafts))
	}
	if drafts[0].TempID != "q1" || drafts[1].TempID != "c1" {
		t.Fatalf("临时 id 应被 trim：%q / %q", drafts[0].TempID, drafts[1].TempID)
	}
	if drafts[1].ParentTempID != "q1" {
		t.Fatalf("母题引用应被 trim，实际 %q", drafts[1].ParentTempID)
	}
}

// 母题只抽出了小问时，必须明确警告，而不是让人以为母子结构保住了。
func TestNormalizeWarnsOnUnknownParent(t *testing.T) {
	drafts := Normalize([]ai.ExtractedQuestion{
		{TempID: "c1", ParentTempID: "missing", Stem: "小问", Answer: "42", Analysis: "略"},
	}, ModeExtract)
	if drafts[0].ParentTempID != "" {
		t.Fatalf("无法解析的母题引用应被清空，实际 %q", drafts[0].ParentTempID)
	}
	if !containsWarning(drafts[0].Warnings, "母题") {
		t.Fatalf("应附上母题缺失的警告，实际 %v", drafts[0].Warnings)
	}
}

// 自引用同样无法建立父子关系。
func TestNormalizeWarnsOnSelfParent(t *testing.T) {
	drafts := Normalize([]ai.ExtractedQuestion{
		{TempID: "a", ParentTempID: "a", Stem: "题", Answer: "42", Analysis: "略"},
	}, ModeExtract)
	if drafts[0].ParentTempID != "" {
		t.Fatalf("自引用应被清空，实际 %q", drafts[0].ParentTempID)
	}
	if !containsWarning(drafts[0].Warnings, "自身") {
		t.Fatalf("应附上自引用的警告，实际 %v", drafts[0].Warnings)
	}
}

// 正常的母子关系必须原样保留，不能被"顺手清理"掉。
func TestNormalizeKeepsValidParentLink(t *testing.T) {
	drafts := Normalize([]ai.ExtractedQuestion{
		{TempID: "m1", QuestionType: "essay", Stem: "母题", Answer: "42", Analysis: "略"},
		{TempID: "c1", ParentTempID: "m1", QuestionType: "essay", Stem: "小问", Answer: "42", Analysis: "略"},
	}, ModeExtract)
	if drafts[1].ParentTempID != "m1" {
		t.Fatalf("合法母子关系应保留，实际 %q", drafts[1].ParentTempID)
	}
	if len(drafts[1].Warnings) != 0 {
		t.Fatalf("合法关系不应带警告，实际 %v", drafts[1].Warnings)
	}
}

func containsWarning(warnings []string, substr string) bool {
	for _, w := range warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}

// 子题自己的编号通常是 (1)(2)，入库时应继承母题的原卷题号。
func TestNormalizeChildInheritsParentSourceNumber(t *testing.T) {
	drafts := Normalize([]ai.ExtractedQuestion{
		{TempID: "m15", QuestionType: "essay", Stem: "15. 已知函数 f(x)。"},
		{TempID: "c1", ParentTempID: "m15", QuestionType: "essay", Stem: "(1) 求 f(0)。"},
		{TempID: "c2", ParentTempID: "m15", QuestionType: "essay", Stem: "(2) 证明 f(x) 单调递增。"},
	}, ModeRecord)
	if drafts[0].SourceNumber != 15 {
		t.Fatalf("母题题号错误，实际 %d", drafts[0].SourceNumber)
	}
	for i := 1; i < len(drafts); i++ {
		if drafts[i].SourceNumber != 15 {
			t.Fatalf("子题应继承母题题号 15，实际 %d", drafts[i].SourceNumber)
		}
	}
}

// 材料标题在题号之前时，仍要按原卷题号而不是数组下标。
func TestNormalizeFindsSourceNumberAfterMaterialHeading(t *testing.T) {
	drafts := Normalize([]ai.ExtractedQuestion{
		{
			TempID:       "q15",
			QuestionType: "single_choice",
			Stem:         "阅读诗歌，完成下面小题。 风 司马光 15．下列对这首诗的理解和赏析，不正确的一项是（ ）",
			Options: []ai.ExtractedOption{
				{Label: "A", Content: "甲"},
				{Label: "B", Content: "乙"},
			},
		},
	}, ModeRecord)
	if drafts[0].SourceNumber != 15 {
		t.Fatalf("应识别材料标题后的原卷题号 15，实际 %d", drafts[0].SourceNumber)
	}
}

func TestNormalizeRefinesSentenceBreakQuestion(t *testing.T) {
	drafts := Normalize([]ai.ExtractedQuestion{{
		TempID:       "q10",
		QuestionType: "multiple_choice",
		Stem:         "材料二画波浪线的部分有三处需要断句，请将相应位置的答案标号涂黑。",
	}}, ModeRecord)
	if drafts[0].QuestionType != model.QuestionTypeFillBlank {
		t.Fatalf("断句题应归为填空题，实际 %s", drafts[0].QuestionType)
	}
	if !containsWarning(drafts[0].Warnings, "断句题") {
		t.Fatalf("应提示题型已修正，实际 %v", drafts[0].Warnings)
	}
}
