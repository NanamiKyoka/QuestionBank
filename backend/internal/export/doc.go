// Package export 定义"可导出的文档"这一中间表示（IR）。
//
// 分层用意：组卷数据 → IR → 各格式渲染器（DOCX / LaTeX）。
// 好处是新增一种导出格式只需要写一个渲染器，不必再处理一遍组卷语义；
// 同时也让"排版规则"（题号、分值、选项列数、解析位置）集中在一处，
// 避免 Word 与 LaTeX 两条线各写一套导致导出结果不一致。
package export

// Document 一份可导出的文档。
type Document struct {
	Title string
	// Meta 附加信息，渲染器可选择性输出（如 LaTeX 的 \title）。
	Meta   map[string]string
	Blocks []Block
}

// Block 块级元素。
type Block interface{ isBlock() }

// Inline 行内元素。
type Inline interface{ isInline() }

// Align 段落对齐方式。
type Align string

const (
	AlignLeft    Align = "left"
	AlignCenter  Align = "center"
	AlignRight   Align = "right"
	AlignJustify Align = "justify"
)

// Heading 标题。Level 从 1 开始。
type Heading struct {
	Level int
	Runs  []Inline
	Align Align
}

// Paragraph 段落。
type Paragraph struct {
	Runs  []Inline
	Align Align
	// IndentLeft 左缩进，单位磅（pt）。
	IndentLeft float64
	// SpaceBefore / SpaceAfter 段前段后间距，单位磅。
	SpaceBefore float64
	SpaceAfter  float64
	// KeepWithNext 与下一段同页（题干与选项不应被分页拆开）。
	KeepWithNext bool
}

// PageBreak 分页符。
type PageBreak struct{}

// Table 简单表格。第一行视为表头。
type Table struct {
	Rows [][]Cell
	// Widths 各列相对宽度；为空表示等宽。
	Widths []int
	// Borderless 无边框表格。选项并排就是用无边框表格实现的：
	// 既保证列对齐，又不会在卷面上画出多余的框线。
	Borderless bool
}

// Cell 表格单元格。
type Cell struct {
	Blocks []Block
}

// ListItem 自动编号的条目。
//
// 编号由渲染器负责（Word 用 numbering.xml，LaTeX 用 enumerate），
// 这样插入删除题目时编号自动连续，不必在 IR 里预先算好。
type ListItem struct {
	// Level 层级，0 为第一层。
	Level int
	Runs  []Inline
	// Numbered 为 true 用数字编号，false 用项目符号。
	Numbered bool
}

// Text 文本行内元素。
type Text struct {
	Value     string
	Bold      bool
	Italic    bool
	Underline bool
	// VertAlign 上下标："" | "superscript" | "subscript"
	VertAlign string
	// Mono 等宽字体，用于代码或答案标记。
	Mono bool
}

// Math 行内数学公式（LaTeX 源码）。
type Math struct {
	LaTeX string
	// Display 为 true 时按独立公式排版。
	Display bool
}

// Image 行内图片。
type Image struct {
	Data []byte
	// Ext 扩展名，不带点：png / jpeg / gif。
	Ext string
	// WidthPt / HeightPt 目标尺寸，单位磅。为 0 时由渲染器按原始像素推断。
	WidthPt  float64
	HeightPt float64
}

// LineBreak 强制换行。
type LineBreak struct{}

func (*Heading) isBlock()   {}
func (*Paragraph) isBlock() {}
func (*PageBreak) isBlock() {}
func (*Table) isBlock()     {}
func (*ListItem) isBlock()  {}

func (*Text) isInline()      {}
func (*Math) isInline()      {}
func (*Image) isInline()     {}
func (*LineBreak) isInline() {}

// HasContent 判断文档是否为空（没有任何可渲染内容）。
func (d *Document) HasContent() bool {
	return d != nil && len(d.Blocks) > 0
}

// plainText 把行内元素拼成纯文本，用于日志与降级输出。
func plainText(items []Inline) string {
	var b []rune
	for _, it := range items {
		if t, ok := it.(*Text); ok {
			b = append(b, []rune(t.Value)...)
		}
	}
	return string(b)
}

// PlainText 返回段落文本，供简单场景使用。
func (p *Paragraph) PlainText() string { return plainText(p.Runs) }

// PlainText 返回标题文本。
func (h *Heading) PlainText() string { return plainText(h.Runs) }
