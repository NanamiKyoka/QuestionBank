package importing

import (
	"strings"
	"testing"

	"github.com/NanamiKyoka/questionmind/internal/ai"
)

// 临时 id 带空白是模型常见的输出习惯；两边都带一个空格时，
// 入库阶段就会因为逐字符不相等而把整组材料题散架，且没有任何提示。
func TestNormalizeTrimsTempIDs(t *testing.T) {
	drafts := Normalize([]ai.ExtractedQuestion{
		{TempID: " q1 ", Stem: "母题", Answer: "42", Analysis: "略"},
		{TempID: " c1 ", ParentTempID: " q1 ", Stem: "小问", Answer: "42", Analysis: "略"},
	})
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
	})
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
	})
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
	})
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
