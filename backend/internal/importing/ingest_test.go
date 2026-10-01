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

// buildNumberedDocx 拼一份带自动编号的 Word 卷子。
//
// 真实卷子的题号几乎都是自动编号，document.xml 里只有 w:numPr，
// 序号本身住在 numbering.xml 描述的模板里。
func buildNumberedDocx(t *testing.T, document, levels, styles string) []byte {
	t.Helper()
	return buildZip(t, map[string]string{
		docxDocumentPart: `<?xml version="1.0" encoding="UTF-8"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>` + document + `</w:body></w:document>`,
		docxNumberingPart: `<?xml version="1.0" encoding="UTF-8"?>
<w:numbering xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:abstractNum w:abstractNumId="0">` + levels + `</w:abstractNum>
  <w:num w:numId="1"><w:abstractNumId w:val="0"/></w:num>
</w:numbering>`,
		docxStylesPart: styles,
	})
}

// 还原之后必须能按题号切题——这正是 I-03a 说的功能失效。
func TestDocxRestoresAutomaticNumbering(t *testing.T) {
	doc := `
<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="1"/></w:numPr></w:pPr><w:r><w:t>【题目】下列说法正确的是</w:t></w:r></w:p>
<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="1"/></w:numPr></w:pPr><w:r><w:t>【题目】下列说法错误的是</w:t></w:r></w:p>
`
	data := buildNumberedDocx(t, doc,
		`<w:lvl w:ilvl="0"><w:start w:val="1"/><w:numFmt w:val="decimal"/><w:lvlText w:val="%1."/></w:lvl>`,
		``)
	got, _, err := DocxToMarkdown(data, nil)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if !strings.Contains(got, "1. 【题目】下列说法正确的是") ||
		!strings.Contains(got, "2. 【题目】下列说法错误的是") {
		t.Fatalf("自动编号未还原:\n%s", got)
	}
	drafts, _ := ParseTemplate(got, "x.docx")
	if len(drafts) != 2 {
		t.Fatalf("还原题号后应切出 2 道题，实际 %d:\n%s", len(drafts), got)
	}
}

// 多级编号：子题号由 %1.%2 生成，进入二级后一级要重新计数。
func TestDocxRestoresMultiLevelNumbering(t *testing.T) {
	levels := `<w:lvl w:ilvl="0"><w:start w:val="1"/><w:numFmt w:val="decimal"/><w:lvlText w:val="%1."/></w:lvl>` +
		`<w:lvl w:ilvl="1"><w:start w:val="1"/><w:numFmt w:val="decimal"/><w:lvlText w:val="(%2)"/></w:lvl>`
	para := func(ilvl, text string) string {
		return `<w:p><w:pPr><w:numPr><w:ilvl w:val="` + ilvl +
			`"/><w:numId w:val="1"/></w:numPr></w:pPr><w:r><w:t>` + text + `</w:t></w:r></w:p>`
	}
	doc := para("0", "第一大题") + para("1", "第一小问") + para("1", "第二小问") + para("0", "第二大题") + para("1", "第一小问")
	data := buildNumberedDocx(t, doc, levels, ``)
	got, _, err := DocxToMarkdown(data, nil)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	for _, want := range []string{"1. 第一大题", "(1) 第一小问", "(2) 第二小问", "2. 第二大题", "(1) 第一小问"} {
		if !strings.Contains(got, want) {
			t.Fatalf("缺少 %q，实际:\n%s", want, got)
		}
	}
}

// 中文卷子的"一、选择题"同样来自自动编号。
func TestDocxRestoresChineseCounting(t *testing.T) {
	levels := `<w:lvl w:ilvl="0"><w:start w:val="1"/><w:numFmt w:val="chineseCounting"/><w:lvlText w:val="%1、"/></w:lvl>` +
		`<w:lvl w:ilvl="1"><w:start w:val="1"/><w:numFmt w:val="chineseCounting"/><w:lvlText w:val="%2、"/></w:lvl>`
	doc := `<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="1"/></w:numPr></w:pPr><w:r><w:t>选择题</w:t></w:r></w:p>` +
		`<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="1"/></w:numPr></w:pPr><w:r><w:t>填空题</w:t></w:r></w:p>`
	data := buildNumberedDocx(t, doc, levels, ``)
	got, _, err := DocxToMarkdown(data, nil)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if !strings.Contains(got, "一、 选择题") || !strings.Contains(got, "二、 填空题") {
		t.Fatalf("中文序号未还原:\n%s", got)
	}
}

// 编号挂在段落样式上时（Word 的 ListParagraph），段落自己只有 pStyle。
func TestDocxRestoresNumberingFromParagraphStyle(t *testing.T) {
	doc := `<w:p><w:pPr><w:pStyle w:val="ListParagraph"/></w:pPr><w:r><w:t>【题目】甲</w:t></w:r></w:p>` +
		`<w:p><w:pPr><w:pStyle w:val="ListParagraph"/></w:pPr><w:r><w:t>【题目】乙</w:t></w:r></w:p>`
	styles := `<?xml version="1.0" encoding="UTF-8"?>
<w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:style w:type="paragraph" w:styleId="ListParagraph">
    <w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="1"/></w:numPr></w:pPr>
  </w:style>
</w:styles>`
	data := buildNumberedDocx(t, doc,
		`<w:lvl w:ilvl="0"><w:numFmt w:val="decimal"/><w:lvlText w:val="%1."/></w:lvl>`, styles)
	got, _, err := DocxToMarkdown(data, nil)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if !strings.Contains(got, "1. 【题目】甲") || !strings.Contains(got, "2. 【题目】乙") {
		t.Fatalf("样式上的编号未还原:\n%s", got)
	}
}

