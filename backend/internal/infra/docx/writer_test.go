package docx

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"github.com/NanamiKyoka/questionmind/internal/export"
)

// testImage 生成一张 2x2 的 PNG，避免在测试里硬编码二进制。
func testImage(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	img.Set(1, 1, color.RGBA{B: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("生成测试图片失败: %v", err)
	}
	return buf.Bytes()
}

// sampleDocument 构造一份覆盖各类块的文档。
func sampleDocument(t *testing.T) *export.Document {
	t.Helper()
	return &export.Document{
		Title: "高一数学月考卷",
		Blocks: []export.Block{
			&export.Heading{Level: 1, Runs: []export.Inline{&export.Text{Value: "一、选择题", Bold: true}}},
			&export.Paragraph{
				Align: export.AlignCenter,
				Runs: []export.Inline{
					&export.Text{Value: "1．", Bold: true},
					&export.Text{Value: "已知函数 "},
					&export.Math{LaTeX: `f(x) = \frac{1}{2}x^2 + \sqrt{x}`},
					&export.Text{Value: " ，则 "},
					&export.Math{LaTeX: `f'(1)`},
					&export.Text{Value: " = "},
				},
			},
			&export.Table{
				Borderless: true,
				Widths:     []int{1, 1},
				Rows: [][]export.Cell{
					{{Blocks: []export.Block{&export.Paragraph{Runs: []export.Inline{&export.Text{Value: "A．1"}}}}},
						{Blocks: []export.Block{&export.Paragraph{Runs: []export.Inline{&export.Text{Value: "B．2"}}}}}},
					{{Blocks: []export.Block{&export.Paragraph{Runs: []export.Inline{&export.Text{Value: "C．3"}}}}},
						{Blocks: []export.Block{&export.Paragraph{Runs: []export.Inline{&export.Text{Value: "D．4"}}}}}},
				},
			},
			&export.Paragraph{Runs: []export.Inline{
				&export.Text{Value: "角度标记 "},
				&export.Text{Value: "x"},
				&export.Text{Value: "2", VertAlign: "superscript"},
			}},
			&export.Paragraph{Runs: []export.Inline{&export.Image{Data: testImage(t), Ext: "png", WidthPt: 120, HeightPt: 80}}},
			&export.PageBreak{},
			&export.Table{
				Rows: [][]export.Cell{
					{{Blocks: []export.Block{&export.Paragraph{Runs: []export.Inline{&export.Text{Value: "题号"}}}}},
						{Blocks: []export.Block{&export.Paragraph{Runs: []export.Inline{&export.Text{Value: "答案"}}}}}},
					{{Blocks: []export.Block{&export.Paragraph{Runs: []export.Inline{&export.Text{Value: "1"}}}}},
						{Blocks: []export.Block{&export.Paragraph{Runs: []export.Inline{&export.Text{Value: "B"}}}}}},
				},
			},
			&export.ListItem{Numbered: true, Runs: []export.Inline{&export.Text{Value: "编号条目"}}},
			&export.ListItem{Numbered: false, Runs: []export.Inline{&export.Text{Value: "项目条目"}}},
		},
	}
}

// openParts 解压 docx 并返回各部件内容。
func openParts(t *testing.T, data []byte) map[string][]byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("生成的 docx 不是合法 zip: %v", err)
	}
	parts := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("打开部件 %s 失败: %v", f.Name, err)
		}
		var buf bytes.Buffer
		if _, err := buf.ReadFrom(rc); err != nil {
			t.Fatalf("读取部件 %s 失败: %v", f.Name, err)
		}
		_ = rc.Close()
		parts[f.Name] = buf.Bytes()
	}
	return parts
}

// OOXML 包必须包含这些部件，缺一个 Word 就打不开。
func TestWriteProducesRequiredParts(t *testing.T) {
	data, err := Write(sampleDocument(t))
	if err != nil {
		t.Fatalf("Write 失败: %v", err)
	}
	parts := openParts(t, data)

	required := []string{
		"[Content_Types].xml",
		"_rels/.rels",
		"word/document.xml",
		"word/_rels/document.xml.rels",
		"word/styles.xml",
		"word/numbering.xml",
	}
	for _, name := range required {
		if _, ok := parts[name]; !ok {
			t.Fatalf("缺少必需部件 %s", name)
		}
	}
}

// 所有 XML 部件都必须是格式良好的，否则 Word 会报"文件已损坏"。
func TestWriteProducesWellFormedXML(t *testing.T) {
	data, err := Write(sampleDocument(t))
	if err != nil {
		t.Fatalf("Write 失败: %v", err)
	}
	for name, content := range openParts(t, data) {
		if !strings.HasSuffix(name, ".xml") && !strings.HasSuffix(name, ".rels") {
			continue
		}
		dec := xml.NewDecoder(bytes.NewReader(content))
		for {
			_, err := dec.Token()
			if err != nil {
				if err.Error() == "EOF" {
					break
				}
				t.Fatalf("部件 %s 的 XML 不合法: %v", name, err)
			}
		}
	}
}

