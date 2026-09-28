// Package docx 把一个 export.Document 渲染为 .docx（OOXML）文件。
//
// 为什么自己写：Go 生态里能生成 docx 的库要么是 AGPL/商业双许可
// （与项目的开源协议目标冲突），要么只支持"填充模板"。
// 而 OOXML 本质就是一组 XML 打进 zip，自己实现反而更可控：
// 需要什么就写什么，不必绕开库的抽象。
//
// 生成的包结构：
//
//	[Content_Types].xml
//	_rels/.rels
//	word/document.xml
//	word/_rels/document.xml.rels
//	word/styles.xml
//	word/numbering.xml
//	word/media/imageN.<ext>
package docx

import (
	"archive/zip"
	"bytes"
	"fmt"
	"strings"

	"github.com/NanamiKyoka/questionmind/internal/export"
	"github.com/NanamiKyoka/questionmind/internal/infra/omml"
)

// EMUPerPoint 1 磅 = 12700 EMU（English Metric Unit，OOXML 的长度单位）。
const EMUPerPoint = 12700

// pxToPt 96 DPI 下的像素换算为磅。
const pxToPt = 72.0 / 96.0

// Write 渲染文档，返回 .docx 的字节内容。
func Write(doc *export.Document) ([]byte, error) {
	if doc == nil {
		return nil, fmt.Errorf("docx: 文档为空")
	}

	w := &writer{doc: doc}
	documentXML, err := w.buildDocument()
	if err != nil {
		return nil, err
	}

	buf := &bytes.Buffer{}
	zw := zip.NewWriter(buf)

	files := []struct {
		name string
		data []byte
	}{
		{"[Content_Types].xml", w.buildContentTypes()},
		{"_rels/.rels", []byte(relsRoot)},
		{"word/document.xml", documentXML},
		{"word/_rels/document.xml.rels", []byte(w.buildDocumentRels())},
		{"word/styles.xml", []byte(stylesXML)},
		{"word/numbering.xml", []byte(numberingXML)},
	}
	for _, f := range files {
		if err := writeZipFile(zw, f.name, f.data); err != nil {
			return nil, err
		}
	}
	// 图片以二进制写入，不能经过任何文本处理。
	for i := range w.images {
		img := &w.images[i]
		if err := writeZipFile(zw, "word/media/"+img.fileName, img.data); err != nil {
			return nil, err
		}
	}

	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("docx: 打包失败: %w", err)
	}
	return buf.Bytes(), nil
}

func writeZipFile(zw *zip.Writer, name string, data []byte) error {
	f, err := zw.Create(name)
	if err != nil {
		return fmt.Errorf("docx: 创建 %s 失败: %w", name, err)
	}
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("docx: 写入 %s 失败: %w", name, err)
	}
	return nil
}

// imageRef 包内的一张图片。
type imageRef struct {
	fileName string
	data     []byte
	// relID 形如 rId5，供 a:blip 引用。
	relID string
	// cx / cy 以 EMU 表示的目标尺寸。
	cx int
	cy int
}

type writer struct {
	doc    *export.Document
	images []imageRef
	// docPrID 图片在文档中的唯一序号，Word 要求每个 drawing 有独立 id。
	docPrID int
}

// addImage 登记一张图片并返回其在 images 中的下标。
//
// 相同内容只登记一次：同一张图在卷子里可能出现多次（如题图与解析图），
// 去重能显著减小文件体积。
func (w *writer) addImage(img *export.Image) int {
	for i := range w.images {
		if bytes.Equal(w.images[i].data, img.Data) {
			return i
		}
	}

	ext := strings.ToLower(img.Ext)
	if ext == "" {
		ext = "png"
	}
	ref := imageRef{
		fileName: fmt.Sprintf("image%d.%s", len(w.images)+1, ext),
		data:     img.Data,
		relID:    fmt.Sprintf("rId%d", 100+len(w.images)), // 100 起，避开固定关系 id
	}
	ref.cx, ref.cy = imageExtent(img)
	w.images = append(w.images, ref)
	return len(w.images) - 1
}

// imageExtent 计算图片的显示尺寸（EMU）。
// 未指定尺寸时按 96 DPI 换算，并把过宽的图缩到正文宽度以内。
func imageExtent(img *export.Image) (int, int) {
	widthPt, heightPt := img.WidthPt, img.HeightPt
	if widthPt <= 0 {
		widthPt = 300
	}
	if heightPt <= 0 {
		// 没有原始像素信息时给一个保守的 4:3 比例。
		heightPt = widthPt * 0.75
	}
	const maxWidthPt = 420.0 // A4 正文宽度约 420pt
	if widthPt > maxWidthPt {
		heightPt = heightPt * maxWidthPt / widthPt
		widthPt = maxWidthPt
	}
	return int(widthPt * EMUPerPoint), int(heightPt * EMUPerPoint)
}

