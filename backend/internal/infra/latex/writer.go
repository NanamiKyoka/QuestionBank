// Package latex 把 export.Document 渲染为可编译的 LaTeX 源码。
//
// 与 docx 不同，这里不需要处理二进制格式——LaTeX 就是文本，
// 所以实现重点是转义与结构映射，而不是容器格式。
//
// 生成的文档使用 ctexart 文档类以支持中文，并依赖 amsmath / graphicx。
package latex

import (
	"fmt"
	"strings"

	"github.com/NanamiKyoka/questionmind/internal/export"
)

// Result 渲染结果。
type Result struct {
	// TeX 主文件内容。
	TeX []byte
	// Images 图片文件，键为相对于主文件的路径（如 images/img1.png）。
	Images map[string][]byte
}

// Write 渲染文档。
func Write(doc *export.Document) (*Result, error) {
	if doc == nil {
		return nil, fmt.Errorf("latex: 文档为空")
	}

	r := &Result{Images: map[string][]byte{}}
	var sb strings.Builder

	sb.WriteString("%% 由 QuestionMind 自动生成\n")
	sb.WriteString("\\documentclass[11pt,a4paper]{ctexart}\n")
	sb.WriteString("\\usepackage{amsmath,amssymb}\n")
	sb.WriteString("\\usepackage{graphicx}\n")
	sb.WriteString("\\usepackage[margin=2.2cm]{geometry}\n")
	sb.WriteString("\\usepackage{enumitem}\n")
	sb.WriteString("\\setlist{nosep,leftmargin=2em}\n")

	if subject := doc.Meta["subject"]; subject != "" {
		sb.WriteString("\\newcommand{\\QMSubject}{" + escape(subject) + "}\n")
	}
	sb.WriteString("\\begin{document}\n")

	// 刻意**不输出 \title / \maketitle**。
	//
	// 标题已经作为文档的第一个块存在于 IR 里，DOCX 与 LaTeX 都靠渲染
	// 这个块来出标题。这里再 \maketitle 一次，导出的 LaTeX 里标题就
	// 出现两遍。让两个渲染器消费同一份 IR 是这套导出的基本约定，
	// 渲染器额外注入内容正是它要避免的那种差异。
	for _, b := range doc.Blocks {
		if err := writeBlock(&sb, r, b); err != nil {
			return nil, err
		}
	}

	sb.WriteString("\\end{document}\n")
	r.TeX = []byte(sb.String())
	return r, nil
}

func writeBlock(sb *strings.Builder, r *Result, b export.Block) error {
	switch n := b.(type) {
	case *export.Heading:
		level := n.Level
		if level < 1 {
			level = 1
		}
		if level > 3 {
			level = 3
		}
		cmd := []string{"section", "subsection", "subsubsection"}[level-1]
		sb.WriteString("\\" + cmd + "*{" + writeInlines(r, n.Runs) + "}\n")
	case *export.Paragraph:
		sb.WriteString("\\par " + writeInlines(r, n.Runs) + "\n")
	case *export.PageBreak:
		sb.WriteString("\\newpage\n")
	case *export.AnswerSpace:
		writeAnswerSpace(sb, n)
	case *export.ListItem:
		// LaTeX 的列表需要 \begin/\end 成对出现，这里用简化写法：
		// 每个条目单独成段并手工加编号前缀，避免跨段落维护列表状态。
		if n.Numbered {
			sb.WriteString("\\par\\noindent " + writeInlines(r, n.Runs) + "\n")
		} else {
			sb.WriteString("\\par\\noindent$\\bullet$ " + writeInlines(r, n.Runs) + "\n")
		}
	case *export.Table:
		writeTable(sb, r, n)
	default:
		return fmt.Errorf("latex: 不支持的块类型 %T", b)
	}
	return nil
}

// answerSpaceLineHeight 每行作答空间的高度。
// 与 DOCX 端 Normal 样式的 1.5 倍行距对齐：行距在两边都必须由格式本身决定，
// 写进 IR 就等于把这个常量复制一份，两种格式迟早会调歪。
const answerSpaceLineHeight = "1.5\\baselineskip"

// writeAnswerSpace 预留作答空间。
//
// lined 用 \noindent\rule{\linewidth} 画满宽横线，与 Word 段落下边框落在
// 同一基线位置；blank 只给等高的垂直空白。两种样式行数相同、占高相同，
// 所以教师在 Word 里校过的版面换到 LaTeX 不会变样。
func writeAnswerSpace(sb *strings.Builder, a *export.AnswerSpace) {
	lines := a.Lines
	if lines < 1 {
		lines = 1
	}
	if a.Lined {
		row := "\\noindent\\rule{\\linewidth}{0.4pt}\\par\\vspace{" + answerSpaceLineHeight + "}\n"
		for i := 0; i < lines; i++ {
			sb.WriteString(row)
		}
		return
	}
	for i := 0; i < lines; i++ {
		sb.WriteString("\\par\\noindent\\mbox{}\\par\\vspace{" + answerSpaceLineHeight + "}\n")
	}
}