// 文档里出现的每个 r:embed 都必须有对应的关系项，
// 否则图片在 Word 里会显示成红叉。
func TestWriteImageRelationshipsAreConsistent(t *testing.T) {
	data, err := Write(sampleDocument(t))
	if err != nil {
		t.Fatalf("Write 失败: %v", err)
	}
	parts := openParts(t, data)
	doc := string(parts["word/document.xml"])
	rels := string(parts["word/_rels/document.xml.rels"])

	embeds := extractEmbeds(doc)
	if len(embeds) == 0 {
		t.Fatal("文档里应至少有一个图片引用")
	}
	for _, id := range embeds {
		if !strings.Contains(rels, `Id="`+id+`"`) {
			t.Fatalf("图片关系 %s 在 document.xml.rels 中缺失", id)
		}
	}

	// 关系里指向的媒体文件必须真的在包里。
	for _, id := range embeds {
		target := relTarget(rels, id)
		if target == "" {
			t.Fatalf("关系 %s 没有 Target", id)
		}
		if _, ok := parts["word/"+target]; !ok {
			t.Fatalf("关系 %s 指向的 %s 不存在于包内", id, target)
		}
	}
}

// 图片扩展名必须在 [Content_Types].xml 里登记，否则 Word 拒绝打开。
func TestWriteDeclaresImageContentType(t *testing.T) {
	data, err := Write(sampleDocument(t))
	if err != nil {
		t.Fatalf("Write 失败: %v", err)
	}
	ct := string(openParts(t, data)["[Content_Types].xml"])
	if !strings.Contains(ct, `Extension="png"`) {
		t.Fatalf("[Content_Types].xml 未登记 png: %s", ct)
	}
	if !strings.Contains(ct, "image/png") {
		t.Fatalf("[Content_Types].xml 未给出 png 的 MIME: %s", ct)
	}
}

// 公式必须作为 OMML 出现在文档里，且命名空间已声明。
func TestWriteEmbedsOMMLMath(t *testing.T) {
	data, err := Write(sampleDocument(t))
	if err != nil {
		t.Fatalf("Write 失败: %v", err)
	}
	doc := string(openParts(t, data)["word/document.xml"])

	if !strings.Contains(doc, "<m:oMath>") {
		t.Fatalf("文档应包含 OMML 公式: %s", truncate(doc))
	}
	if !strings.Contains(doc, `xmlns:m="http://schemas.openxmlformats.org/officeDocument/2006/math"`) {
		t.Fatal("文档根节点应声明 m 命名空间，否则公式无法识别")
	}
	if !strings.Contains(doc, "<m:f>") {
		t.Fatal("分式应被渲染为 OMML 的 m:f")
	}
	// LaTeX 源码不应残留在文档文本里。
	if strings.Contains(doc, `\frac`) {
		t.Fatalf("不应残留 LaTeX 源码: %s", truncate(doc))
	}
}

func TestWriteEscapesSpecialCharacters(t *testing.T) {
	doc := &export.Document{
		Title: "t",
		Blocks: []export.Block{
			&export.Paragraph{Runs: []export.Inline{
				&export.Text{Value: `a<b & c>"d" 'e'`},
			}},
		},
	}
	data, err := Write(doc)
	if err != nil {
		t.Fatalf("Write 失败: %v", err)
	}
	body := string(openParts(t, data)["word/document.xml"])

	if strings.Contains(body, "a<b") {
		t.Fatal("小于号未被转义，会破坏 XML")
	}
	for _, want := range []string{"&lt;", "&amp;", "&gt;", "&quot;", "&apos;"} {
		if !strings.Contains(body, want) {
			t.Fatalf("缺少转义序列 %s", want)
		}
	}
}

// 空文档也要能生成合法的 docx，而不是报错或产出坏包。
func TestWriteEmptyDocument(t *testing.T) {
	data, err := Write(&export.Document{})
	if err != nil {
		t.Fatalf("空文档不应报错: %v", err)
	}
	parts := openParts(t, data)
	if _, ok := parts["word/document.xml"]; !ok {
		t.Fatal("空文档也应包含 document.xml")
	}
	if !strings.Contains(string(parts["word/document.xml"]), "<w:sectPr>") {
		t.Fatal("文档应包含节属性（页面尺寸与页边距）")
	}
}

func TestWriteNilDocument(t *testing.T) {
	if _, err := Write(nil); err == nil {
		t.Fatal("nil 文档应返回错误")
	}
}