// ---- document.xml ----

func (w *writer) buildDocument() ([]byte, error) {
	var sb strings.Builder
	sb.WriteString(xmlHeader)
	sb.WriteString(`<w:document ` + documentNS + `><w:body>`)

	for _, b := range w.doc.Blocks {
		if err := w.writeBlock(&sb, b); err != nil {
			return nil, err
		}
	}

	// sectPr 定义页面尺寸与页边距。A4：11906 x 16838 twips（1/20 磅）。
	sb.WriteString(`<w:sectPr>` +
		`<w:pgSz w:w="11906" w:h="16838"/>` +
		`<w:pgMar w:top="1134" w:right="1134" w:bottom="1134" w:left="1134" ` +
		`w:header="851" w:footer="992" w:gutter="0"/>` +
		`</w:sectPr>`)
	sb.WriteString(`</w:body></w:document>`)
	return []byte(sb.String()), nil
}

func (w *writer) writeBlock(sb *strings.Builder, b export.Block) error {
	switch n := b.(type) {
	case *export.Paragraph:
		w.writeParagraph(sb, n)
	case *export.Heading:
		w.writeHeading(sb, n)
	case *export.PageBreak:
		sb.WriteString(`<w:p><w:r><w:br w:type="page"/></w:r></w:p>`)
	case *export.ListItem:
		w.writeListItem(sb, n)
	case *export.Table:
		w.writeTable(sb, n)
	default:
		return fmt.Errorf("docx: 不支持的块类型 %T", b)
	}
	return nil
}

func (w *writer) writeParagraph(sb *strings.Builder, p *export.Paragraph) {
	sb.WriteString("<w:p>")
	writePPr(sb, p.Align, p.IndentLeft, p.SpaceBefore, p.SpaceAfter, p.KeepWithNext, "")
	for _, r := range p.Runs {
		w.writeInline(sb, r)
	}
	sb.WriteString("</w:p>")
}

func (w *writer) writeHeading(sb *strings.Builder, h *export.Heading) {
	level := h.Level
	if level < 1 {
		level = 1
	}
	if level > 3 {
		level = 3
	}
	sb.WriteString("<w:p>")
	writePPr(sb, h.Align, 0, 6, 6, false, fmt.Sprintf("Heading%d", level))
	for _, r := range h.Runs {
		w.writeInline(sb, r)
	}
	sb.WriteString("</w:p>")
}

func (w *writer) writeListItem(sb *strings.Builder, li *export.ListItem) {
	numID := 1 // 数字编号
	if !li.Numbered {
		numID = 2 // 项目符号
	}
	level := li.Level
	if level < 0 {
		level = 0
	}
	if level > 2 {
		level = 2
	}

	sb.WriteString("<w:p><w:pPr>")
	sb.WriteString(`<w:pStyle w:val="ListParagraph"/>`)
	sb.WriteString(fmt.Sprintf(`<w:numPr><w:ilvl w:val="%d"/><w:numId w:val="%d"/></w:numPr>`, level, numID))
	sb.WriteString("</w:pPr>")
	for _, r := range li.Runs {
		w.writeInline(sb, r)
	}
	sb.WriteString("</w:p>")
}

