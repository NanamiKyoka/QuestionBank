package pdftext

import (
	"bytes"
	"compress/zlib"
	"strings"
	"testing"
)

// buildPDF 用给定的对象拼一份 PDF（经典 xref 表，不压缩）。
//
// 手工构造而不是塞一个二进制样本进仓库：这样每条断言都对应一段
// 自己看得懂的 PDF 语法，样例过期了改一行就能跟上规范，
// 而二进制样本一旦坏了，没人说得清它坏在哪。
func buildPDF(objects []string, rootRef int) []byte {
	var buf bytes.Buffer
	offsets := make([]int, len(objects)+1)

	buf.WriteString("%PDF-1.7\n")
	for i, body := range objects {
		offsets[i+1] = buf.Len()
		buf.WriteString(itoa(i + 1))
		buf.WriteString(" 0 obj\n")
		buf.WriteString(body)
		buf.WriteString("\nendobj\n")
	}

	xref := buf.Len()
	buf.WriteString("xref\n0 ")
	buf.WriteString(itoa(len(objects) + 1))
	buf.WriteString("\n0000000000 65535 f \n")
	for i := 1; i <= len(objects); i++ {
		buf.WriteString(pad10(offsets[i]))
		buf.WriteString(" 00000 n \n")
	}
	buf.WriteString("trailer\n<< /Size ")
	buf.WriteString(itoa(len(objects) + 1))
	buf.WriteString(" /Root ")
	buf.WriteString(itoa(rootRef))
	buf.WriteString(" 0 R >>\nstartxref\n")
	buf.WriteString(itoa(xref))
	buf.WriteString("\n%%EOF\n")
	return buf.Bytes()
}

func pad10(v int) string {
	s := itoa(v)
	for len(s) < 10 {
		s = "0" + s
	}
	return s
}

func deflate(t *testing.T, s string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	if _, err := w.Write([]byte(s)); err != nil {
		t.Fatalf("压缩失败: %v", err)
	}
	w.Close()
	return buf.Bytes()
}

// simpleTextPDF 造一份只有一个页面的 PDF，用内置编码显示一段文字。
func simpleTextPDF(t *testing.T, content string) []byte {
	t.Helper()
	compressed := deflate(t, content)
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 612 792] >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] " +
			"/Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		"<< /Length " + itoa(len(compressed)) + " /Filter /FlateDecode >>\nstream\n" +
			string(compressed) + "\nendstream",
		// 简单字体：1 字节码，ToUnicode 把 65..90 映成大写字母。
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /FirstChar 32 /LastChar 126 " +
			"/Widths [278 278 278 278 278 278 278 278 278 278 278 278 278 278 278 278 " +
			"278 278 278 278 278 278 278 278 278 278 278 278 278 278 278 278 " +
			"278 278 278 278 278 278 278 278 278 278 278 278 278 278 278 278 " +
			"278 278 278 278 278 278 278 278 278 278 278 278 278 278 278 278 " +
			"278 278 278 278 278 278 278 278 278 278 278 278 278 278 278 278 " +
			"278 278 278 278 278 278 278 278 278 278 278 278 278 278 278 278 >> " +
			"/ToUnicode 6 0 R >>",
		// 上面这段 ToUnicode 用一个合法的 CMap 代替：见下。
		"",
	}
	cmap := deflate(t, strings.Join([]string{
		"/CIDInit /ProcSet findresource begin",
		"12 dict begin",
		"begincmap",
		"1 begincodespacerange <00> <FF> endcodespacerange",
		"95 beginbfchar",
		"<20> <0020> <21> <0021> <22> <0022> <23> <0023> <24> <0024> <25> <0025>",
		"<26> <0026> <27> <0027> <28> <0028> <29> <0029> <2A> <002A> <2B> <002B>",
		"<2C> <002C> <2D> <002D> <2E> <002E> <2F> <002F> <30> <0030> <31> <0031>",
		"<32> <0032> <33> <0033> <34> <0034> <35> <0035> <36> <0036> <37> <0037>",
		"<38> <0038> <39> <0039> <3A> <003A> <3B> <003B> <3C> <003C> <3D> <003D>",
		"<3E> <003E> <3F> <003F> <40> <0040> <41> <0041> <42> <0042> <43> <0043>",
		"<44> <0044> <45> <0045> <46> <0046> <47> <0047> <48> <0048> <49> <0049>",
		"<4A> <004A> <4B> <004B> <4C> <004C> <4D> <004D> <4E> <004E> <4F> <004F>",
		"<50> <0050> <51> <0051> <52> <0052> <53> <0053> <54> <0054> <55> <0055>",
		"<56> <0056> <57> <0057> <58> <0058> <59> <0059> <5A> <005A> <5B> <005B>",
		"<5C> <005C> <5D> <005D> <5E> <005E> <5F> <005F> <60> <0060> <61> <0061>",
		"<62> <0062> <63> <0063> <64> <0064> <65> <0065> <66> <0066> <67> <0067>",
		"<68> <0068> <69> <0069> <6A> <006A> <6B> <006B> <6C> <006C> <6D> <006D>",
		"<6E> <006E> <6F> <006F> <70> <0070> <71> <0071> <72> <0072> <73> <0073>",
		"<74> <0074> <75> <0075> <76> <0076> <77> <0077> <78> <0078> <79> <0079>",
		"<7A> <007A> <7B> <007B> <7C> <007C> <7D> <007D> <7E> <007E>",
		"endbfchar",
		"endcmap",
		"CMapName currentdict /CMap defineresource pop",
		"end",
		"end",
	}, "\n"))
	objects[5] = "<< /Length " + itoa(len(cmap)) + " /Filter /FlateDecode >>\nstream\n" +
		string(cmap) + "\nendstream"
	return buildPDF(objects, 1)
}