// 同一张图重复出现时只应写入一次，减小文件体积。
func TestWriteDeduplicatesImages(t *testing.T) {
	img := testImage(t)
	doc := &export.Document{
		Blocks: []export.Block{
			&export.Paragraph{Runs: []export.Inline{&export.Image{Data: img, Ext: "png"}}},
			&export.Paragraph{Runs: []export.Inline{&export.Image{Data: img, Ext: "png"}}},
		},
	}
	data, err := Write(doc)
	if err != nil {
		t.Fatalf("Write 失败: %v", err)
	}
	media := 0
	for name := range openParts(t, data) {
		if strings.HasPrefix(name, "word/media/") {
			media++
		}
	}
	if media != 1 {
		t.Fatalf("相同图片应只写入一次，实际 %d 次", media)
	}
}

// 列表编号由 numbering.xml 驱动，而不是写死在文本里。
func TestWriteUsesNumbering(t *testing.T) {
	data, err := Write(sampleDocument(t))
	if err != nil {
		t.Fatalf("Write 失败: %v", err)
	}
	parts := openParts(t, data)
	doc := string(parts["word/document.xml"])
	numbering := string(parts["word/numbering.xml"])

	if !strings.Contains(doc, `<w:numId w:val="1"/>`) {
		t.Fatal("数字编号条目应引用 numId=1")
	}
	if !strings.Contains(doc, `<w:numId w:val="2"/>`) {
		t.Fatal("项目符号条目应引用 numId=2")
	}
	if !strings.Contains(numbering, `w:numId="1"`) || !strings.Contains(numbering, `w:numId="2"`) {
		t.Fatal("numbering.xml 应同时定义两套编号")
	}
}

// 分页符必须是 w:br type="page"。
func TestWritePageBreak(t *testing.T) {
	doc := &export.Document{Blocks: []export.Block{&export.PageBreak{}}}
	data, err := Write(doc)
	if err != nil {
		t.Fatalf("Write 失败: %v", err)
	}
	body := string(openParts(t, data)["word/document.xml"])
	if !strings.Contains(body, `<w:br w:type="page"/>`) {
		t.Fatalf("应输出分页符: %s", body)
	}
}

// 无边框表格用于选项并排，必须显式取消框线。
func TestWriteBorderlessTable(t *testing.T) {
	doc := &export.Document{Blocks: []export.Block{
		&export.Table{Borderless: true, Rows: [][]export.Cell{
			{{Blocks: []export.Block{&export.Paragraph{Runs: []export.Inline{&export.Text{Value: "A"}}}}}},
		}},
	}}
	data, err := Write(doc)
	if err != nil {
		t.Fatalf("Write 失败: %v", err)
	}
	body := string(openParts(t, data)["word/document.xml"])
	if !strings.Contains(body, `<w:top w:val="none"`) {
		t.Fatalf("无边框表格应取消框线: %s", truncate(body))
	}
}

func TestImageExtentKeepsAspectAndClampsWidth(t *testing.T) {
	// 超宽图应被缩到正文宽度并保持比例。
	cx, cy := imageExtent(&export.Image{WidthPt: 840, HeightPt: 200})
	if cx != int(420*EMUPerPoint) {
		t.Fatalf("宽度应被限制为 420pt，实际 %d EMU", cx)
	}
	if cy != int(100*EMUPerPoint) {
		t.Fatalf("高度应按比例缩为 100pt，实际 %d EMU", cy)
	}

	// 未指定尺寸时应有合理默认值，不能是 0（否则图片不可见）。
	cx2, cy2 := imageExtent(&export.Image{})
	if cx2 <= 0 || cy2 <= 0 {
		t.Fatalf("默认尺寸必须为正，实际 %d x %d", cx2, cy2)
	}
}

// ---- 辅助 ----

func extractEmbeds(doc string) []string {
	var out []string
	for {
		i := strings.Index(doc, `r:embed="`)
		if i < 0 {
			break
		}
		doc = doc[i+len(`r:embed="`):]
		j := strings.Index(doc, `"`)
		if j < 0 {
			break
		}
		out = append(out, doc[:j])
		doc = doc[j:]
	}
	return out
}

func relTarget(rels, id string) string {
	marker := fmt.Sprintf(`Id="%s"`, id)
	i := strings.Index(rels, marker)
	if i < 0 {
		return ""
	}
	rest := rels[i:]
	t := strings.Index(rest, `Target="`)
	if t < 0 {
		return ""
	}
	rest = rest[t+len(`Target="`):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		return ""
	}
	return rest[:j]
}

func truncate(s string) string {
	if len(s) > 400 {
		return s[:400] + "..."
	}
	return s
}