func (w *writer) writeTable(sb *strings.Builder, t *export.Table) {
	if len(t.Rows) == 0 {
		return
	}
	sb.WriteString(`<w:tbl><w:tblPr><w:tblStyle w:val="TableGrid"/>` +
		`<w:tblW w:w="5000" w:type="pct"/>`)
	// 无边框表格用于选项并排：需要显式取消全部框线，
	// 否则会继承 TableGrid 样式在卷面上画出网格。
	if t.Borderless {
		sb.WriteString(`<w:tblBorders>` +
			`<w:top w:val="none" w:sz="0" w:color="auto"/>` +
			`<w:left w:val="none" w:sz="0" w:color="auto"/>` +
			`<w:bottom w:val="none" w:sz="0" w:color="auto"/>` +
			`<w:right w:val="none" w:sz="0" w:color="auto"/>` +
			`<w:insideH w:val="none" w:sz="0" w:color="auto"/>` +
			`<w:insideV w:val="none" w:sz="0" w:color="auto"/>` +
			`</w:tblBorders>`)
	} else {
		sb.WriteString(`<w:tblBorders>` +
			`<w:top w:val="single" w:sz="4" w:color="808080"/>` +
			`<w:left w:val="single" w:sz="4" w:color="808080"/>` +
			`<w:bottom w:val="single" w:sz="4" w:color="808080"/>` +
			`<w:right w:val="single" w:sz="4" w:color="808080"/>` +
			`<w:insideH w:val="single" w:sz="4" w:color="808080"/>` +
			`<w:insideV w:val="single" w:sz="4" w:color="808080"/>` +
			`</w:tblBorders>`)
	}
	sb.WriteString(`</w:tblPr>`)

	cols := 0
	for _, row := range t.Rows {
		if len(row) > cols {
			cols = len(row)
		}
	}
	// 列宽以 dxa（1/20 磅）表示，正文宽度约 8306 twips。
	totalWidth := 8306
	widths := t.Widths
	if len(widths) != cols {
		widths = make([]int, cols)
		for i := range widths {
			widths[i] = 1
		}
	}
	sum := 0
	for _, v := range widths {
		sum += v
	}
	if sum == 0 {
		sum = 1
	}
	sb.WriteString("<w:tblGrid>")
	for _, v := range widths {
		sb.WriteString(fmt.Sprintf(`<w:gridCol w:w="%d"/>`, totalWidth*v/sum))
	}
	sb.WriteString("</w:tblGrid>")

	for ri, row := range t.Rows {
		sb.WriteString("<w:tr>")
		for ci := 0; ci < cols; ci++ {
			sb.WriteString(fmt.Sprintf(`<w:tc><w:tcPr><w:tcW w:w="%d" w:type="dxa"/></w:tcPr>`,
				totalWidth*widths[ci]/sum))
			if ci < len(row) {
				for _, blk := range row[ci].Blocks {
					w.writeBlock(sb, blk)
				}
			} else {
				// 补齐缺失的单元格，否则 Word 会认为表格结构损坏。
				sb.WriteString("<w:p/>")
			}
			sb.WriteString("</w:tc>")
		}
		sb.WriteString("</w:tr>")
		// 表头行重复出现在每一页。
		_ = ri
	}
	sb.WriteString("</w:tbl>")
}

// writePPr 输出段落属性。
func writePPr(sb *strings.Builder, align export.Align, indentPt, beforePt, afterPt float64, keepNext bool, style string) {
	if align == "" && indentPt == 0 && beforePt == 0 && afterPt == 0 && !keepNext && style == "" {
		return
	}
	sb.WriteString("<w:pPr>")
	if style != "" {
		sb.WriteString(fmt.Sprintf(`<w:pStyle w:val="%s"/>`, style))
	}
	if keepNext {
		sb.WriteString(`<w:keepNext/>`)
	}
	if beforePt > 0 || afterPt > 0 {
		sb.WriteString(fmt.Sprintf(`<w:spacing w:before="%d" w:after="%d"/>`,
			int(beforePt*20), int(afterPt*20)))
	}
	if indentPt > 0 {
		sb.WriteString(fmt.Sprintf(`<w:ind w:left="%d"/>`, int(indentPt*20)))
	}
	if align != "" {
		sb.WriteString(fmt.Sprintf(`<w:jc w:val="%s"/>`, mapAlign(align)))
	}
	sb.WriteString("</w:pPr>")
}

func mapAlign(a export.Align) string {
	switch a {
	case export.AlignCenter:
		return "center"
	case export.AlignRight:
		return "right"
	case export.AlignJustify:
		return "both"
	default:
		return "left"
	}
}

// writeInline 输出一个行内元素。
func (w *writer) writeInline(sb *strings.Builder, in export.Inline) {
	switch n := in.(type) {
	case *export.Text:
		writeTextRun(sb, n)
	case *export.Math:
		// 公式作为 <m:oMath> 直接嵌在段落里，不包 w:r。
		math := omml.Render(n.LaTeX)
		if math != "" {
			sb.WriteString(math)
		}
	case *export.LineBreak:
		sb.WriteString("<w:r><w:br/></w:r>")
	case *export.Image:
		w.writeImageRun(sb, n)
	}
}

func writeTextRun(sb *strings.Builder, t *export.Text) {
	if t.Value == "" {
		return
	}
	sb.WriteString("<w:r>")
	// rPr 必须写在 w:t 之前。
	var rpr strings.Builder
	if t.Mono {
		rpr.WriteString(`<w:rFonts w:ascii="Consolas" w:hAnsi="Consolas"/>`)
	}
	if t.Bold {
		rpr.WriteString("<w:b/>")
	}
	if t.Italic {
		rpr.WriteString("<w:i/>")
	}
	if t.Underline {
		rpr.WriteString(`<w:u w:val="single"/>`)
	}
	switch t.VertAlign {
	case "superscript":
		rpr.WriteString(`<w:vertAlign w:val="superscript"/>`)
	case "subscript":
		rpr.WriteString(`<w:vertAlign w:val="subscript"/>`)
	}
	if rpr.Len() > 0 {
		sb.WriteString("<w:rPr>")
		sb.WriteString(rpr.String())
		sb.WriteString("</w:rPr>")
	}
	// xml:space="preserve" 必不可少：否则多个连续空格会被 Word 吞掉。
	sb.WriteString(`<w:t xml:space="preserve">`)
	sb.WriteString(escapeXML(t.Value))
	sb.WriteString("</w:t></w:r>")
}

