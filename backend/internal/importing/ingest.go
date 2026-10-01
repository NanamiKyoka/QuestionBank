package importing

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"

	"github.com/NanamiKyoka/questionmind/internal/pdftext"
)

// 文档摄取：把上传的文件变成"可交给规则解析器或模型的文本"。
//
// 为什么自己读 .docx 而不用现成库：本项目要求 CGO_ENABLED=0，
// 而且已经在自研 OOXML 写出器（internal/infra/docx），
// 再引一个只读不写的库反而多一份依赖与一份信任。
// .docx 本质是一个 zip，document.xml 是 OOXML 流式可解析的，
// 需要的只是"按段落取出文本 + 把 OMML 公式转成 LaTeX"。

// ImageSaver 保存文档内嵌图片，返回可写进 Markdown 的引用。
// 由 service 层用 internal/infra/storage 注入，避免 importing 直接依赖存储实现。
type ImageSaver interface {
	Save(data []byte, name string) (string, error)
}

// IngestedDoc 摄取出来的一份文档。
type IngestedDoc struct {
	// Name 来源文件名。解析时写进每一道题的 SourceFile，
	// 审核界面靠它把题目按来源分组。
	Name string
	Text string
}

// IngestResult 一次摄取的产出。
type IngestResult struct {
	// Docs 归一后的文档列表。
	//
	// 刻意**逐份保留**而不是拼成一大段：一份卷子里的文末答案区是按题号
	// 回填的，两份都用 1/2/3 编号时，合并解析会让第一份的答案贴到第二份的题上。
	// 分开解析，答案区的作用域才不会越界。
	Docs []IngestedDoc
	// Files 本次覆盖到的文件名（= 各 Doc 的 Name）。
	Files []string
	// Warnings 摄取过程中的提示（如跳过了不支持的成员）。
	Warnings []string
}

// 摄取上限。都是为了挡住"一个压缩包把服务端打爆"这类输入。
const (
	// MaxArchiveFiles 归档内允许的条目数。
	MaxArchiveFiles = 2000
	// MaxArchiveBytes 归档解压后的总字节上限。
	MaxArchiveBytes = 200 << 20 // 200MB
	// MaxDocFileBytes 单个文档文件的大小上限。
	MaxDocFileBytes = 32 << 20 // 32MB
)

// 支持的上传扩展名。
//
// .pdf 在其中，但它走的不是"PDF 解析"那条路：PDF 是排版格式而非内容格式，
// 把它变成可读文字本身就是一项独立工程（见 internal/pdftext）。
// 这里只取文本层——有文字层的电子卷直接就能录，纯扫描件会被明确告知
// 改走截图识别，而不是悄悄返回一份 0 题的草稿。
var supportedUploadExt = map[string]bool{
	".md": true, ".markdown": true,
	".txt":  true,
	".docx": true,
	".pdf":  true,
	".zip":  true,
}

// UploadSupported 判断这个文件名是否是我们能处理的。
func UploadSupported(filename string) bool {
	return supportedUploadExt[strings.ToLower(path.Ext(filename))]
}

// UploadExtHint 面向用户的一句话格式说明。
// 界面、错误提示与文档共用同一份，免得三处各写一遍然后慢慢漂移。
func UploadExtHint() string { return ".md / .txt / .docx / .pdf / .zip" }

// Ingest 把上传内容转成文档文本。
//
// filename 用于决定格式与在结果里登记来源名。
func Ingest(data []byte, filename string, images ImageSaver) (*IngestResult, error) {
	if len(data) > MaxDocFileBytes {
		return nil, fmt.Errorf("文件过大（上限 %d MB）", MaxDocFileBytes>>20)
	}
	ext := strings.ToLower(path.Ext(filename))

	switch {
	case ext == ".zip":
		return ingestZip(data, filename, images)
	case ext == ".pdf":
		return ingestPDF(data, filename)
	case ext == ".docx":
		text, warn, err := DocxToMarkdown(data, images)
		if err != nil {
			return nil, err
		}
		return &IngestResult{
			Docs:     []IngestedDoc{{Name: filename, Text: text}},
			Files:    []string{filename},
			Warnings: warn,
		}, nil
	case ext == ".md" || ext == ".markdown" || ext == ".txt":
		return &IngestResult{
			Docs:  []IngestedDoc{{Name: filename, Text: string(data)}},
			Files: []string{filename},
		}, nil
	}
	return nil, fmt.Errorf("暂不支持的文件类型：%s", ext)
}