func TestExtractsTextFromAPage(t *testing.T) {
	pdf := simpleTextPDF(t, strings.Join([]string{
		"BT /F1 12 Tf 72 700 Td (Hello) Tj",
		"0 -20 Td (World) Tj",
		"ET",
	}, "\n"))

	res, err := Extract(pdf)
	if err != nil {
		t.Fatalf("抽取失败: %v", err)
	}
	if res.Pages != 1 {
		t.Fatalf("应有 1 页，实际 %d", res.Pages)
	}
	if !res.HasText() {
		t.Fatal("应当取到文字")
	}
	if got := strings.ReplaceAll(res.Text, "\n", "|"); got != "Hello|World" {
		t.Fatalf("行序不对：%q", got)
	}
}

// 行内被拆成多个片段时，要按 x 顺序拼回去，且该补空格的地方补上。
func TestJoinsRunsOnTheSameLine(t *testing.T) {
	pdf := simpleTextPDF(t, strings.Join([]string{
		"BT /F1 12 Tf 72 700 Td [(AB) -400 (CD)] TJ ET",
	}, "\n"))

	res, err := Extract(pdf)
	if err != nil {
		t.Fatalf("抽取失败: %v", err)
	}
	// TJ 里的大幅负间距代表"这里原本有空格"。
	if !strings.Contains(res.Text, "AB CD") {
		t.Fatalf("应当补上空格，实际 %q", res.Text)
	}
}

// 同一页上的两行必须保持上下顺序，而不是按 x 排。
func TestKeepsVerticalOrder(t *testing.T) {
	pdf := simpleTextPDF(t, strings.Join([]string{
		"BT /F1 12 Tf 72 700 Td (second) Tj",
		"1 0 0 1 72 690 Tm (first) Tj",
		"ET",
	}, "\n"))

	res, err := Extract(pdf)
	if err != nil {
		t.Fatalf("抽取失败: %v", err)
	}
	lines := strings.Split(res.Text, "\n")
	if len(lines) != 2 || lines[0] != "second" || lines[1] != "first" {
		t.Fatalf("PDF 里 y 向上，行序应当自上而下：%q", lines)
	}
}

// xref 损坏时退回全文件扫描：这是"能导入"与"打不开一份真卷子"的区别。
func TestRecoversWhenXrefIsBroken(t *testing.T) {
	pdf := simpleTextPDF(t, "BT /F1 12 Tf 72 700 Td (Recovered) Tj ET")
	// 把 startxref 指到一个 nonsense 偏移。
	broken := bytes.Replace(pdf, []byte("startxref\n"), []byte("startxref\n"), 1)
	idx := bytes.LastIndex(pdf, []byte("xref\n0 "))
	if idx < 0 {
		t.Fatal("构造的样例里没有 xref 表")
	}
	broken = append([]byte{}, pdf[:idx]...)
	broken = append(broken, []byte("xref\n0 1\n")...) // 一张只有空项的假表
	broken = append(broken, []byte("trailer\n<< /Size 1 >>\nstartxref\n999999\n%%EOF\n")...)

	res, err := Extract(broken)
	if err != nil {
		t.Fatalf("应当退回扫描重建而不是报错: %v", err)
	}
	if !strings.Contains(res.Text, "Recovered") {
		t.Fatalf("扫描重建后应当仍能取到文字，实际 %q", res.Text)
	}
}

func TestRejectsNonPDF(t *testing.T) {
	if _, err := Extract([]byte("this is not a pdf")); err == nil {
		t.Fatal("非 PDF 必须被拒绝")
	}
}

// 没有文字层时要如实说出来，而不是报"识别出 0 道题"。
func TestReportsPagesWithoutText(t *testing.T) {
	pdf := simpleTextPDF(t, "0 0 1 RG 100 100 200 300 re f")
	res, err := Extract(pdf)
	if err != nil {
		t.Fatalf("抽取失败: %v", err)
	}
	if res.HasText() {
		t.Fatalf("纯图形页不该有文字，实际 %q", res.Text)
	}
	if len(res.Warnings) == 0 {
		t.Fatal("应当给出「没有文字层」的提示")
	}
}
