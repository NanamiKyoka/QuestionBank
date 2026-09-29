package importing

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

// buildDocx 用最少的 OOXML 拼一个 .docx。
//
// 刻意手写而不是调内部/infra/docx 的写出器：那个写出器只产出排版结果，
// 不含 OMML 公式与表格，用它做输入端到端往返会漏掉最需要验证的部分。
func buildDocx(t *testing.T, document string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(docxDocumentPart)
	if err != nil {
		t.Fatalf("写 zip 失败: %v", err)
	}
	if _, err := w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"
            xmlns:m="http://schemas.openxmlformats.org/officeDocument/2006/math">
<w:body>` + document + `</w:body></w:document>`)); err != nil {
		t.Fatalf("写 document.xml 失败: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("关闭 zip 失败: %v", err)
	}
	return buf.Bytes()
}

func buildZip(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("写 zip 条目失败: %v", err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("写 zip 条目内容失败: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("关闭 zip 失败: %v", err)
	}
	return buf.Bytes()
}

func TestDocxToMarkdownParagraphs(t *testing.T) {
	data := buildDocx(t, `
<w:p><w:r><w:t>1.【题目】下列说法正确的是</w:t></w:r></w:p>
<w:p><w:r><w:t>【答案】A</w:t></w:r><w:r><w:t>【解析】因为它成立</w:t></w:r></w:p>
`)
	got, warns, err := DocxToMarkdown(data, nil)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(warns) != 0 {
		t.Fatalf("不应有告警: %v", warns)
	}
	// 一句话被 Word 拆成多个 run 时必须还是一行。
	if strings.Contains(got, "【解析】因为它成立\n") {
		t.Fatalf("同一段落被拆成了多行:\n%s", got)
	}
	for _, want := range []string{"1.【题目】下列说法正确的是", "【答案】A", "【解析】因为它成立"} {
		if !strings.Contains(got, want) {
			t.Fatalf("缺少 %q，实际:\n%s", want, got)
		}
	}
	// 摄取出来的文本必须能直接喂给规则解析器。
	drafts, _ := ParseTemplate(got, "x.docx")
	if len(drafts) != 1 {
		t.Fatalf("摄取结果应能解析出 1 道题，实际 %d", len(drafts))
	}
	// 这份文档只有题干与答案、没有选项，答案却是选项字母 "A"——
	// 解析器必须把它标出来，而不是当成一道"参考答案为 A"的问答题收下。
	if !hasWarning(drafts[0].Warnings, "选项") {
		t.Fatalf("选项缺失时应当给出提示，实际警告：%v", drafts[0].Warnings)
	}
}

func TestDocxToMarkdownTable(t *testing.T) {
	data := buildDocx(t, `
<w:tbl>
<w:tr><w:tc><w:p><w:r><w:t>题号</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>答案</w:t></w:r></w:p></w:tc></w:tr>
<w:tr><w:tc><w:p><w:r><w:t>1</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>A</w:t></w:r></w:p></w:tc></w:tr>
</w:tbl>
`)
	got, _, err := DocxToMarkdown(data, nil)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if !strings.Contains(got, "| 题号 | 答案 |") {
		t.Fatalf("表格未转成管道表:\n%s", got)
	}
	ans, _ := parseAnswerSection(strings.Split(got, "\n"))
	if ans[1] != "A" {
		t.Fatalf("表格里的答案应能取回，实际 %v", ans)
	}
}

func TestDocxToMarkdownMath(t *testing.T) {
	data := buildDocx(t, `
<w:p><w:r><w:t>当 </w:t></w:r>
<m:oMath><m:f><m:num><m:r><m:t>a</m:t></m:r></m:num><m:den><m:r><m:t>b</m:t></m:r></m:den></m:f></m:oMath>
<w:r><w:t> 时</w:t></w:r></w:p>
`)
	got, _, err := DocxToMarkdown(data, nil)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if !strings.Contains(got, `\frac{a}{b}`) {
		t.Fatalf("OMML 分数未转成 LaTeX:\n%s", got)
	}
}

func TestIngestMarkdownAndZip(t *testing.T) {
	res, err := Ingest([]byte("【题目】甲"), "a.md", nil)
	if err != nil {
		t.Fatalf("md 摄取失败: %v", err)
	}
	if len(res.Docs) != 1 || res.Docs[0].Text != "【题目】甲" || res.Docs[0].Name != "a.md" {
		t.Fatalf("md 摄取结果不对: %+v", res.Docs)
	}

	zipData := buildZip(t, map[string]string{
		"包/一.md":   "【题目】甲",
		"包/二.txt":  "【题目】乙",
		"包/图片.png": "not a document",
	})
	res, err = Ingest(zipData, "卷子.zip", nil)
	if err != nil {
		t.Fatalf("zip 摄取失败: %v", err)
	}
	// 必须逐份保留：两份都用 1/2/3 编号时，合并成一段再解析，
	// 第一份的文末答案区会贴到第二份的题上，而且表面完全看不出来。
	if len(res.Docs) != 2 {
		t.Fatalf("应保留 2 份文档，实际 %d", len(res.Docs))
	}
	names := map[string]bool{}
	for _, d := range res.Docs {
		names[d.Name] = true
		if strings.Contains(d.Text, "not a document") {
			t.Fatalf("zip 内的非文档条目不该被导入")
		}
	}
	if !names["一.md"] || !names["二.txt"] {
		t.Fatalf("来源名应取路径末段，实际 %v", names)
	}
	if len(res.Files) != 2 {
		t.Fatalf("来源文件数不对: %v", res.Files)
	}
}

// zip-slip：条目名带父目录穿越必须被拒，且不能影响同包里的正常文件。
func TestIngestZipRejectsPathTraversal(t *testing.T) {
	zipData := buildZip(t, map[string]string{
		"../逃逸.md": "【题目】不该出现",
		"正常.md":    "【题目】应该出现",
	})
	res, err := Ingest(zipData, "bad.zip", nil)
	if err != nil {
		t.Fatalf("包内仍有合法文档，不该整包失败: %v", err)
	}
	if strings.Contains(res.Docs[0].Text, "不该出现") {
		t.Fatalf("穿越路径的条目被导入了: %q", res.Docs[0].Text)
	}
	if !strings.Contains(res.Docs[0].Text, "应该出现") {
		t.Fatalf("正常条目应照常导入: %q", res.Docs[0].Text)
	}
	for _, f := range res.Files {
		if strings.Contains(f, "逃逸") {
			t.Fatalf("穿越路径的条目被登记为来源: %v", res.Files)
		}
	}
}

func TestIngestRejectsUnsupportedType(t *testing.T) {
	if _, err := Ingest([]byte("x"), "a.pdf", nil); err == nil {
		t.Fatal("PDF 不在支持范围内，必须报错而不是静默导入二进制")
	}
	if UploadSupported("a.DOCX") != true {
		t.Fatal("扩展名匹配应当大小写不敏感")
	}
	if UploadSupported("a.pdf") {
		t.Fatal("PDF 不应被标记为支持")
	}
}
