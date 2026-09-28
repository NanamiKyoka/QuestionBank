// Package importing 实现智能录题流水线：解析 → 抽取 → 归一化 → 审核 → 入库。
//
// 本文件负责其中最容易出错的一环：把模型输出的 Markdown 文本转成
// 系统内部的富文本结构（Tiptap 兼容的 JSON）。
//
// 为什么不直接存 Markdown：题干里需要区分"这里是公式""这里是要填的空"，
// 这些在 Markdown 里没有对应表示。转成结构化节点后，导出 Word 时才能
// 把公式变成 OMML、把空位变成下划线；前端编辑器也才能按节点渲染。
package importing

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/NanamiKyoka/questionmind/internal/model"
)

// 富文本节点类型。与前端编辑器保持一致。
const (
	nodeDoc         = "doc"
	nodeParagraph   = "paragraph"
	nodeHeading     = "heading"
	nodeText        = "text"
	nodeInlineMath  = "inlineMath"
	nodeBlockMath   = "blockMath"
	nodeHardBreak   = "hardBreak"
	nodeBulletList  = "bulletList"
	nodeOrderedList = "orderedList"
	nodeListItem    = "listItem"
	nodeTable       = "table"
	nodeTableRow    = "tableRow"
	nodeTableCell   = "tableCell"
	nodeTableHead   = "tableHeader"
	nodeBlank       = "blank"
)

// blankPattern 匹配连续 3 个及以上的下划线，视为一个填空位。
//
// 之所以要求 3 个起：Markdown 里单个下划线常用于表示变量名（如 x_1），
// 阈值太低会把正常的数学记号误判成空位。
var blankPattern = regexp.MustCompile(`_{3,}`)

// headingPattern 匹配 "# 标题"，最多六级。
var headingPattern = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)

// bulletPattern 匹配无序列表项。
var bulletPattern = regexp.MustCompile(`^\s*[-*+]\s+(.*)$`)

// orderedPatterns 匹配有序列表项。
//
// 分成两条：西文分隔符（"." / ")"）后面**必须有空白**，否则 "1.5" 这样的
// 小数会被误判成列表项；中文分隔符（"、" / "）"）后面可以没有空白，
// 因为 "1、5" 不存在小数歧义。
var orderedPatterns = []*regexp.Regexp{
	regexp.MustCompile(`^\s*\d+[.)]\s+(.*)$`),
	regexp.MustCompile(`^\s*\d+[、）]\s*(.*)$`),
}

// matchOrdered 尝试把一行识别为有序列表项。
func matchOrdered(line string) (string, bool) {
	for _, re := range orderedPatterns {
		if m := re.FindStringSubmatch(line); m != nil {
			return m[1], true
		}
	}
	return "", false
}

func isOrderedListItem(line string) bool {
	_, ok := matchOrdered(line)
	return ok
}

// tableSepPattern 匹配 Markdown 表格的分隔行，如 |:---|---:|。
var tableSepPattern = regexp.MustCompile(`^\s*\|?[\s:|-]+\|[\s:|-]*$`)

// richNode 富文本节点，序列化为 JSON。
//
// Attrs 用 map 而不是具体结构：不同节点类型的属性差别很大，
// 而且前端可能扩展自定义属性，服务端不该拦。
type richNode struct {
	Type    string         `json:"type"`
	Text    string         `json:"text,omitempty"`
	Marks   []richMark     `json:"marks,omitempty"`
	Attrs   map[string]any `json:"attrs,omitempty"`
	Content []richNode     `json:"content,omitempty"`
}

type richMark struct {
	Type  string         `json:"type"`
	Attrs map[string]any `json:"attrs,omitempty"`
}

// MarkdownToRichDoc 把 Markdown 文本转换为富文本文档。
//
// 支持：段落、标题、行内公式 $...$、块级公式 $$...$$、加粗、斜体、行内代码、
// 无序/有序列表、管道表格、填空位（3 个以上下划线）。
// 不支持的语法（引用、代码块、图片）会退化为普通文本而不是丢弃内容——
// 对于录题场景，"看到原始文字"远比"格式正确"重要。
func MarkdownToRichDoc(md string) model.JSON {
	blocks := parseBlocks(md)
	doc := richNode{Type: nodeDoc, Content: blocks}
	raw, err := json.Marshal(doc)
	if err != nil {
		// 序列化失败属于程序设计问题；退化为一个纯文本段落保证不丢内容。
		fallback, _ := json.Marshal(richNode{
			Type: nodeDoc,
			Content: []richNode{{
				Type:    nodeParagraph,
				Content: []richNode{{Type: nodeText, Text: md}},
			}},
		})
		return model.JSON(fallback)
	}
	return model.JSON(raw)
}

