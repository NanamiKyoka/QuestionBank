package importing

import (
	"strings"
	"testing"
)

func TestDocxPreservesEmbeddedImages(t *testing.T) {
	doc := `<w:p><w:r><w:t>函数图像：</w:t></w:r><w:r><w:drawing><wp:inline xmlns:wp="http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing"><a:blip xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" r:embed="rId5"/></wp:inline></w:drawing></w:r></w:p>`
	data := buildZip(t, map[string]string{
		docxDocumentPart: `<?xml version="1.0" encoding="UTF-8"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><w:body>` + doc + `</w:body></w:document>`,
		docxRelationshipsPart: `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId5" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="media/image1.png"/></Relationships>`,
		docxMediaPrefix + "image1.png": "png-bytes",
	})
	saver := &countingSaver{}
	got, warns, err := DocxToMarkdown(data, saver)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "![图片](/media/image1.png)") || saver.saved != 1 {
		t.Fatalf("内嵌图片未保留: text=%q saved=%d warns=%v", got, saver.saved, warns)
	}
}

func TestDocxPreservesNestedTables(t *testing.T) {
	doc := `<w:tbl><w:tr><w:tc><w:p><w:r><w:t>外层</w:t></w:r></w:p>` +
		`<w:tbl><w:tr><w:tc><w:p><w:r><w:t>内层甲</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>内层乙</w:t></w:r></w:p></w:tc></w:tr></w:tbl>` +
		`</w:tc><w:tc><w:p><w:r><w:t>尾列</w:t></w:r></w:p></w:tc></w:tr></w:tbl>`
	got, _, err := DocxToMarkdown(buildDocx(t, doc), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"外层", "内层甲", "内层乙", "尾列"} {
		if !strings.Contains(got, want) {
			t.Fatalf("嵌套表格丢失 %q:\n%s", want, got)
		}
	}
}

func TestDocxPreservesFootnotes(t *testing.T) {
	doc := `<w:p><w:r><w:t>函数极限</w:t></w:r><w:r><w:footnoteReference w:id="2"/></w:r><w:r><w:t>的定义。</w:t></w:r></w:p>`
	data := buildZip(t, map[string]string{
		docxDocumentPart: `<?xml version="1.0" encoding="UTF-8"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>` + doc + `</w:body></w:document>`,
		docxFootnotesPart: `<?xml version="1.0" encoding="UTF-8"?>
<w:footnotes xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:footnote w:id="2"><w:p><w:r><w:t>参见教材第 3 章</w:t></w:r></w:p></w:footnote></w:footnotes>`,
	})
	got, _, err := DocxToMarkdown(data, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "函数极限[脚注：参见教材第 3 章]的定义。") {
		t.Fatalf("脚注未保留: %q", got)
	}
}
