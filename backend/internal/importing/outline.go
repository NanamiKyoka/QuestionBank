package importing

// 导入版面大纲。
//
// 为什么需要它：规则解析器目前只产出"题目"——一串 Draft。
// 但一份试卷不只有题目：大题标题（"一、选择题"）、题间的说明文字、
// 作答区都是版面的一部分，丢掉就再也补不回来。整卷导入要能把
// 一份上传的卷子还原成可编辑的组卷文档，就必须先把这些记下来。
//
// 大纲与草稿是**两套东西**，刻意不合并：
//   - 草稿回答"这里有哪些题"（导入一道题就够用，不需要版面）；
//   - 大纲回答"这份卷子长什么样"（只有整卷导入才需要）。
// 合成一个结构会让只想导题的人也被迫面对版面概念。

// OutlineKind 大纲节点的种类。
type OutlineKind string

const (
	// OutlineHeading 大题标题，如"一、选择题（每小题 2 分）"。
	OutlineHeading OutlineKind = "heading"
	// OutlineRichText 题间的说明文字、注意事项等。
	OutlineRichText OutlineKind = "rich_text"
	// OutlineQuestion 引用一道题，用 DraftIndex 指向草稿数组下标。
	OutlineQuestion OutlineKind = "question"
	// OutlinePageBreak 分页符。
	OutlinePageBreak OutlineKind = "page_break"
	// OutlineAnswerSpace 作答留白。
	OutlineAnswerSpace OutlineKind = "answer_space"
	// OutlineDegraded 没能还原的内容。
	//
	// 单独一种节点而不是"跳过"：用户需要知道**哪部分**没进来。
	// 静默丢弃整段说明文字，比明确告诉他"这段没保住"糟糕得多。
	OutlineDegraded OutlineKind = "degraded"
)

// Outline 大纲的一个节点。
type Outline struct {
	Kind OutlineKind `json:"kind"`

	// Text 标题与说明文字的正文。
	Text string `json:"text,omitempty"`
	// Level 标题层级，1 起。
	Level int `json:"level,omitempty"`

	// DraftIndex OutlineQuestion 专用：指向 Drafts 数组里的第几道题。
	DraftIndex int `json:"draft_index,omitempty"`

	// Lines / Style 作答区专用。
	Lines int    `json:"lines,omitempty"`
	Style string `json:"style,omitempty"`

	// Note OutlineDegraded 专用：说明丢了什么、为什么。
	Note string `json:"note,omitempty"`
}

// Heading 构造一个标题节点。
func Heading(level int, text string) Outline {
	return Outline{Kind: OutlineHeading, Level: level, Text: text}
}

// RichText 构造一个说明文字节点。
func RichText(text string) Outline {
	return Outline{Kind: OutlineRichText, Text: text}
}

// QuestionRef 构造一个题目引用节点。
func QuestionRef(draftIndex int) Outline {
	return Outline{Kind: OutlineQuestion, DraftIndex: draftIndex}
}

// Degraded 构造一个"没能还原"的占位节点。
func Degraded(note string) Outline {
	return Outline{Kind: OutlineDegraded, Note: note}
}

// OutlineForDrafts 在没有版面信息时，造一个只有题目的最小大纲。
//
// 用于模型路径：模型只吐题目，不吐版面。此时宁可给一份"只有题目"的大纲，
// 也不要编造出原文根本没有的标题。
func OutlineForDrafts(n int) []Outline {
	out := make([]Outline, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, QuestionRef(i))
	}
	return out
}