// parseBlocks 按行解析块级结构。
func parseBlocks(md string) []richNode {
	// 统一换行符并去掉首尾空白。
	md = strings.ReplaceAll(md, "\r\n", "\n")
	md = strings.ReplaceAll(md, "\r", "\n")
	lines := strings.Split(md, "\n")

	var blocks []richNode
	var para []string

	flushPara := func() {
		if len(para) == 0 {
			return
		}
		text := strings.TrimSpace(strings.Join(para, "\n"))
		para = nil
		if text == "" {
			return
		}
		// 段落内部的换行按硬换行处理，保留原作者的断行意图。
		blocks = append(blocks, richNode{Type: nodeParagraph, Content: parseInline(text)})
	}

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		switch {
		case trimmed == "":
			flushPara()

		case strings.HasPrefix(trimmed, "$$"):
			flushPara()
			latex := strings.TrimSuffix(strings.TrimPrefix(trimmed, "$$"), "$$")
			// 跨行的块级公式。
			for !strings.HasSuffix(trimmed, "$$") || latex == "" {
				i++
				if i >= len(lines) {
					break
				}
				next := strings.TrimSpace(lines[i])
				if strings.HasSuffix(next, "$$") {
					latex += "\n" + strings.TrimSuffix(next, "$$")
					break
				}
				latex += "\n" + next
				trimmed = next
			}
			latex = strings.TrimSpace(latex)
			if latex != "" {
				blocks = append(blocks, richNode{
					Type:  nodeBlockMath,
					Attrs: map[string]any{"latex": latex},
				})
			}

		case headingPattern.MatchString(trimmed):
			flushPara()
			m := headingPattern.FindStringSubmatch(trimmed)
			blocks = append(blocks, richNode{
				Type:    nodeHeading,
				Attrs:   map[string]any{"level": len(m[1])},
				Content: parseInline(m[2]),
			})

		case isTableStart(lines, i):
			flushPara()
			var rows []richNode
			for i < len(lines) {
				cur := strings.TrimSpace(lines[i])
				if !strings.Contains(cur, "|") {
					break
				}
				if tableSepPattern.MatchString(cur) && len(rows) == 1 {
					// 分隔行：跳过，但它标志着第一行是表头。
					i++
					continue
				}
				cells := splitTableRow(cur)
				row := richNode{Type: nodeTableRow}
				header := len(rows) == 0
				for _, c := range cells {
					cellType := nodeTableCell
					if header {
						cellType = nodeTableHead
					}
					row.Content = append(row.Content, richNode{
						Type:    cellType,
						Content: []richNode{{Type: nodeParagraph, Content: parseInline(c)}},
					})
				}
				rows = append(rows, row)
				i++
			}
			i-- // 外层循环还会自增
			if len(rows) > 0 {
				blocks = append(blocks, richNode{Type: nodeTable, Content: rows})
			}

		case bulletPattern.MatchString(trimmed):
			flushPara()
			var items []richNode
			for i < len(lines) && bulletPattern.MatchString(strings.TrimSpace(lines[i])) {
				m := bulletPattern.FindStringSubmatch(strings.TrimSpace(lines[i]))
				items = append(items, listItem(m[1]))
				i++
			}
			i--
			blocks = append(blocks, richNode{Type: nodeBulletList, Content: items})

		case isOrderedListItem(trimmed):
			flushPara()
			var items []richNode
			for i < len(lines) {
				text, ok := matchOrdered(strings.TrimSpace(lines[i]))
				if !ok {
					break
				}
				items = append(items, listItem(text))
				i++
			}
			i--
			blocks = append(blocks, richNode{Type: nodeOrderedList, Content: items})

		default:
			para = append(para, trimmed)
		}
	}
	flushPara()

	if len(blocks) == 0 {
		blocks = []richNode{{Type: nodeParagraph}}
	}
	return blocks
}

func listItem(text string) richNode {
	return richNode{
		Type:    nodeListItem,
		Content: []richNode{{Type: nodeParagraph, Content: parseInline(text)}},
	}
}

