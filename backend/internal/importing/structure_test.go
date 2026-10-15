package importing

import (
	"strings"
	"testing"
)

func TestSeparateDocumentSectionsFindsAnswerRun(t *testing.T) {
	doc := strings.Join([]string{
		"一、阅读",
		"（一）阅读 I",
		"1．下列对材料相关内容的理解和分析，不正确的一项是（ ）",
		"A．甲 B．乙 C．丙 D．丁",
		"2．根据材料内容，下列说法正确的一项是（ ）",
		"1．C    2．D    3．A",
		"【详解】1．C．解析",
	}, "\n")

	got := SeparateDocumentSections(doc)
	if strings.Contains(got.Questions, "1．C") {
		t.Fatalf("答案区混入题目区: %q", got.Questions)
	}
	if !strings.Contains(got.Answers, "1．C") || !strings.Contains(got.Answers, "【详解】") {
		t.Fatalf("答案区不完整: %q", got.Answers)
	}
}

func TestSplitForRecordDropsAnswerSection(t *testing.T) {
	doc := "一、选择题\n1．题干一\n2．题干二\n1．C 2．D\n【详解】题干一\n【详解】题干二\n"
	chunks := SplitForExtractionMode(doc, 0, ModeRecord)
	joined := ""
	for _, c := range chunks {
		joined += c.Text + "\n"
	}
	if strings.Contains(joined, "【详解】") || strings.Contains(joined, "1．C") {
		t.Fatalf("仅录题不应把答案区发给模型: %q", joined)
	}
	if !strings.Contains(joined, "题干一") || !strings.Contains(joined, "题干二") {
		t.Fatalf("题目区被误删: %q", joined)
	}
}

func TestSplitForExtractKeepsAnswerSection(t *testing.T) {
	doc := "一、选择题\n1．题干一\n2．题干二\n1．C 2．D\n【详解】题干一\n【详解】题干二\n"
	chunks := SplitForExtractionMode(doc, 0, ModeExtract)
	joined := ""
	for _, c := range chunks {
		joined += c.Text + "\n"
	}
	if !strings.Contains(joined, "【详解】") {
		t.Fatalf("完整抽取仍应保留答案区上下文: %q", joined)
	}
}

func TestSplitStartsNewChunkAtReadingGroup(t *testing.T) {
	var b strings.Builder
	b.WriteString("一、阅读\n")
	writeReadingGroup := func(group string, from, to int) {
		b.WriteString(group + "\n")
		b.WriteString("材料一：这是一段用于测试材料归属的阅读材料。" + strings.Repeat(filler, 5) + "\n")
		for i := from; i <= to; i++ {
			b.WriteString(itoa(i) + "．题目" + itoa(i) + strings.Repeat(filler, 4) + "\n")
		}
	}
	writeReadingGroup("（一）阅读 I（本题共 2 小题）", 1, 2)
	writeReadingGroup("（二）阅读 II（本题共 2 小题）", 3, 4)
	writeReadingGroup("（三）阅读 III（本题共 2 小题）", 5, 6)

	if len(b.String()) < extractionSplitThreshold {
		t.Fatalf("样本太短：%d < %d", len(b.String()), extractionSplitThreshold)
	}
	chunks := SplitForExtraction(b.String(), 0)
	if len(chunks) < 3 {
		t.Fatalf("阅读组应形成独立块，实际 %d 块", len(chunks))
	}
	for i, c := range chunks {
		if c.Group == "" {
			t.Fatalf("第 %d 块缺少材料组上下文: %q", i, c.Text)
		}
		if !strings.Contains(c.Text, "（本段材料组：") {
			t.Fatalf("第 %d 块没有把材料组传给模型: %q", i, c.Text)
		}
		if c.QuestionFrom == 0 || c.QuestionTo < c.QuestionFrom {
			t.Fatalf("第 %d 块缺少原卷题号范围: %+v", i, c)
		}
	}
}

func TestReadNumberedQuestionsSkipsAnswerRun(t *testing.T) {
	doc := "1．题干一\n2．题干二\n1．C 2．D\n"
	if got := SeparateDocumentSections(doc).Questions; strings.Contains(got, "1．C") {
		t.Fatalf("答案行没有被隔离: %q", got)
	}
}