// PDFTextPrefix 标在 PDF 取出的文本前面，告诉模型（以及读日志的人）
// 这段文字的来历。
//
// 有必要：PDF 文本层里的公式是**按视觉位置拆开**的——x² 变成 "x2"，
// 分式变成上下两行。于是模型看到的是
// "3. 已知 sinα = ，则 cos2α = ．" 夹着孤零零的一行 "1" 和一行 "5"。
// 不说明来历的话，模型多半会把这些碎片当成正文里多出来的段落，
// 凭空多出好几道"题"。
const PDFTextPrefix = "<!-- 来源：PDF 文本层。公式与上下标已按视觉位置拆开，" +
	"分式的分子分母、根号、上下标可能落在相邻行；" +
	"请结合上下文还原成 LaTeX，例如 x2 还原为 x^2、竖排的分子分母还原为 \\frac{a}{b}。 -->\n"

// PDFLayoutNote 追加到文档级警告里的版面说明。
const PDFLayoutNote = "PDF 版面提示：公式与上下标在文本层里是拆开的，" +
	"分式的分子分母会落在相邻两行；几何图形只保留标注文字、不含线条，" +
	"请在识别结果里把公式还原为 LaTeX，图形部分留空由人工补图。"

// ingestPDF 从 PDF 的文本层取出题面文字。
//
// 刻意**不渲染页面**：渲染要完整实现字体、路径与图像的栅格化，
// 是另一个数量级的工程，而录题要的是题面文字。文本层加坐标已经
// 足够让模型还原公式与表格；图形部分如实告诉用户需要补图——
// 凭空生成一张画不出来却看着像那么回事的图，比说清楚更糟。
func ingestPDF(data []byte, filename string) (*IngestResult, error) {
	res, err := pdftext.Extract(data)
	if err != nil {
		return nil, err
	}
	if !res.HasText() {
		return nil, fmt.Errorf(
			"这份 PDF 没有可读取的文字层，多半是扫描件或纯图片；请改用「上传图片」逐页截图识别")
	}

	warnings := append([]string(nil), res.Warnings...)
	warnings = append(warnings, PDFLayoutNote)

	return &IngestResult{
		Docs:     []IngestedDoc{{Name: filename, Text: PDFTextPrefix + res.Text}},
		Files:    []string{filename},
		Warnings: warnings,
	}, nil
}

