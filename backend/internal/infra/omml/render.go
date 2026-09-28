package omml

import (
	"bytes"
	"encoding/xml"
	"strings"
)

// NS 是 OMML 的命名空间。生成的片段嵌入 document.xml 时，
// 该前缀需在文档根节点上声明（由 docx 包负责）。
const NS = "http://schemas.openxmlformats.org/officeDocument/2006/math"

// Render 把 LaTeX 公式渲染为 OMML 片段。
//
// 返回的是完整的 <m:oMath> 元素，可直接插入 <w:p> 内。
// 解析失败不会发生——不识别的构造会退化为字面文本。
func Render(latex string) string {
	body := StripMathDelimiters(latex)
	if strings.TrimSpace(body) == "" {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("<m:oMath>")
	writeExpr(&sb, Parse(body))
	sb.WriteString("</m:oMath>")
	return sb.String()
}

// writeExpr 按节点类型写出 OMML。
func writeExpr(sb *strings.Builder, e Expr) {
	switch n := e.(type) {
	case nil:
		return
	case *Row:
		if n.IsEmpty() {
			return
		}
		for _, it := range n.Items {
			writeExpr(sb, it)
		}
	case *Run:
		writeRun(sb, n.Text, n.Upright)
	case *Space:
		if n.Width > 0 {
			writeRun(sb, strings.Repeat(" ", n.Width), true)
		}
	case *Frac:
		sb.WriteString("<m:f>")
		writeContainer(sb, "m:num", n.Num)
		writeContainer(sb, "m:den", n.Den)
		sb.WriteString("</m:f>")
	case *Sqrt:
		sb.WriteString("<m:rad><m:radPr>")
		if n.Deg == nil {
			// 平方根：隐藏次数占位。
			sb.WriteString(`<m:degHide m:val="1"/>`)
		}
		sb.WriteString("</m:radPr>")
		if n.Deg == nil {
			sb.WriteString("<m:deg/>")
		} else {
			writeContainer(sb, "m:deg", n.Deg)
		}
		writeContainer(sb, "m:e", n.Base)
		sb.WriteString("</m:rad>")
	case *Script:
		writeScript(sb, n)
	case *Delim:
		sb.WriteString("<m:d><m:dPr>")
		if n.Beg != "" {
			sb.WriteString(`<m:begChr m:val="` + xmlAttr(n.Beg) + `"/>`)
		}
		if n.End != "" {
			sb.WriteString(`<m:endChr m:val="` + xmlAttr(n.End) + `"/>`)
		}
		sb.WriteString("</m:dPr>")
		writeContainer(sb, "m:e", n.Body)
		sb.WriteString("</m:d>")
	case *Nary:
		sb.WriteString("<m:nary><m:naryPr>")
		sb.WriteString(`<m:chr m:val="` + xmlAttr(n.Op) + `"/>`)
		// 积分的上下限习惯排在右侧，求和排在上下方。
		if n.Op == "∫" || n.Op == "∬" || n.Op == "∭" || n.Op == "∮" {
			sb.WriteString(`<m:limLoc m:val="subSup"/>`)
		} else {
			sb.WriteString(`<m:limLoc m:val="undOvr"/>`)
		}
		sb.WriteString("</m:naryPr>")
		if n.Sub == nil {
			sb.WriteString("<m:sub/>")
		} else {
			writeContainer(sb, "m:sub", n.Sub)
		}
		if n.Sup == nil {
			sb.WriteString("<m:sup/>")
		} else {
			writeContainer(sb, "m:sup", n.Sup)
		}
		writeContainer(sb, "m:e", n.Body)
		sb.WriteString("</m:nary>")
	case *LimLow:
		sb.WriteString("<m:limLow>")
		sb.WriteString("<m:e>")
		writeRun(sb, n.Name, true)
		sb.WriteString("</m:e>")
		writeContainer(sb, "m:lim", n.Sub)
		sb.WriteString("</m:limLow>")
		if n.Body != nil {
			if row, ok := n.Body.(*Row); !ok || !row.IsEmpty() {
				sb.WriteString("<m:r><m:t> </m:t></m:r>")
				writeExpr(sb, n.Body)
			}
		}
	case *Func:
		hasArg := true
		if row, ok := n.Arg.(*Row); ok && row.IsEmpty() {
			hasArg = false
		}
		if n.Arg == nil {
			hasArg = false
		}
		if !hasArg {
			writeRun(sb, n.Name, true)
			return
		}
		sb.WriteString("<m:func><m:funcPr/>")
		sb.WriteString("<m:fName>")
		writeRun(sb, n.Name, true)
		sb.WriteString("</m:fName>")
		writeContainer(sb, "m:e", n.Arg)
		sb.WriteString("</m:func>")
	case *Accent:
		sb.WriteString("<m:acc><m:accPr>")
		sb.WriteString(`<m:chr m:val="` + xmlAttr(n.Char) + `"/>`)
		sb.WriteString("</m:accPr>")
		writeContainer(sb, "m:e", n.Base)
		sb.WriteString("</m:acc>")
	}
}

// writeScript 输出上标、下标或两者。
func writeScript(sb *strings.Builder, n *Script) {
	hasSub := n.Sub != nil
	hasSup := n.Sup != nil
	if !hasSub && !hasSup {
		writeExpr(sb, n.Base)
		return
	}

	// 下标 / 上标若本身是单字符，按正体排版更接近印刷习惯（如 x_1 的 1）。
	base := n.Base
	if base == nil {
		base = &Row{}
	}

	switch {
	case hasSub && hasSup:
		sb.WriteString("<m:sSubSup>")
		writeContainer(sb, "m:e", base)
		writeContainer(sb, "m:sub", n.Sub)
		writeContainer(sb, "m:sup", n.Sup)
		sb.WriteString("</m:sSubSup>")
	case hasSup:
		sb.WriteString("<m:sSup>")
		writeContainer(sb, "m:e", base)
		writeContainer(sb, "m:sup", n.Sup)
		sb.WriteString("</m:sSup>")
	default:
		sb.WriteString("<m:sSub>")
		writeContainer(sb, "m:e", base)
		writeContainer(sb, "m:sub", n.Sub)
		sb.WriteString("</m:sSub>")
	}
}

// writeContainer 输出一个必须有内容的容器元素。
//
// OMML 允许空容器，但某些 Word 版本遇到空的 m:e 会渲染出异常间距，
// 因此空内容统一补一个空运行占位。
func writeContainer(sb *strings.Builder, tag string, e Expr) {
	sb.WriteString("<" + tag + ">")
	if isEmptyExpr(e) {
		sb.WriteString("<m:r><m:t></m:t></m:r>")
	} else {
		writeExpr(sb, e)
	}
	sb.WriteString("</" + tag + ">")
}

func isEmptyExpr(e Expr) bool {
	switch n := e.(type) {
	case nil:
		return true
	case *Row:
		return n.IsEmpty()
	case *Run:
		return n.Text == ""
	case *Space:
		return true
	}
	return false
}

// writeRun 输出一个文本运行。
//
// Upright 用 <m:sty m:val="p"/> 表示正体；不设置样式时为数学斜体，
// 这正是变量应有的排版。
func writeRun(sb *strings.Builder, text string, upright bool) {
	if text == "" {
		return
	}
	sb.WriteString("<m:r>")
	if upright {
		sb.WriteString(`<m:rPr><m:sty m:val="p"/></m:rPr>`)
	}
	sb.WriteString(`<m:t xml:space="preserve">`)
	sb.WriteString(xmlText(text))
	sb.WriteString("</m:t>")
	sb.WriteString("</m:r>")
}

// xmlText 转义 XML 文本内容。
func xmlText(s string) string {
	var buf bytes.Buffer
	_ = xml.EscapeText(&buf, []byte(s))
	return buf.String()
}

// xmlAttr 转义 XML 属性值。
func xmlAttr(s string) string {
	return strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	).Replace(s)
}

// IsBalanced 粗略检查花括号是否配对，供导出前给出警告。
func IsBalanced(latex string) bool {
	depth := 0
	for _, c := range StripMathDelimiters(latex) {
		switch c {
		case '{':
			depth++
		case '}':
			depth--
			if depth < 0 {
				return false
			}
		}
	}
	return depth == 0
}