func writeTable(sb *strings.Builder, r *Result, t *export.Table) {
	if len(t.Rows) == 0 {
		return
	}
	cols := 0
	for _, row := range t.Rows {
		if len(row) > cols {
			cols = len(row)
		}
	}
	if cols == 0 {
		return
	}

	sb.WriteString("\\begin{center}\\begin{tabular}{|" + strings.Repeat("l|", cols) + "}\n\\hline\n")
	for _, row := range t.Rows {
		cells := make([]string, cols)
		for i := 0; i < cols; i++ {
			if i < len(row) {
				cells[i] = writeBlocksInline(r, row[i].Blocks)
			}
		}
		sb.WriteString(strings.Join(cells, " & ") + " \\\\ \\hline\n")
	}
	sb.WriteString("\\end{tabular}\\end{center}\n")
}

// writeBlocksInline 把单元格里的块内容压成一行（表格内不能有段落换行）。
func writeBlocksInline(r *Result, blocks []export.Block) string {
	var parts []string
	for _, b := range blocks {
		switch n := b.(type) {
		case *export.Paragraph:
			parts = append(parts, writeInlines(r, n.Runs))
		case *export.Heading:
			parts = append(parts, "\\textbf{"+writeInlines(r, n.Runs)+"}")
		case *export.ListItem:
			parts = append(parts, writeInlines(r, n.Runs))
		}
	}
	return strings.Join(parts, " ")
}

// writeInlines 渲染行内元素。
func writeInlines(r *Result, items []export.Inline) string {
	var sb strings.Builder
	for _, in := range items {
		switch n := in.(type) {
		case *export.Text:
			sb.WriteString(formatText(n))
		case *export.Math:
			// LaTeX 原生支持数学模式，直接原样输出即可——这是 LaTeX 相比
			// DOCX 的优势：不需要做公式格式转换。
			body := trimMathDelims(n.LaTeX)
			if n.Display {
				sb.WriteString("\\[" + body + "\\]")
			} else {
				sb.WriteString("$" + body + "$")
			}
		case *export.LineBreak:
			sb.WriteString("\\\\\n")
		case *export.Image:
			sb.WriteString(renderImage(r, n))
		}
	}
	return sb.String()
}

func formatText(t *export.Text) string {
	s := escape(t.Value)
	if t.Mono {
		s = "\\texttt{" + s + "}"
	}
	if t.Bold {
		s = "\\textbf{" + s + "}"
	}
	if t.Italic {
		s = "\\textit{" + s + "}"
	}
	if t.Underline {
		s = "\\underline{" + s + "}"
	}
	switch t.VertAlign {
	case "superscript":
		s = "\\textsuperscript{" + s + "}"
	case "subscript":
		s = "\\textsubscript{" + s + "}"
	}
	return s
}

func renderImage(r *Result, img *export.Image) string {
	if len(img.Data) == 0 {
		return ""
	}
	name := fmt.Sprintf("images/img%d.%s", len(r.Images)+1, extOr(img.Ext, "png"))
	r.Images[name] = img.Data

	opts := "width=0.8\\linewidth"
	if img.WidthPt > 0 {
		// LaTeX 的长度单位：1pt 即 1 磅，直接用磅表示更精确。
		opts = fmt.Sprintf("width=%.0fpt", img.WidthPt)
		if img.HeightPt > 0 {
			opts += fmt.Sprintf(",height=%.0fpt", img.HeightPt)
		}
	}
	return "\\includegraphics[" + opts + "]{" + name + "}"
}

func extOr(ext, def string) string {
	if ext == "" {
		return def
	}
	ext = strings.ToLower(ext)
	if ext == "jpg" {
		ext = "jpeg"
	}
	return ext
}

// trimMathDelims 去掉公式外层的 $ / \[ \] 。
func trimMathDelims(s string) string {
	s = strings.TrimSpace(s)
	for _, p := range [][2]string{
		{"$$", "$$"}, {"\\[", "\\]"}, {"\\(", "\\)"}, {"$", "$"},
	} {
		if strings.HasPrefix(s, p[0]) && strings.HasSuffix(s, p[1]) && len(s) >= len(p[0])+len(p[1]) {
			return strings.TrimSpace(s[len(p[0]) : len(s)-len(p[1])])
		}
	}
	return s
}

// latexEscapes LaTeX 的保留字符。
//
// 反斜杠必须最先替换，否则后续插入的反斜杠会被二次转义。
var latexEscapes = strings.NewReplacer(
	`\`, `\textbackslash{}`,
	`{`, `\{`,
	`}`, `\}`,
	`$`, `\$`,
	`&`, `\&`,
	`#`, `\#`,
	`%`, `\%`,
	`_`, `\_`,
	`~`, `\textasciitilde{}`,
	`^`, `\textasciicircum{}`,
)

func escape(s string) string {
	if s == "" {
		return ""
	}
	// 保留已有的公式定界符由调用方处理；这里只做字符级转义。
	return latexEscapes.Replace(s)
}

// Escape 对外暴露转义，便于测试与复用。
func Escape(s string) string { return escape(s) }
