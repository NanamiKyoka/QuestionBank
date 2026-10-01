package service

import (
	"testing"

	"github.com/NanamiKyoka/questionmind/internal/ai"
)

// 模型在每一块里都从 1 开始编号。不改的话，入库时 draftDepths
// 会把后一块的"母题 1"认成前一块的，母子关系直接错位——
// 而这种错位在界面上完全看不出来，只会在删除母题时炸开。
func TestReidentifyMakesTempIDsGloballyUnique(t *testing.T) {
	first := reidentify(0, []ai.ExtractedQuestion{
		{TempID: "q1", Stem: "第一块第一题"},
		{TempID: "q2", Stem: "第一块第二题"},
	})
	second := reidentify(2, []ai.ExtractedQuestion{
		{TempID: "q1", Stem: "第二块第一题"},
		{TempID: "q2", Stem: "第二块第二题"},
	})

	seen := map[string]bool{}
	for _, q := range append(first, second...) {
		if q.TempID == "" {
			t.Fatalf("重编号后不应出现空 temp_id：%q", q.Stem)
		}
		if seen[q.TempID] {
			t.Fatalf("temp_id 重复：%q", q.TempID)
		}
		seen[q.TempID] = true
	}
}

// 母题排在子题**后面**时也要正确改写——模型确实这么写过。
func TestReidentifyFixesParentsDeclaredLater(t *testing.T) {
	got := reidentify(0, []ai.ExtractedQuestion{
		{TempID: "a1", ParentTempID: "a0", Stem: "子题"},
		{TempID: "a0", Stem: "母题"},
	})
	if got[1].TempID == "a0" {
		t.Fatal("母题应被换成新的全局编号")
	}
	if got[0].ParentTempID != got[1].TempID {
		t.Fatalf("子题应指向新的母题编号，实际 %q -> %q", got[0].ParentTempID, got[1].TempID)
	}
}

// 母题不在本块时，保留原样的 parent：悄悄改成空更容易让人漏掉问题。
func TestReidentifyKeepsDanglingParent(t *testing.T) {
	got := reidentify(0, []ai.ExtractedQuestion{
		{TempID: "a1", ParentTempID: "somewhere-else", Stem: "子题"},
	})
	if got[0].ParentTempID != "somewhere-else" {
		t.Fatalf("找不到的母题应原样保留，实际 %q", got[0].ParentTempID)
	}
}

// 同一块里出现重复 temp_id 时，只认第一次：猜错会把一道题挂到另一道题下面。
func TestReidentifyIgnoresDuplicateTempIDs(t *testing.T) {
	got := reidentify(0, []ai.ExtractedQuestion{
		{TempID: "dup", Stem: "甲"},
		{TempID: "dup", Stem: "乙"},
	})
	if got[0].TempID == got[1].TempID {
		t.Fatalf("重复编号必须被拆开，实际都是 %q", got[0].TempID)
	}
}
