package export

import (
	"strings"
	"testing"

	"github.com/NanamiKyoka/questionmind/internal/domain"
	"github.com/NanamiKyoka/questionmind/internal/model"
)

// ---- 测试辅助 ----

// jsonOf 编码 JSON 列。构造测试节点不值得为一次编码失败单开 error 通道，
// 编码失败说明用例本身写错了，直接 panic 比让断言报个假象更清楚。
func jsonOf(v any) model.JSON {
	j, err := model.JSONOf(v)
	if err != nil {
		panic(err)
	}
	return j
}

// stemDoc 造一段最小可渲染的富文本。
func stemDoc(text string) model.JSON {
	return jsonOf(map[string]any{
		"type": "doc",
		"content": []any{
			map[string]any{
				"type":    "paragraph",
				"content": []any{map[string]any{"type": "text", "text": text}},
			},
		},
	})
}

func questionNode(id string, questionID uint, props map[string]any) model.PaperNode {
	return model.PaperNode{
		ID:       id,
		NodeKind: model.NodeKindBlock,
		NodeType: model.NodeTypeQuestion,
		Content:  jsonOf(domain.QuestionRef{QuestionID: questionID}),
		Props:    jsonOf(props),
	}
}

func answerSpaceNode(props map[string]any, content any) model.PaperNode {
	n := model.PaperNode{
		ID:       "space",
		NodeKind: model.NodeKindBlock,
		NodeType: model.NodeTypeAnswerSpace,
	}
	if props != nil {
		n.Props = jsonOf(props)
	}
	if content != nil {
		n.Content = jsonOf(content)
	}
	return n
}

// assemblePaper 组装一份只含给定节点的试卷。
func assemblePaper(in Input) *Document {
	return Assemble(in, nil)
}

// docText 把 IR 的文本内容拼成一段纯文本，便于断言排版结果。
func docText(doc *Document) string {
	var sb strings.Builder
	var walk func(blocks []Block)
	walk = func(blocks []Block) {
		for _, b := range blocks {
			switch n := b.(type) {
			case *Paragraph:
				sb.WriteString(n.PlainText())
				sb.WriteString("\n")
			case *Heading:
				sb.WriteString(n.PlainText())
				sb.WriteString("\n")
			}
		}
	}
	walk(doc.Blocks)
	return sb.String()
}

func answerSpacesOf(doc *Document) []*AnswerSpace {
	var out []*AnswerSpace
	for _, b := range doc.Blocks {
		if a, ok := b.(*AnswerSpace); ok {
			out = append(out, a)
		}
	}
	return out
}

// ---- 分值 ----

// 分值写在题目节点的 props.score 上，Assemble 必须把它渲染成「（N 分）」，
// 并在题头汇总成「（满分 N 分）」。
//
// 这条曾经长期失效：服务端 sumScore 与导出器都读 props.score，
// 而编辑器从不写这个键，于是整卷总分永远是 0，两处分值都不出现。
func TestAssembleEmitsPerQuestionScoreAndTotal(t *testing.T) {
	doc := assemblePaper(Input{
		Title:            "单元测试卷",
		NumberingEnabled: true,
		ScoringEnabled:   true,
		Nodes: []model.PaperNode{
			questionNode("q1", 1, map[string]any{"score": 5}),
			questionNode("q2", 2, map[string]any{"score": 2.5}),
		},
		Questions: map[uint]QuestionView{
			1: {ID: 1, Type: model.QuestionTypeSingleChoice, Stem: stemDoc("第一题")},
			2: {ID: 2, Type: model.QuestionTypeSingleChoice, Stem: stemDoc("第二题")},
		},
		TotalScore: 7.5,
	})

	text := docText(doc)
	for _, want := range []string{"（满分 8 分）", "1．（5 分）第一题", "2．（2.5 分）第二题"} {
		if !strings.Contains(text, want) {
			t.Fatalf("应输出 %q，实际:\n%s", want, text)
		}
	}
}

// 没有 props.score 时题号照常出现，但两处分值都不该输出——
// 出现「（0 分）」比不出现更糟，它会被当成"这题 0 分"印在卷面上。
func TestAssembleOmitsScoreWhenNotSet(t *testing.T) {
	doc := assemblePaper(Input{
		Title:            "单元测试卷",
		NumberingEnabled: true,
		ScoringEnabled:   true,
		Nodes:            []model.PaperNode{questionNode("q1", 1, nil)},
		Questions: map[uint]QuestionView{
			1: {ID: 1, Type: model.QuestionTypeSingleChoice, Stem: stemDoc("第一题")},
		},
		TotalScore: 0,
	})

	text := docText(doc)
	if !strings.Contains(text, "1．第一题") {
		t.Fatalf("题号与题干应照常输出，实际:\n%s", text)
	}
	if strings.Contains(text, "分）") {
		t.Fatalf("未设分值时不应输出任何分值文本，实际:\n%s", text)
	}
}

