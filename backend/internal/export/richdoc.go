package export

import (
	"strings"

	"github.com/NanamiKyoka/questionmind/internal/model"
)

// ImageLoader 把富文本里的图片地址解析为字节内容。
//
// 用接口而不是直接读文件：题目内容里的 src 是 /media/... 这样的对外地址，
// 具体落在本地磁盘还是对象存储由 storage 层决定，导出不应该关心。
type ImageLoader interface {
	// Load 返回图片字节与扩展名（png/jpeg/...）。
	Load(src string) (data []byte, ext string, err error)
}

// blankPlaceholder 填空题空位在导出时的占位符。
// 用下划线而不是空白，因为纸质试卷上学生需要看到"这里有位置要填"。
const blankPlaceholder = "________"

// rdNode 编辑器富文本节点。
//
// 这里单独定义一份结构而不复用 domain.RichDoc：后者的 Marks 是 []any，
// 拿不到标记类型；导出必须要知道加粗/斜体/上下标才能正确排版。
type rdNode struct {
	Type    string         `json:"type"`
	Text    string         `json:"text"`
	Marks   []rdMark       `json:"marks"`
	Attrs   map[string]any `json:"attrs"`
	Content []rdNode       `json:"content"`
}

type rdMark struct {
	Type  string         `json:"type"`
	Attrs map[string]any `json:"attrs"`
}

// RichDocBlocks 把富文本内容转换为块级 IR。
//
// loader 可以为 nil：没有图片或图片加载失败时只跳过图片，不影响其余内容。
func RichDocBlocks(raw model.JSON, loader ImageLoader) []Block {
	if raw.IsEmpty() {
		return nil
	}
	var root rdNode
	if err := raw.Into(&root); err != nil {
		return nil
	}
	if root.Type == "doc" || len(root.Content) > 0 {
		return nodesToBlocks(root.Content, loader, 0)
	}
	return nil
}

// nodesToBlocks 转换一组节点；depth 用于防止异常的深层嵌套导致栈溢出。
func nodesToBlocks(nodes []rdNode, loader ImageLoader, depth int) []Block {
	if depth > 12 {
		return nil
	}

	var blocks []Block
	// 连续的列表项合并处理，保证同一列表的编号连续。
	var listBuffer []*ListItem

	flushList := func() {
		if len(listBuffer) > 0 {
			blocks = append(blocks, toBlocks(listBuffer)...)
			listBuffer = nil
		}
	}

	for i := range nodes {
		n := &nodes[i]
		switch n.Type {
		case "paragraph":
			flushList()
			blocks = append(blocks, &Paragraph{
				Runs:        inlinesOf(n.Content, loader, depth+1),
				Align:       alignOf(n.Attrs),
				IndentLeft:  attrFloat(n.Attrs, "indent"),
				SpaceBefore: 4,
				SpaceAfter:  4,
			})
		case "heading":
			flushList()
			level := attrInt(n.Attrs, "level")
			if level < 1 {
				level = 1
			}
			blocks = append(blocks, &Heading{Level: level, Runs: inlinesOf(n.Content, loader, depth+1)})
		case "blockMath":
			flushList()
			blocks = append(blocks, &Paragraph{
				Runs:  []Inline{&Math{LaTeX: attrString(n.Attrs, "latex"), Display: true}},
				Align: AlignCenter,
			})
		case "bulletList", "orderedList":
			flushList()
			numbered := n.Type == "orderedList"
			for j := range n.Content {
				if n.Content[j].Type != "listItem" {
					continue
				}
				blocks = append(blocks, &ListItem{
					Level:    0,
					Numbered: numbered,
					Runs:     inlinesOf(n.Content[j].Content, loader, depth+1),
				})
			}
		case "table":
			flushList()
			if tbl := tableOf(n, loader, depth); tbl != nil {
				blocks = append(blocks, tbl)
			}
		case "codeBlock":
			flushList()
			blocks = append(blocks, &Paragraph{
				Runs: []Inline{&Text{Value: plainOf(n.Content), Mono: true}},
			})
		case "horizontalRule":
			flushList()
			blocks = append(blocks, &Paragraph{Runs: []Inline{&Text{Value: strings.Repeat("—", 20)}}, Align: AlignCenter})
		case "image":
			flushList()
			if img := imageOf(n, loader); img != nil {
				blocks = append(blocks, &Paragraph{Runs: []Inline{img}, Align: AlignCenter})
			}
		default:
			// 未知块：尽力把它的文本子节点输出，而不是整段丢弃。
			if len(n.Content) > 0 {
				flushList()
				blocks = append(blocks, nodesToBlocks(n.Content, loader, depth+1)...)
			}
		}
	}
	flushList()
	return blocks
}

// toBlocks 把列表项转为块（当前实现里 ListItem 本身就是块）。
func toBlocks(items []*ListItem) []Block {
	out := make([]Block, 0, len(items))
	for _, it := range items {
		out = append(out, it)
	}
	return out
}