// isTableStart 判断当前位置是否是一张表的开头：
// 当前行含 |，且下一行是分隔行。
func isTableStart(lines []string, i int) bool {
	if i+1 >= len(lines) {
		return false
	}
	if !strings.Contains(lines[i], "|") {
		return false
	}
	return tableSepPattern.MatchString(strings.TrimSpace(lines[i+1]))
}

// splitTableRow 切分表格行，去掉首尾的空列。
func splitTableRow(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	parts := strings.Split(line, "|")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, strings.TrimSpace(p))
	}
	return out
}

// parseInline 解析行内元素。
//
// 扫描顺序很关键：公式优先于强调符号。否则 $a_b$ 里的下划线、
// $x*y*z$ 里的星号会被当成 Markdown 语法处理，公式直接坏掉。
func parseInline(text string) []richNode {
	text = strings.ReplaceAll(text, "\n", " ")
	var out []richNode
	var buf strings.Builder

	flush := func() {
		if buf.Len() == 0 {
			return
		}
		s := buf.String()
		buf.Reset()
		out = append(out, splitBlanks(s)...)
	}

	runes := []rune(text)
	for i := 0; i < len(runes); {
		switch {
		case runes[i] == '$':
			// 行内公式：匹配到下一个 $ 为止。
			if end := indexRune(runes, i+1, '$'); end > i {
				flush()
				latex := strings.TrimSpace(string(runes[i+1 : end]))
				if latex != "" {
					out = append(out, richNode{Type: nodeInlineMath, Attrs: map[string]any{"latex": latex}})
				}
				i = end + 1
				continue
			}
			buf.WriteRune(runes[i])
			i++

		case strings.HasPrefix(string(runes[i:]), "**"):
			// 加粗：**...**
			if end := indexSubstring(runes, i+2, "**"); end > i {
				inner := string(runes[i+2 : end])
				flush()
				out = append(out, marked(inner, "bold")...)
				i = end + 2
				continue
			}
			buf.WriteRune(runes[i])
			i++

		case runes[i] == '*':
			// 斜体：*...*
			if end := indexRune(runes, i+1, '*'); end > i {
				inner := string(runes[i+1 : end])
				flush()
				out = append(out, marked(inner, "italic")...)
				i = end + 1
				continue
			}
			buf.WriteRune(runes[i])
			i++

		case runes[i] == '`':
			if end := indexRune(runes, i+1, '`'); end > i {
				inner := string(runes[i+1 : end])
				flush()
				out = append(out, marked(inner, "code")...)
				i = end + 1
				continue
			}
			buf.WriteRune(runes[i])
			i++

		default:
			buf.WriteRune(runes[i])
			i++
		}
	}
	flush()

	if len(out) == 0 {
		out = []richNode{{Type: nodeText, Text: ""}}
	}
	return out
}

// marked 生成带标记的文本节点，并继续识别其中的填空位。
func marked(text, mark string) []richNode {
	nodes := splitBlanks(text)
	for i := range nodes {
		if nodes[i].Type == nodeText {
			nodes[i].Marks = append(nodes[i].Marks, richMark{Type: mark})
		}
	}
	return nodes
}

// splitBlanks 把文本里连续 3 个以上的下划线切成独立的空位节点。
func splitBlanks(text string) []richNode {
	if text == "" {
		return []richNode{{Type: nodeText, Text: ""}}
	}
	locs := blankPattern.FindAllStringIndex(text, -1)
	if len(locs) == 0 {
		return []richNode{{Type: nodeText, Text: text}}
	}

	var out []richNode
	prev := 0
	for _, loc := range locs {
		if loc[0] > prev {
			out = append(out, richNode{Type: nodeText, Text: text[prev:loc[0]]})
		}
		out = append(out, richNode{Type: nodeBlank})
		prev = loc[1]
	}
	if prev < len(text) {
		out = append(out, richNode{Type: nodeText, Text: text[prev:]})
	}
	return out
}

// CountBlanksInMarkdown 统计文本中的填空位数量，供归一化阶段校验答案个数。
func CountBlanksInMarkdown(md string) int {
	return len(blankPattern.FindAllString(md, -1))
}

func indexRune(runes []rune, from int, target rune) int {
	for i := from; i < len(runes); i++ {
		if runes[i] == target {
			return i
		}
	}
	return -1
}

func indexSubstring(runes []rune, from int, target string) int {
	t := []rune(target)
	for i := from; i+len(t) <= len(runes); i++ {
		match := true
		for j := range t {
			if runes[i+j] != t[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}
