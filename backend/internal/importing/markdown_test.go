package importing

import (
	"strings"
	"testing"

	"github.com/NanamiKyoka/questionmind/internal/model"
)

// 把富文本 JSON 转回一个便于断言的紧凑形式：
// 段落之间用 \n 分隔，公式写成 $latex$，空位写成 ___。
func renderDoc(raw model.JSON) string {
	var doc richNode
	if err := raw.Into(&doc); err != nil {
		return "<decode-error>"
	}

	var sb strings.Builder
	var inline func(nodes []richNode)
	inline = func(nodes []richNode) {
		for i := range nodes {
			switch nodes[i].Type {
			case nodeText:
				sb.WriteString(nodes[i].Text)
			case nodeBlank:
				sb.WriteString("___")
			case nodeInlineMath:
				sb.WriteString("$" + attrStr(nodes[i].Attrs, "latex") + "$")
			case nodeBlockMath:
				sb.WriteString("$$" + attrStr(nodes[i].Attrs, "latex") + "$$")
			case nodeImage:
				sb.WriteString(attrStr(nodes[i].Attrs, "alt"))
			default:
				inline(nodes[i].Content)
			}
		}
	}

	for i := range doc.Content {
		blk := doc.Content[i]
		switch blk.Type {
		case nodeBlockMath:
			sb.WriteString("$$" + attrStr(blk.Attrs, "latex") + "$$")
		case nodeHeading:
			sb.WriteString(strings.Repeat("#", attrInt(blk.Attrs, "level")) + " ")
			inline(blk.Content)
		case nodeBulletList, nodeOrderedList:
			for j := range blk.Content {
				sb.WriteString("[item]")
				inline(blk.Content[j].Content)
				sb.WriteString(";")
			}
		case nodeTable:
			for j := range blk.Content {
				sb.WriteString("[row]")
				for k := range blk.Content[j].Content {
					inline(blk.Content[j].Content[k].Content)
					sb.WriteString("|")
				}
				sb.WriteString(";")
			}
		default:
			inline(blk.Content)
		}
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

func attrStr(attrs map[string]any, key string) string {
	if v, ok := attrs[key].(string); ok {
		return v
	}
	return ""
}

func attrInt(attrs map[string]any, key string) int {
	if v, ok := attrs[key].(float64); ok {
		return int(v)
	}
	if v, ok := attrs[key].(int); ok {
		return v
	}
	return 0
}

func TestMarkdownBasicBlocks(t *testing.T) {
	cases := []struct {
		name string
		md   string
		want string
	}{
		{"单个段落", "这是一道题。", "这是一道题。"},
		{"两个段落", "第一段。\n\n第二段。", "第一段。\n第二段。"},
		{"标题", "## 一、选择题", "## 一、选择题"},
		{"多级标题", "# 一级\n\n### 三级", "# 一级\n### 三级"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := renderDoc(MarkdownToRichDoc(tc.md)); got != tc.want {
				t.Fatalf("MarkdownToRichDoc(%q)\n得到 %q\n期望 %q", tc.md, got, tc.want)
			}
		})
	}
}

// 公式必须优先于强调符号解析，否则 $a_b$、$x*y$ 会被当 Markdown 处理坏掉。
func TestMarkdownMathTakesPrecedence(t *testing.T) {
	cases := []struct {
		name string
		md   string
		want string
	}{
		{"行内公式", "已知 $x^2$ 求值", "已知 $x^2$ 求值"},
		{"公式里的星号", "计算 $a*b*c$", "计算 $a*b*c$"},
		{"公式里的下划线", "求 $x_1 + x_2$", "求 $x_1 + x_2$"},
		{"块级公式", "$$\\frac{1}{2}$$", "$$\\frac{1}{2}$$"},
		{"公式里的花括号", "$\\frac{a}{b}$", "$\\frac{a}{b}$"},
		{"多个行内公式", "$a$ 与 $b$", "$a$ 与 $b$"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := renderDoc(MarkdownToRichDoc(tc.md)); got != tc.want {
				t.Fatalf("得到 %q，期望 %q", got, tc.want)
			}
		})
	}
}

func TestMarkdownInlineMarks(t *testing.T) {
	var doc richNode
	raw := MarkdownToRichDoc("这是**重点**和*斜体*以及`代码`")
	if err := raw.Into(&doc); err != nil {
		t.Fatalf("解码失败: %v", err)
	}

	marks := map[string]string{}
	var walk func(nodes []richNode)
	walk = func(nodes []richNode) {
		for i := range nodes {
			if nodes[i].Type == nodeText && len(nodes[i].Marks) > 0 {
				marks[nodes[i].Text] = nodes[i].Marks[0].Type
			}
			walk(nodes[i].Content)
		}
	}
	walk(doc.Content)

	for text, want := range map[string]string{"重点": "bold", "斜体": "italic", "代码": "code"} {
		if marks[text] != want {
			t.Fatalf("%q 的标记应为 %s，实际 %q（全部：%v）", text, want, marks[text], marks)
		}
	}
}