// inlinesOf 转换行内节点。
func inlinesOf(nodes []rdNode, loader ImageLoader, depth int) []Inline {
	out := make([]Inline, 0, len(nodes))
	for i := range nodes {
		n := &nodes[i]
		switch n.Type {
		case "text":
			out = append(out, textWithMarks(n))
		case "inlineMath":
			out = append(out, &Math{LaTeX: attrString(n.Attrs, "latex")})
		case "blockMath":
			out = append(out, &Math{LaTeX: attrString(n.Attrs, "latex"), Display: true})
		case "hardBreak":
			out = append(out, &LineBreak{})
		case "blank":
			out = append(out, &Text{Value: blankPlaceholder, Underline: false})
		case "image":
			if img := imageOf(n, loader); img != nil {
				out = append(out, img)
			}
		default:
			// 容器节点（如 listItem 内部的段落）：递归取其文本。
			if len(n.Content) > 0 {
				out = append(out, inlinesOf(n.Content, loader, depth+1)...)
			}
		}
	}
	return out
}

// textWithMarks 把文本节点及其标记转为 IR。
func textWithMarks(n *rdNode) *Text {
	t := &Text{Value: n.Text}
	for _, m := range n.Marks {
		switch m.Type {
		case "bold", "strong":
			t.Bold = true
		case "italic", "em":
			t.Italic = true
		case "underline":
			t.Underline = true
		case "code":
			t.Mono = true
		case "superscript":
			t.VertAlign = "superscript"
		case "subscript":
			t.VertAlign = "subscript"
		case "strike", "link", "highlight", "textStyle":
			// 导出到纸质/Word 时这些标记不影响判读，忽略即可。
		}
	}
	return t
}

// tableOf 转换表格。第一行若是表头则加粗。
func tableOf(n *rdNode, loader ImageLoader, depth int) *Table {
	t := &Table{}
	for i := range n.Content {
		row := &n.Content[i]
		if row.Type != "tableRow" {
			continue
		}
		cells := make([]Cell, 0, len(row.Content))
		for j := range row.Content {
			c := &row.Content[j]
			if c.Type != "tableCell" && c.Type != "tableHeader" {
				continue
			}
			blocks := nodesToBlocks(c.Content, loader, depth+1)
			if c.Type == "tableHeader" {
				boldBlocks(blocks)
			}
			cells = append(cells, Cell{Blocks: blocks})
		}
		t.Rows = append(t.Rows, cells)
	}
	if len(t.Rows) == 0 {
		return nil
	}
	return t
}

// boldBlocks 把块内所有文本加粗。
func boldBlocks(blocks []Block) {
	for _, b := range blocks {
		switch n := b.(type) {
		case *Paragraph:
			for _, in := range n.Runs {
				if t, ok := in.(*Text); ok {
					t.Bold = true
				}
			}
		case *Heading:
			for _, in := range n.Runs {
				if t, ok := in.(*Text); ok {
					t.Bold = true
				}
			}
		}
	}
}

// imageOf 构造图片元素。加载失败返回 nil（导出时静默跳过）。
func imageOf(n *rdNode, loader ImageLoader) *Image {
	src := attrString(n.Attrs, "src")
	if src == "" || loader == nil {
		return nil
	}
	data, ext, err := loader.Load(src)
	if err != nil || len(data) == 0 {
		return nil
	}
	return &Image{
		Data:     data,
		Ext:      ext,
		WidthPt:  attrFloat(n.Attrs, "width"),
		HeightPt: attrFloat(n.Attrs, "height"),
	}
}

// plainOf 提取纯文本，用于代码块等不需要保留格式的场景。
func plainOf(nodes []rdNode) string {
	var sb strings.Builder
	var walk func([]rdNode)
	walk = func(list []rdNode) {
		for i := range list {
			if list[i].Text != "" {
				sb.WriteString(list[i].Text)
			}
			if list[i].Type == "hardBreak" {
				sb.WriteString("\n")
			}
			walk(list[i].Content)
		}
	}
	walk(nodes)
	return sb.String()
}

func alignOf(attrs map[string]any) Align {
	switch attrString(attrs, "textAlign") {
	case "center":
		return AlignCenter
	case "right":
		return AlignRight
	case "justify":
		return AlignJustify
	}
	return ""
}

// ---- 属性取值辅助 ----

func attrString(attrs map[string]any, key string) string {
	if attrs == nil {
		return ""
	}
	if v, ok := attrs[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func attrFloat(attrs map[string]any, key string) float64 {
	if attrs == nil {
		return 0
	}
	v, ok := attrs[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case string:
		// 形如 "120px" 或 "50%"
		n = strings.TrimSpace(strings.TrimSuffix(n, "px"))
		var f float64
		var seen bool
		for _, c := range n {
			if c >= '0' && c <= '9' {
				f = f*10 + float64(c-'0')
				seen = true
			} else if c == '.' {
				continue
			} else {
				break
			}
		}
		if seen {
			return f
		}
	}
	return 0
}

func attrInt(attrs map[string]any, key string) int {
	return int(attrFloat(attrs, key))
}