// ingestZip 展开包含 Markdown 的归档。
//
// 安全上必须先看**声明的**大小再解压：zip 头里的 uncompressed size
// 是压缩包作者写的，不可信，但足以挡掉最常见的压缩炸弹；
// 真正解压时还要按实际写入量再兜一次底。
func ingestZip(data []byte, filename string, images ImageSaver) (*IngestResult, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("压缩包无法打开：%w", err)
	}
	if len(zr.File) > MaxArchiveFiles {
		return nil, fmt.Errorf("压缩包内条目过多（%d 个，上限 %d）", len(zr.File), MaxArchiveFiles)
	}

	var declared int64
	for _, f := range zr.File {
		declared += int64(f.UncompressedSize64)
	}
	if declared > MaxArchiveBytes {
		return nil, fmt.Errorf("压缩包解压后约 %d MB，超过上限 %d MB", declared>>20, MaxArchiveBytes>>20)
	}

	var (
		docs  []IngestedDoc
		files []string
		warns []string
		total int64
	)
	for _, f := range zr.File {
		name := f.Name
		// zip-slip：条目名可能写成 "../../etc/passwd"。这里不落到磁盘，
		// 但仍按同一套规则判断并拒绝，避免以后改成落盘时再引入漏洞。
		if !safeArchiveName(name) {
			warns = append(warns, fmt.Sprintf("已跳过不安全的条目路径：%s", name))
			continue
		}
		ext := strings.ToLower(path.Ext(name))
		if !supportedUploadExt[ext] || strings.HasSuffix(strings.ToLower(name), "/") {
			continue
		}
		total += int64(f.UncompressedSize64)
		if total > MaxArchiveBytes {
			return nil, fmt.Errorf("压缩包解压后超过上限 %d MB", MaxArchiveBytes>>20)
		}

		rc, err := f.Open()
		if err != nil {
			warns = append(warns, fmt.Sprintf("无法读取 %s：%v", name, err))
			continue
		}
		buf, err := io.ReadAll(io.LimitReader(rc, MaxDocFileBytes+1))
		rc.Close()
		if err != nil {
			warns = append(warns, fmt.Sprintf("无法读取 %s：%v", name, err))
			continue
		}
		if len(buf) > MaxDocFileBytes {
			warns = append(warns, fmt.Sprintf("已跳过过大的条目：%s", name))
			continue
		}

		var (
			body   string
			extraW []string
		)
		switch ext {
		case ".docx":
			body, extraW, err = DocxToMarkdown(buf, images)
			if err != nil {
				warns = append(warns, fmt.Sprintf("无法解析 %s：%v", name, err))
				continue
			}
		case ".pdf":
			pdfRes, err := ingestPDF(buf, path.Base(name))
			if err != nil {
				warns = append(warns, fmt.Sprintf("无法解析 %s：%v", name, err))
				continue
			}
			body, extraW = pdfRes.Docs[0].Text, pdfRes.Warnings
		default:
			body = string(buf)
		}
		warns = append(warns, extraW...)
		if strings.TrimSpace(body) == "" {
			continue
		}
		docs = append(docs, IngestedDoc{Name: path.Base(name), Text: body})
		files = append(files, path.Base(name))
	}

	if len(docs) == 0 {
		return nil, fmt.Errorf("压缩包里没有可导入的文档（支持 %s）", UploadExtHint())
	}
	return &IngestResult{Docs: docs, Files: files, Warnings: warns}, nil
}

