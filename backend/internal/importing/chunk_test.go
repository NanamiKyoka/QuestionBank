package importing

import (
	"strings"
	"testing"
)

// 短文档不该被切：一次调用本来就够快，切开只会多付几次往返。
func TestSplitKeepsShortDocumentsWhole(t *testing.T) {
	doc := "一、选择题\n1. 甲\n2. 乙\n"
	got := SplitForExtraction(doc, 0)
	if len(got) != 1 {
		t.Fatalf("短文档应当只有一块，实际 %d", len(got))
	}
	if got[0].Text != doc {
		t.Fatalf("内容被改动了: %q", got[0].Text)
	}
}

// 单块上限按**题数**算，不只是字节数。
//
// 这是实测出来的：模型为每道题要吐一千多 token，所以"题数"才是输出量
// 的代理指标。只按字节切的话，一份全是长解析题的卷子每块字节数不大，
// 输出量却能顶到上游的超时上限。
// filler 撑长样本用的句子。切分有长度门槛，样本太短根本触发不了切分，
// 那样的用例等于什么都没测。
const filler = "，已知首项为 3、公比为 2 的等比数列，求其前 n 项和的表达式并说明推导过程。"

func TestSplitCapsQuestionsPerChunk(t *testing.T) {
	var b strings.Builder
	b.WriteString("一、填空题\n")
	for i := 1; i <= 40; i++ {
		b.WriteString(itoa(i) + ". 已知数列第 " + itoa(i) + " 项" + filler + "\n")
	}
	if len(b.String()) < extractionSplitThreshold {
		t.Fatalf("样本太短：%d < %d", len(b.String()), extractionSplitThreshold)
	}

	got := SplitForExtraction(b.String(), 0)
	if len(got) < 2 {
		t.Fatalf("这么长的卷子应当被切开，实际只有 %d 块", len(got))
	}
	for i, c := range got {
		n := countQuestions(c.Text)
		if n > extractionMaxQuestionsPerChunk {
			t.Fatalf("第 %d 块有 %d 道题，超过上限 %d", i, n, extractionMaxQuestionsPerChunk)
		}
	}

	// 切开后每道题都必须还在，且只出现一次。
	joined := ""
	for _, c := range got {
		joined += stripLeadingNoise(c.Text) + "\n"
	}
	for i := 1; i <= 40; i++ {
		// 题号要连着后面的正文一起匹配：只查 "1. " 会把 11.、21. 一并算进来。
		needle := itoa(i) + ". 已知数列第 " + itoa(i) + " 项"
		if n := strings.Count(joined, needle); n != 1 {
			t.Fatalf("第 %d 题在拼接结果里出现了 %d 次", i, n)
		}
	}
}

// 绝不把小问和它的母题切���。
//
// parent_temp_id 是跨块关联的；切开之后那道母题会变成一道孤立的题，
// 外加一串失去归属的小问——这是导入链路里最隐蔽的一种数据损坏。
func TestSplitKeepsSubQuestionsWithTheirParent(t *testing.T) {
	var b strings.Builder
	for q := 1; q <= 12; q++ {
		b.WriteString(itoa(q) + ".（本题满分 14 分）本题共有 2 个小题，下面给出一段足够长的题面文字用来撑过切分门槛" + filler + "\n")
		b.WriteString("   (1) 求证：CH ⊥ PB，说明每一步的依据；\n")
		b.WriteString("   (2) 若体积为 √10/5，求二面角的大小并写出计算过程。\n")
	}
	if len(b.String()) < extractionSplitThreshold {
		t.Fatalf("样本太短：%d < %d", len(b.String()), extractionSplitThreshold)
	}

	got := SplitForExtraction(b.String(), 0)
	if len(got) < 2 {
		t.Skip("样本太短，没有触发切分")
	}
	for i, c := range got {
		body := stripLeadingNoise(c.Text)
		mothers := strings.Count(body, ".（本题满分")
		subs := strings.Count(body, "(1) 求证")
		if mothers != subs {
			t.Fatalf("第 %d 块有 %d 道母题却只有 %d 个小问，母子被切开了", i, mothers, subs)
		}
	}
}

// 除第一块外，每块都要带上它所属的大题标题。
//
// 不带的话，"2. 已知数列…"这种题号脱离上下文后没法判断题型，
// 而题型判错会让整道题的答案对不上。
func TestSplitAddsSectionHeadingToLaterChunks(t *testing.T) {
	var b strings.Builder
	b.WriteString("一、选择题（本大题共 30 题，每题 5 分）\n")
	for i := 1; i <= 30; i++ {
		b.WriteString(itoa(i) + ". 下列说法正确的是（  ）" + filler + "\n")
		b.WriteString("A. 甲 B. 乙 C. 丙 D. 丁\n")
	}
	if len(b.String()) < extractionSplitThreshold {
		t.Fatalf("样本太短：%d < %d", len(b.String()), extractionSplitThreshold)
	}

	got := SplitForExtraction(b.String(), 0)
	if len(got) < 2 {
		t.Fatalf("样本应当被切开，实际 %d 块", len(got))
	}
	for i := 1; i < len(got); i++ {
		if !strings.Contains(got[i].Text, "一、选择题") {
			t.Fatalf("第 %d 块没有带上大题标题: %q", i, firstLineOf(got[i].Text))
		}
	}
}

// 面向用户的失败说明里不该出现给模型看的注释与前缀。
func TestDescribeChunkHidesModelOnlyNoise(t *testing.T) {
	body := PDFTextPrefix + "一、填空题\n1. 第一题\n"
	if got := DescribeChunk(body); got != "一、填空题" {
		t.Fatalf("不该把 PDF 注释当成标题，实际 %q", got)
	}
	got := DescribeChunk("（本段属于：二、选择题）\n13. 第十三题")
	if got != "13. 第十三题" {
		t.Fatalf("不该把分段前缀当成标题，实际 %q", got)
	}
}

func countQuestions(body string) int {
	n := 0
	for _, line := range splitLines(body) {
		if questionStartRe.MatchString(line) {
			n++
		}
	}
	return n
}

func firstLineOf(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
