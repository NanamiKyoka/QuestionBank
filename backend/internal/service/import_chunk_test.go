package service

import (
	"strings"
	"testing"

	"github.com/NanamiKyoka/questionmind/internal/ai"
	"github.com/NanamiKyoka/questionmind/internal/importing"
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

func TestAssignSourceNumbersUsesChunkRange(t *testing.T) {
	got := []ai.ExtractedQuestion{
		{TempID: "m15", Stem: "阅读诗歌，完成下面小题。"},
		{TempID: "c1", ParentTempID: "m15", Stem: "(1) 小题"},
		{TempID: "m16", Stem: "有一首歌这样唱道"},
	}
	assignSourceNumbers(importing.Chunk{QuestionFrom: 15, QuestionTo: 16}, got)
	if got[0].SourceNumber != 15 || got[2].SourceNumber != 16 {
		t.Fatalf("应按分块范围回填题号，实际 %d / %d", got[0].SourceNumber, got[2].SourceNumber)
	}
	if got[1].SourceNumber != 0 {
		t.Fatalf("子题不应单独占用题号，实际 %d", got[1].SourceNumber)
	}
}

func TestPageQuestionRangesAlignToPages(t *testing.T) {
	ranges := pageQuestionRanges([]string{
		"8. 辛\n2. 乙\n1. 甲\n10. 癸",
		"17. 庚\n11. 甲\n12. 乙",
		"19. 乙\n18. 甲",
	}, 3)
	if ranges[0].from != 1 || ranges[0].to != 10 {
		t.Fatalf("第 1 页题号范围错误: %+v", ranges[0])
	}
	if got := rangeForPages(ranges, 1, 1); got.from != 11 || got.to != 17 {
		t.Fatalf("第 2 页题号范围错误: %+v", got)
	}
	if got := rangeForPages(ranges, 0, 2); got.from != 1 || got.to != 17 {
		t.Fatalf("两页合并题号范围错误: %+v", got)
	}
}

func TestRepairOrphanSubquestionsCreatesMother(t *testing.T) {
	questions := []ai.ExtractedQuestion{
		{TempID: "q18", SourceNumber: 18, Stem: "18. 椭圆题"},
		{TempID: "o1", SourceNumber: 18, Stem: "19(1) 求函数的最大值"},
		{TempID: "o2", SourceNumber: 18, Stem: "19(2) 证明存在 y"},
		{TempID: "o3", SourceNumber: 18, Stem: "19(3) 求 b 的最小值"},
	}
	got := repairOrphanSubquestions(questions, "18. 椭圆题\n19. 已知函数 f(x)=5cos x−cos5x\n(1) 求最大值\n(2) 证明\n", questionRange{from: 18, to: 18})
	if len(got) != 5 {
		t.Fatalf("母题应被插入，实际节点 %d", len(got))
	}
	parent := got[1]
	if parent.TempID != "auto-q19" || parent.SourceNumber != 19 || !strings.Contains(parent.Stem, "5cos") {
		t.Fatalf("自动母题错误: %+v", parent)
	}
	for i := 2; i < len(got); i++ {
		if got[i].ParentTempID != parent.TempID {
			t.Fatalf("孤立小题未挂到母题: %+v", got[i])
		}
	}
}

func TestFillPageRangeFallbackUsesPreviousPageAndRootCount(t *testing.T) {
	got := fillPageRangeFallback(questionRange{}, questionRange{from: 1, to: 16}, 3)
	if got.from != 17 || got.to != 19 {
		t.Fatalf("缺页文本层时应回退到 17~19，实际 %+v", got)
	}
	got = fillPageRangeFallback(questionRange{from: 17, to: 18}, questionRange{}, 3)
	if got.from != 17 || got.to != 19 {
		t.Fatalf("页文本层上限偏小时应扩展到 17~19，实际 %+v", got)
	}
}