// 段落显式写 numId=0 是"取消编号"，必须压过样式上的那个。
func TestDocxParagraphNumberingOverridesStyle(t *testing.T) {
	doc := `<w:p><w:pPr><w:pStyle w:val="ListParagraph"/><w:numPr><w:numId w:val="0"/></w:numPr></w:pPr><w:r><w:t>【题目】甲</w:t></w:r></w:p>`
	styles := `<?xml version="1.0" encoding="UTF-8"?>
<w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:style w:type="paragraph" w:styleId="ListParagraph">
    <w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="1"/></w:numPr></w:pPr>
  </w:style>
</w:styles>`
	data := buildNumberedDocx(t, doc,
		`<w:lvl w:ilvl="0"><w:numFmt w:val="decimal"/><w:lvlText w:val="%1."/></w:lvl>`, styles)
	got, _, err := DocxToMarkdown(data, nil)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if strings.Contains(got, "1.") {
		t.Fatalf("numId=0 表示取消编号，不应加前缀:\n%s", got)
	}
}

// 项目符号不参与计数，原样输出即可。
func TestDocxKeepsBulletSymbol(t *testing.T) {
	levels := `<w:lvl w:ilvl="0"><w:numFmt w:val="bullet"/><w:lvlText w:val="·"/></w:lvl>`
	doc := `<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="1"/></w:numPr></w:pPr><w:r><w:t>要点一</w:t></w:r></w:p>` +
		`<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="1"/></w:numPr></w:pPr><w:r><w:t>要点二</w:t></w:r></w:p>`
	data := buildNumberedDocx(t, doc, levels, ``)
	got, _, err := DocxToMarkdown(data, nil)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if !strings.Contains(got, "· 要点一") || !strings.Contains(got, "· 要点二") {
		t.Fatalf("项目符号未还原:\n%s", got)
	}
}

// 没有 numbering.xml 的文档必须照旧工作，不能因为还原失败就整份失败。
func TestDocxWithoutNumberingStillIngests(t *testing.T) {
	data := buildDocx(t, `<w:p><w:r><w:t>1.【题目】甲</w:t></w:r></w:p>`)
	got, _, err := DocxToMarkdown(data, nil)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if !strings.Contains(got, "1.【题目】甲") {
		t.Fatalf("没有编号定义时应原样输出:\n%s", got)
	}
}

func TestFormatOrdinal(t *testing.T) {
	cases := []struct {
		n      int
		format string
		want   string
	}{
		{7, "decimal", "7"},
		{7, "decimalZero", "07"},
		{3, "lowerLetter", "c"},
		{27, "lowerLetter", "aa"},
		{52, "lowerLetter", "zz"},
		{4, "upperLetter", "D"},
		{9, "upperRoman", "IX"},
		{14, "lowerRoman", "xiv"},
		{10, "chineseCounting", "一十"},
		{21, "chineseCounting", "二十一"},
		{101, "chineseCounting", "一百零一"},
		{105, "chineseCounting", "一百零五"},
		{1, "ideographTraditional", "甲"},
		{5000, "upperRoman", "5000"},
		{3, "unknownFormat", "3"},
	}
	for _, c := range cases {
		if got := formatOrdinal(c.n, c.format); got != c.want {
			t.Errorf("formatOrdinal(%d, %q) = %q，期望 %q", c.n, c.format, got, c.want)
		}
	}
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
	if _, err := Ingest([]byte("x"), "a.doc", nil); err == nil {
		t.Fatal(".doc 不在支持范围内，必须报错而不是把二进制当文本导入")
	}
	if UploadSupported("a.DOCX") != true {
		t.Fatal("扩展名匹配应当大小写不敏感")
	}
	if !UploadSupported("a.PDF") {
		t.Fatal("PDF 现在受支持，且扩展名匹配应当大小写不敏感")
	}
}

// PDF 现在能被摄取了，但它取的是**文字层**。
// 没有文字层的扫描件必须被明确告知改走截图，而不是静默返回空文档——
// 后者会让用户看到"识别出 0 道题"，然后怀疑系统坏了。
func TestIngestPDFReportsMissingTextLayer(t *testing.T) {
	pdf := minimalPDF(t)
	_, err := Ingest(pdf, "scan.pdf", nil)
	if err == nil {
		t.Fatal("没有文字层的 PDF 应当报错")
	}
	if !strings.Contains(err.Error(), "文字层") {
		t.Fatalf("错误信息应当说清是没有文字层，实际 %v", err)
	}
	if !strings.Contains(err.Error(), "上传图片") {
		t.Fatalf("错误信息应当给出下一步该怎么做，实际 %v", err)
	}
}

// 有文字层的 PDF 应当带上"来源"说明：文本层里的公式是拆开的，
// 不告诉模型的话，它会把 x2 当成正文、把碎片当成多出来的段落。
func TestIngestPDFMarksProvenance(t *testing.T) {
	pdf := textPDF(t, "BT /F1 12 Tf 72 700 Td (1. What is 1 + 1) Tj ET")

	res, err := Ingest(pdf, "paper.pdf", nil)
	if err != nil {
		t.Fatalf("摄取失败: %v", err)
	}
	if !strings.HasPrefix(res.Docs[0].Text, PDFTextPrefix) {
		t.Fatalf("PDF 文本应当带来源说明，实际开头是 %q", truncate(res.Docs[0].Text, 40))
	}
	if !strings.Contains(res.Docs[0].Text, "1. What is 1 + 1") {
		t.Fatalf("正文没有取到: %q", res.Docs[0].Text)
	}
	var noted bool
	for _, w := range res.Warnings {
		if strings.Contains(w, "PDF 版面提示") {
			noted = true
		}
	}
	if !noted {
		t.Fatalf("应当给出 PDF 版面提示，实际 %v", res.Warnings)
	}
}
