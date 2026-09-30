package importing

import (
	"strings"
	"testing"
)

// 版面大纲必须和草稿对得上：大纲里每个 question 节点的 draft_index
// 都要指向一道真实存在的题，错位一次就会把 A 题的内容塞进 B 题的卷面。

const sectionedPaper = `# 高一数学期中试卷

考生注意：全卷用时 120 分钟。

一、选择题（每小题 5 分，共 20 分）
1.【题目】下列函数中递增的是（  ）
A. $y=x^2$
B. $y=-x^2$
【答案】A
2.【题目】集合 $A=\{1,2\}$ 的真子集个数是（  ）
A. 2  B. 3  C. 4
【答案】C

二、填空题（每空 5 分，共 20 分）
3.【题目】$1+1=$（  ）
【答案】2

三、解答题（共 10 分）
4.【题目】求 $f(x)=x^2$ 在 $x=1$ 处的导数。
`

func outlineKinds(o []Outline) []OutlineKind {
	k := make([]OutlineKind, 0, len(o))
	for _, it := range o {
		k = append(k, it.Kind)
	}
	return k
}

func countKind(o []Outline, kind OutlineKind) int {
	n := 0
	for _, it := range o {
		if it.Kind == kind {
			n++
		}
	}
	return n
}

func TestParseTemplateFullCapturesHeadingsAndQuestions(t *testing.T) {
	drafts, outline, warns := ParseTemplateFull(sectionedPaper, "期中试卷.md")
	if len(warns) != 0 {
		t.Fatalf("不应有文档级告警: %v", warns)
	}
	if len(drafts) != 4 {
		t.Fatalf("应解析出 4 道题，实际 %d", len(drafts))
	}

	// 4 个标题：Markdown 的卷名 + 三个大题。
	if got := countKind(outline, OutlineHeading); got != 4 {
		t.Fatalf("应保留 4 个标题（卷名 + 3 个大题），实际 %d：%v", got, outlineKinds(outline))
	}
	if got := countKind(outline, OutlineQuestion); got != 4 {
		t.Fatalf("大纲应引用 4 道题，实际 %d", got)
	}

	// 每个题目引用都必须指向一道真实存在的题。
	for i, it := range outline {
		if it.Kind != OutlineQuestion {
			continue
		}
		if it.DraftIndex < 0 || it.DraftIndex >= len(drafts) {
			t.Fatalf("大纲第 %d 项的 draft_index=%d 越界（共 %d 道题）", i, it.DraftIndex, len(drafts))
		}
	}

	// 标题要保住原文的编号（"一、选择题"）——那是卷面的一部分，
	// 去掉会让导出的卷子和原件对不上。要去掉的只是 Markdown 记号。
	var headings []string
	for _, h := range outline {
		if h.Kind == OutlineHeading {
			headings = append(headings, h.Text)
		}
	}
	headingsJoined := strings.Join(headings, "|")
	if !strings.Contains(headingsJoined, "选择题") {
		t.Fatalf("标题文字不对: %v", headings)
	}
	if strings.Contains(headingsJoined, "#") {
		t.Fatalf("标题里不该留下 Markdown 记号: %v", headings)
	}
	// "一、" 这类中文序号原样保留；"（一）" 这类二级编号判成 level 2。
	for _, h := range outline {
		if h.Kind != OutlineHeading {
			continue
		}
		if strings.Contains(h.Text, "选择题") && h.Level != 1 {
			t.Fatalf("「一、…」应判为一级标题，实际 level=%d", h.Level)
		}
	}
}

// 顺序必须对：标题在前，它下面的题紧随其后。
func TestOutlineOrderMatchesPaperOrder(t *testing.T) {
	drafts, outline, _ := ParseTemplateFull(sectionedPaper, "p.md")

	var seq []string
	for _, it := range outline {
		switch it.Kind {
		case OutlineHeading:
			seq = append(seq, "H:"+it.Text)
		case OutlineQuestion:
			seq = append(seq, "Q:"+drafts[it.DraftIndex].StemText)
		}
	}
	// 标题文字要留住编号，所以用 Contains 定位而不是前缀匹配。
	joined := strings.Join(seq, " || ")

	choiceAt := strings.Index(joined, "选择题")
	blankAt := strings.Index(joined, "填空题")
	if choiceAt < 0 || blankAt < 0 || choiceAt > blankAt {
		t.Fatalf("标题顺序不对: %s", joined)
	}
	// StemText 不含公式，所以按公式以外的文字断言。
	if !strings.Contains(joined[choiceAt:blankAt], "下列函数中递增的是") {
		t.Fatalf("第 1 题应落在选择题标题之后: %s", joined)
	}
	if !strings.Contains(joined[blankAt:], "处的导数") {
		t.Fatalf("第 4 题应落在填空题标题之后: %s", joined)
	}
}

// 题间的说明文字不能被丢掉，也不能被塞进题干。
func TestOutlineKeepsInterstitialText(t *testing.T) {
	_, outline, _ := ParseTemplateFull(sectionedPaper, "p.md")
	if countKind(outline, OutlineRichText) == 0 {
		t.Fatalf("考生须知属于版面，应保留为说明文字: %v", outlineKinds(outline))
	}
	var texts []string
	for _, it := range outline {
		if it.Kind == OutlineRichText {
			texts = append(texts, it.Text)
		}
	}
	if !strings.Contains(strings.Join(texts, "|"), "考生注意") {
		t.Fatalf("说明文字内容不对: %v", texts)
	}
}

// 大纲里出现的每个 draft_index 都必须唯一——重复意味着同一道题被放了两遍。
func TestOutlineQuestionIndexesAreUnique(t *testing.T) {
	drafts, outline, _ := ParseTemplateFull(sectionedPaper, "p.md")
	seen := map[int]bool{}
	for _, it := range outline {
		if it.Kind != OutlineQuestion {
			continue
		}
		if seen[it.DraftIndex] {
			t.Fatalf("第 %d 道题在大纲里出现了两次", it.DraftIndex)
		}
		seen[it.DraftIndex] = true
	}
	if len(seen) != len(drafts) {
		t.Fatalf("每道题都该在大纲里出现一次：大纲 %d，草稿 %d", len(seen), len(drafts))
	}
}

// 没有版面信息时（模型路径）不许编造标题。
func TestOutlineForDraftsHasNoFabricatedHeadings(t *testing.T) {
	o := OutlineForDrafts(3)
	if len(o) != 3 {
		t.Fatalf("应产出 3 个题目节点，实际 %d", len(o))
	}
	for i, it := range o {
		if it.Kind != OutlineQuestion || it.DraftIndex != i {
			t.Fatalf("第 %d 项不对: %+v", i, it)
		}
		if it.Text != "" || it.Note != "" {
			t.Fatalf("不该凭空造出内容: %+v", it)
		}
	}
}

// 单题文档也要产出可用大纲，否则"只有一道题"的导入会无法还原成卷。
func TestOutlineForSingleQuestionDocument(t *testing.T) {
	drafts, outline, _ := ParseTemplateFull("1.【题目】甲\nA. 1\nB. 2\n【答案】A\n", "x.md")
	if len(drafts) != 1 || len(outline) != 1 || outline[0].Kind != OutlineQuestion {
		t.Fatalf("单题文档的大纲不对: drafts=%d outline=%v", len(drafts), outline)
	}
}