func TestMarkdownLists(t *testing.T) {
	if got := renderDoc(MarkdownToRichDoc("- 甲\n- 乙")); got != "[item]甲;[item]乙;" {
		t.Fatalf("无序列表解析错误: %q", got)
	}
	if got := renderDoc(MarkdownToRichDoc("1. 甲\n2. 乙")); got != "[item]甲;[item]乙;" {
		t.Fatalf("有序列表解析错误: %q", got)
	}
	// 中文顿号编号也要能识别。
	if got := renderDoc(MarkdownToRichDoc("1、甲\n2、乙")); got != "[item]甲;[item]乙;" {
		t.Fatalf("中文编号列表解析错误: %q", got)
	}
}

func TestMarkdownTable(t *testing.T) {
	md := "| 题号 | 1 | 2 |\n|:---:|:---:|:---:|\n| 答案 | B | C |"
	got := renderDoc(MarkdownToRichDoc(md))
	if !strings.Contains(got, "[row]题号|1|2|;") {
		t.Fatalf("表头解析错误: %q", got)
	}
	if !strings.Contains(got, "[row]答案|B|C|;") {
		t.Fatalf("表格数据解析错误: %q", got)
	}
}

// 三个以上下划线识别为填空位，单个下划线保持原样（数学里很常见）。
func TestMarkdownBlanks(t *testing.T) {
	cases := map[string]string{
		"填 ___ 处":    "填 ___ 处",
		"填 ______ 处": "填 ___ 处",
		"x_1 等于什么":   "x_1 等于什么",
		"___和___":    "___和___",
		"求____与____": "求___与___",
	}
	for md, want := range cases {
		if got := renderDoc(MarkdownToRichDoc(md)); got != want {
			t.Fatalf("MarkdownToRichDoc(%q) = %q，期望 %q", md, got, want)
		}
	}
}

func TestCountBlanksInMarkdown(t *testing.T) {
	cases := map[string]int{
		"求 ___ 与 ___":  2,
		"没有空位":         0,
		"x_1":          0,
		"_____":        1,
		"a___b___c___": 3,
	}
	for md, want := range cases {
		if got := CountBlanksInMarkdown(md); got != want {
			t.Fatalf("CountBlanksInMarkdown(%q) = %d，期望 %d", md, got, want)
		}
	}
}

// 空输入也要产出结构完整的文档，不能是 nil 或非法 JSON。
func TestMarkdownEmptyInput(t *testing.T) {
	raw := MarkdownToRichDoc("")
	if raw.IsEmpty() {
		t.Fatal("应产出合法文档而不是空值")
	}
	var doc richNode
	if err := raw.Into(&doc); err != nil {
		t.Fatalf("应是合法 JSON: %v", err)
	}
	if doc.Type != nodeDoc {
		t.Fatalf("根节点类型应为 doc，实际 %q", doc.Type)
	}
	if len(doc.Content) == 0 {
		t.Fatal("应至少有一个块")
	}
}

// 未支持的语法要退化为文本，而不是丢内容。
func TestMarkdownUnsupportedSyntaxKeepsText(t *testing.T) {
	got := renderDoc(MarkdownToRichDoc("> 引用内容"))
	if !strings.Contains(got, "引用内容") {
		t.Fatalf("引用内容不应丢失: %q", got)
	}
	got2 := renderDoc(MarkdownToRichDoc("![图](a.png)"))
	if !strings.Contains(got2, "图") {
		t.Fatalf("图片说明文字不应丢失: %q", got2)
	}
}

func TestMarkdownHeadingLevelsClamped(t *testing.T) {
	// 七个井号超出 Markdown 六级标题的上限，应落为普通文本段落，
	// 而不是被静默截断成六级标题。
	raw := MarkdownToRichDoc("####### 七级")
	var doc richNode
	if err := raw.Into(&doc); err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	if len(doc.Content) == 0 {
		t.Fatal("应有内容")
	}
	if doc.Content[0].Type == nodeHeading {
		t.Fatalf("超过六级的井号不应识别为标题: %+v", doc.Content[0])
	}
	if !strings.Contains(renderDoc(raw), "七级") {
		t.Fatal("文本内容不应丢失")
	}
}

// 小数不能被误判成有序列表项——这是 "." 分隔符最容易踩的坑。
func TestMarkdownDoesNotTreatDecimalsAsList(t *testing.T) {
	got := renderDoc(MarkdownToRichDoc("圆周率约等于 3.14"))
	if strings.Contains(got, "[item]") {
		t.Fatalf("小数不应被识别为列表: %q", got)
	}
}

func TestMarkdownConvertsImageToNode(t *testing.T) {
	var doc richNode
	if err := MarkdownToRichDoc("看图 ![函数图像](/media/f.png) 回答").Into(&doc); err != nil {
		t.Fatal(err)
	}
	found := false
	var walk func([]richNode)
	walk = func(nodes []richNode) {
		for _, n := range nodes {
			if n.Type == nodeImage {
				found = true
				if attrStr(n.Attrs, "src") != "/media/f.png" || attrStr(n.Attrs, "alt") != "函数图像" {
					t.Fatalf("图片节点属性错误: %+v", n.Attrs)
				}
			}
			walk(n.Content)
		}
	}
	walk(doc.Content)
	if !found {
		t.Fatal("Markdown 图片应转换为 image 节点")
	}
}