// safeArchiveName 拒绝绝对路径、父目录穿越与 Windows 盘符。
func safeArchiveName(name string) bool {
	if name == "" {
		return false
	}
	norm := strings.ReplaceAll(name, `\`, "/")
	if strings.HasPrefix(norm, "/") {
		return false
	}
	if len(norm) >= 2 && norm[1] == ':' {
		return false
	}
	for _, seg := range strings.Split(norm, "/") {
		if seg == ".." {
			return false
		}
	}
	return true
}

// ---- docx 读取 ----

// docx 内几个固定成员的路径。
const (
	docxDocumentPart  = "word/document.xml"
	docxNumberingPart = "word/numbering.xml"
	docxStylesPart    = "word/styles.xml"
	docxMediaPrefix   = "word/media/"
)

// DocxToMarkdown 把 .docx 的正文转成 Markdown。
//
// 只取段落文本、表格（转成管道表）与图片引用；公式（OMML）转成 LaTeX；
// Word 的自动编号还原成普通文字前缀——卷子的题号多半是自动编号，
// 不还原就只剩纯文本，按题号切题会整条失效。
// 样式、批注、页眉页脚一概不取——录题只需要题目本身。
func DocxToMarkdown(data []byte, images ImageSaver) (string, []string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", nil, fmt.Errorf("Word 文档无法打开：%w", err)
	}

	var (
		docPart   *zip.File
		numbering []byte
		styles    []byte
		media     = map[string][]byte{}
	)
	for _, f := range zr.File {
		switch {
		case f.Name == docxDocumentPart:
			docPart = f
		case f.Name == docxNumberingPart:
			numbering = readDocxPart(f)
		case f.Name == docxStylesPart:
			styles = readDocxPart(f)
		case strings.HasPrefix(f.Name, docxMediaPrefix) && !strings.HasSuffix(f.Name, "/"):
			rc, err := f.Open()
			if err != nil {
				continue
			}
			b, _ := io.ReadAll(io.LimitReader(rc, MaxDocFileBytes))
			rc.Close()
			media[f.Name] = b
		}
	}
	if docPart == nil {
		return "", nil, fmt.Errorf("这不是一个 Word 文档（缺少 %s）", docxDocumentPart)
	}

	rc, err := docPart.Open()
	if err != nil {
		return "", nil, fmt.Errorf("Word 正文无法读取：%w", err)
	}
	defer rc.Close()

	var warns []string
	p := &docxParser{
		images:    images,
		media:     media,
		warns:     &warns,
		numbering: newNumberingState(parseNumbering(numbering, styles)),
	}
	if err := p.parse(rc); err != nil {
		return "", warns, fmt.Errorf("Word 正文解析失败：%w", err)
	}
	return p.out(), warns, nil
}

// readDocxPart 读一个结构化部件，带上限兜底。
func readDocxPart(f *zip.File) []byte {
	rc, err := f.Open()
	if err != nil {
		return nil
	}
	defer rc.Close()
	b, _ := io.ReadAll(io.LimitReader(rc, MaxDocFileBytes+1))
	if len(b) > MaxDocFileBytes {
		return nil
	}
	return b
}

// docxParser 逐 token 扫 document.xml。
//
// 刻意不解码成结构体再遍历：OOXML 的段落可以任意嵌套（表格里的单元格、
// 文本框里的段落），递归结构体需要处理大量容错；顺着 token 流走反而稳。
type docxParser struct {
	images    ImageSaver
	media     map[string][]byte
	warns     *[]string
	numbering *numberingState

	lines []string
	// para 攒当前段落：Word 会把一句话拆成很多个 run，逐 token 直接写会把
	// 一段话劈成几十行。这里的做法是段落结束时才落一行。
	para strings.Builder
	// inTable 表示正在表格里；此时段落文本进 cellBuf 而不是 lines。
	inTable bool
	rowBuf  []string
	cellBuf strings.Builder

	// ---- 当前段落的编号属性（w:pPr） ----
	// styleID 段落样式：编号常常挂在样式上，段落自己反而只挂个 pStyle。
	styleID string
	// numID/ilvl 是段落自己声明的；hasNumPr 区分"声明了 numId=0"
	// （显式取消编号）和"什么都没声明"（要回落到样式上的那个）。
	numID    string
	ilvl     int
	hasNumPr bool
	// numPr 只在 w:pPr 里才是段落编号，用深度计数把它与别处的同名元素分开。
	depthPPr   int
	depthNumPr int
}

func (p *docxParser) out() string { return strings.TrimSpace(strings.Join(p.lines, "\n")) }

// flushPara 段落结束：编号还原成文字前缀，表格里进单元格，否则单独成行。
func (p *docxParser) flushPara() {
	s := strings.TrimSpace(p.para.String())
	prefix := p.currentNumbering()
	p.para.Reset()
	p.resetParaNumbering()
	if s == "" {
		return
	}
	if prefix != "" {
		s = prefix + s
	}
	if p.inTable {
		p.cellBuf.WriteString(s)
		return
	}
	p.lines = append(p.lines, s)
}

// currentNumbering 算出本段该带的编号前缀。
//
// 优先级：段落自己声明的 numPr > 段落样式带来的 numPr。
// 两者都没有就是普通段落，不加任何前缀。
func (p *docxParser) currentNumbering() string {
	if p.numbering == nil {
		return ""
	}
	if p.hasNumPr {
		return p.numbering.prefix(p.numID, p.ilvl)
	}
	if ref, ok := p.numbering.part.styles[p.styleID]; ok {
		return p.numbering.prefix(ref.numID, ref.ilvl)
	}
	return ""
}

func (p *docxParser) resetParaNumbering() {
	p.styleID = ""
	p.numID = ""
	p.ilvl = 0
	p.hasNumPr = false
}

// flushRow 把攒够的单元格收成一行管道表。
func (p *docxParser) flushRow() {
	cells := make([]string, 0, len(p.rowBuf))
	for _, c := range p.rowBuf {
		cells = append(cells, strings.TrimSpace(c))
	}
	p.rowBuf = nil
	// 全空的行（Word 里常见）直接丢掉，否则会留下一堆 "|  |  |"。
	if len(cells) == 0 || strings.TrimSpace(strings.Join(cells, "")) == "" {
		return
	}
	p.lines = append(p.lines, "| "+strings.Join(cells, " | ")+" |")
	// 第一行当表头，补一条分隔行，Markdown 才认得出这是表格。
	p.lines = append(p.lines, "|"+strings.Repeat(" --- |", len(cells)))
}

func (p *docxParser) parse(r io.Reader) error {
	dec := xml.NewDecoder(r)
	// mathDepth>0 表示正在公式内部：w:t 仍是正文，m:t 才是公式内容。
	var (
		mathDepth int
		inMath    bool
		math      strings.Builder
	)

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "p":
				p.resetParaNumbering()
			case "tbl":
				p.inTable = true
			case "tr":
				p.rowBuf = nil
			case "tc":
				p.cellBuf.Reset()
			case "pPr":
				p.depthPPr++
			case "numPr":
				if p.depthPPr > 0 {
					p.depthNumPr++
					p.hasNumPr = true
				}
			case "ilvl":
				p.ilvl = atoiOrZero(attrValue(t, "val"))
			case "numId":
				// val="0" 是 Word 显式取消编号，如实记下来，
				// 它必须压过样式上那个编号。
				p.numID = attrValue(t, "val")
			case "pStyle":
				p.styleID = attrValue(t, "val")
			case "oMath", "oMathPara":
				if mathDepth == 0 {
					inMath = true
					math.Reset()
				}
				mathDepth++
			case "f", "rad", "d":
				if inMath {
					switch t.Name.Local {
					// 分数的 \frac 这里不带括号：分子分母各自带一对，
					// 直接得到 \frac{a}{b}，而不是多套一层的 \frac{{a}{b}}。
					case "f":
						math.WriteString(`\frac`)
					case "rad":
						math.WriteString(`\sqrt{`)
					case "d":
						math.WriteString("(")
					}
				}
			case "num", "den":
				if inMath {
					math.WriteString("{")
				}
			case "sSup", "sSub", "sSubSup":
				if inMath {
					math.WriteString("_{")
				}
			}

		case xml.CharData:
			if inMath {
				math.WriteString(escapeLatexText(string(t)))
			} else {
				p.para.WriteString(string(t))
			}

		case xml.EndElement:
			switch t.Name.Local {
			case "numPr":
				p.depthNumPr--
			case "pPr":
				p.depthPPr--
			case "oMath", "oMathPara":
				mathDepth--
				if mathDepth <= 0 {
					if s := strings.TrimSpace(math.String()); s != "" {
						p.para.WriteString("$" + s + "$")
					}
					mathDepth = 0
					inMath = false
					math.Reset()
				}
			case "num", "den":
				if inMath {
					math.WriteString("}")
				}
			case "rad":
				if inMath {
					math.WriteString("}")
				}
			case "sSup", "sSub", "sSubSup":
				if inMath {
					math.WriteString("}")
				}
			case "d":
				if inMath {
					math.WriteString(")")
				}
			case "p":
				p.flushPara()
			case "tc":
				p.rowBuf = append(p.rowBuf, p.cellBuf.String())
				p.cellBuf.Reset()
			case "tr":
				p.flushRow()
			case "tbl":
				p.inTable = false
			case "br", "cr":
				if !inMath {
					p.para.WriteString("\n")
				}
			}

		case xml.ProcInst, xml.Directive, xml.Comment:
			// 忽略。
		}
	}
	return nil
}

// attrValue 取属性值，不带命名空间前缀。
func attrValue(t xml.StartElement, name string) string {
	for _, a := range t.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

func atoiOrZero(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}

// escapeLatexText 处理公式里的特殊字符，避免 LaTeX 语法被正文破坏。
func escapeLatexText(s string) string {
	r := strings.NewReplacer(
		`\`, `\backslash{}`, "{", `\{`, "}", `\}`, "%", `\%`, "#", `\#`,
		"&", `\&`, "_", `\_`,
	)
	return r.Replace(s)
}