// 关掉「启用分值统计」后，即使节点上有分值也不能印出来。
func TestAssembleScoreGatedByScoringEnabled(t *testing.T) {
	doc := assemblePaper(Input{
		Title:            "单元测试卷",
		NumberingEnabled: true,
		ScoringEnabled:   false,
		Nodes:            []model.PaperNode{questionNode("q1", 1, map[string]any{"score": 5})},
		Questions: map[uint]QuestionView{
			1: {ID: 1, Type: model.QuestionTypeSingleChoice, Stem: stemDoc("第一题")},
		},
		TotalScore: 5,
	})

	if text := docText(doc); strings.Contains(text, "分）") {
		t.Fatalf("未启用分值统计时不应输出分值，实际:\n%s", text)
	}
}

// ---- 答题区 ----

// 答题区的唯一契约是 props.lines + props.style。
func TestAnswerSpaceReadsLinesAndStyle(t *testing.T) {
	cases := []struct {
		name  string
		props map[string]any
		lines int
		lined bool
	}{
		{"横线", map[string]any{"lines": 6, "style": AnswerStyleLined}, 6, true},
		{"留白", map[string]any{"lines": 3, "style": AnswerStyleBlank}, 3, false},
		{"缺省样式按留白处理", map[string]any{"lines": 2}, 2, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc := assemblePaper(Input{Nodes: []model.PaperNode{answerSpaceNode(c.props, nil)}})
			spaces := answerSpacesOf(doc)
			if len(spaces) != 1 {
				t.Fatalf("应产出 1 个答题区块，实际 %d 个", len(spaces))
			}
			if spaces[0].Lines != c.lines || spaces[0].Lined != c.lined {
				t.Fatalf("期望 lines=%d lined=%v，实际 lines=%d lined=%v",
					c.lines, c.lined, spaces[0].Lines, spaces[0].Lined)
			}
		})
	}
}

// 老节点把留白高度写在 content.height 里，导出端却只认 props.lines，
// 于是用户的设置被静默丢弃、永远退化成 4 个空段落。
// 这里要保证旧数据仍按它自己的高度导出，而不是被默认值覆盖。
func TestAnswerSpaceFallsBackToLegacyContentHeight(t *testing.T) {
	cases := []struct {
		name   string
		props  map[string]any
		height any
		want   int
	}{
		{"旧数据 120px", nil, 120, 4},
		{"旧数据 240px", nil, 240, 8},
		{"旧数据 45px", nil, 45, 2},
		// props.lines 是新契约，优先级高于遗留字段。
		{"新字段优先", map[string]any{"lines": 7}, 120, 7},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc := assemblePaper(Input{Nodes: []model.PaperNode{answerSpaceNode(c.props, map[string]any{"height": c.height})}})
			spaces := answerSpacesOf(doc)
			if len(spaces) != 1 {
				t.Fatalf("应产出 1 个答题区块，实际 %d 个", len(spaces))
			}
			if spaces[0].Lines != c.want {
				t.Fatalf("期望 %d 行，实际 %d 行", c.want, spaces[0].Lines)
			}
		})
	}
}

// 完全没有设置时给默认值；离谱的行数要夹住，
// 否则一个被改坏的节点就能让一次导出写出几万个段落。
//
// 非正的行数一律按"未设置"处理（连同遗留字段一起回落），
// 而不是当成 0 行——答题区在导出结果里凭空消失，比行数差一点更难排查。
func TestAnswerSpaceDefaultsAndClamp(t *testing.T) {
	cases := []struct {
		name string
		node model.PaperNode
		want int
	}{
		{"无 props 无 content", answerSpaceNode(nil, nil), defaultAnswerLines},
		{"行数为 0", answerSpaceNode(map[string]any{"lines": 0}, nil), defaultAnswerLines},
		{"行数为负", answerSpaceNode(map[string]any{"lines": -3}, nil), defaultAnswerLines},
		{"行数超上限", answerSpaceNode(map[string]any{"lines": 9999}, nil), maxAnswerLines},
		{"遗留高度为 0", answerSpaceNode(nil, map[string]any{"height": 0}), defaultAnswerLines},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc := assemblePaper(Input{Nodes: []model.PaperNode{c.node}})
			spaces := answerSpacesOf(doc)
			if len(spaces) != 1 {
				t.Fatalf("应产出 1 个答题区块，实际 %d 个", len(spaces))
			}
			if spaces[0].Lines != c.want {
				t.Fatalf("期望 %d 行，实际 %d 行", c.want, spaces[0].Lines)
			}
		})
	}
}

// 答题区必须整块产出一个 AnswerSpace，不能再摊平成若干空段落：
// 摊平之后渲染器就分不清哪一段该画答题横线。
func TestAnswerSpaceEmitsSingleBlock(t *testing.T) {
	doc := assemblePaper(Input{
		Title: "测试卷",
		Nodes: []model.PaperNode{answerSpaceNode(map[string]any{"lines": 5, "style": AnswerStyleLined}, nil)},
	})

	if len(answerSpacesOf(doc)) != 1 {
		t.Fatal("应产出 1 个 AnswerSpace 块")
	}
	// 文档里只该剩下标题这一个段落；答题区的 5 行由渲染器按 lines 生成，
	// 不在 IR 里摊成 5 个空段落。
	if len(doc.Blocks) != 2 {
		t.Fatalf("IR 应只有标题块与答题区块，实际 %d 个块", len(doc.Blocks))
	}
	if _, ok := doc.Blocks[1].(*AnswerSpace); !ok {
		t.Fatalf("第二个块应为 AnswerSpace，实际 %T", doc.Blocks[1])
	}
}