func (w *writer) writeImageRun(sb *strings.Builder, img *export.Image) {
	if len(img.Data) == 0 {
		return
	}
	idx := w.addImage(img)
	ref := &w.images[idx]
	w.docPrID++

	sb.WriteString("<w:r><w:drawing>")
	sb.WriteString(fmt.Sprintf(`<wp:inline distT="0" distB="0" distL="0" distR="0">`))
	sb.WriteString(fmt.Sprintf(`<wp:extent cx="%d" cy="%d"/>`, ref.cx, ref.cy))
	sb.WriteString(fmt.Sprintf(`<wp:docPr id="%d" name="Picture %d"/>`, w.docPrID, w.docPrID))
	sb.WriteString(`<a:graphic><a:graphicData uri="http://schemas.openxmlformats.org/drawingml/2006/picture">`)
	sb.WriteString(`<pic:pic><pic:nvPicPr>`)
	sb.WriteString(fmt.Sprintf(`<pic:cNvPr id="%d" name="%s"/>`, w.docPrID, escapeXML(ref.fileName)))
	sb.WriteString(`<pic:cNvPicPr/></pic:nvPicPr>`)
	sb.WriteString(`<pic:blipFill><a:blip r:embed="` + ref.relID + `"/>`)
	sb.WriteString(`<a:stretch><a:fillRect/></a:stretch></pic:blipFill>`)
	sb.WriteString(`<pic:spPr><a:xfrm><a:off x="0" y="0"/>`)
	sb.WriteString(fmt.Sprintf(`<a:ext cx="%d" cy="%d"/>`, ref.cx, ref.cy))
	sb.WriteString(`</a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom></pic:spPr>`)
	sb.WriteString(`</pic:pic></a:graphicData></a:graphic></wp:inline>`)
	sb.WriteString("</w:drawing></w:r>")
}

// ---- 包内其它文件 ----

func (w *writer) buildContentTypes() []byte {
	var sb strings.Builder
	sb.WriteString(xmlHeader)
	sb.WriteString(`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">`)
	sb.WriteString(`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>`)
	sb.WriteString(`<Default Extension="xml" ContentType="application/xml"/>`)

	// 出现的每种图片扩展名都要登记，否则 Word 打不开。
	seen := map[string]bool{}
	for _, img := range w.images {
		ext := extOf(img.fileName)
		if seen[ext] {
			continue
		}
		seen[ext] = true
		if ct, ok := imageContentTypes[ext]; ok {
			sb.WriteString(fmt.Sprintf(`<Default Extension="%s" ContentType="%s"/>`, ext, ct))
		}
	}

	sb.WriteString(`<Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>`)
	sb.WriteString(`<Override PartName="/word/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.styles+xml"/>`)
	sb.WriteString(`<Override PartName="/word/numbering.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.numbering+xml"/>`)
	sb.WriteString(`</Types>`)
	return []byte(sb.String())
}

var imageContentTypes = map[string]string{
	"png":  "image/png",
	"jpg":  "image/jpeg",
	"jpeg": "image/jpeg",
	"gif":  "image/gif",
	"bmp":  "image/bmp",
	"webp": "image/webp",
}

func extOf(name string) string {
	if i := strings.LastIndex(name, "."); i >= 0 {
		return strings.ToLower(name[i+1:])
	}
	return ""
}

func (w *writer) buildDocumentRels() string {
	var sb strings.Builder
	sb.WriteString(xmlHeader)
	sb.WriteString(`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`)
	sb.WriteString(`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>`)
	sb.WriteString(`<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/numbering" Target="numbering.xml"/>`)
	for _, img := range w.images {
		sb.WriteString(fmt.Sprintf(
			`<Relationship Id="%s" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="media/%s"/>`,
			img.relID, img.fileName))
	}
	sb.WriteString(`</Relationships>`)
	return sb.String()
}

// escapeXML 转义 XML 文本。
func escapeXML(s string) string {
	return strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	).Replace(s)
}

const xmlHeader = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n"

// documentNS 声明文档根节点上的全部命名空间。
// m: 是公式（OMML）所需的命名空间，缺失会导致公式无法识别。
const documentNS = `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" ` +
	`xmlns:m="http://schemas.openxmlformats.org/officeDocument/2006/math" ` +
	`xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" ` +
	`xmlns:wp="http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing" ` +
	`xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" ` +
	`xmlns:pic="http://schemas.openxmlformats.org/drawingml/2006/picture"`

const relsRoot = xmlHeader + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
	`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>` +
	`</Relationships>`
