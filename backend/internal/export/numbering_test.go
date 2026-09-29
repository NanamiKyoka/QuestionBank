package export

import (
	"strings"
	"testing"

	"github.com/NanamiKyoka/questionmind/internal/model"
)

// 题号不再是"服务端按位置算出来的整数"：
// 按二级标题分组编号需要 "1.1" 这种十进制形式，整数表达不了，
// 而且每道大题重新从 1 开始编号时，整数计数器也接不住。
// 下面这组用例锁死三条规则：
//  1. 节点上填了题号就用它，且可以是 "1.1"；
//  2. 没填的老数据继续按位置自动编号，不许集体丢题号；
//  3. 答案页的题号必须与题干页一致——老师要拿答案页对着答题卡讲。

// numberingInput 造一份带 numbering_enabled 的导出输入。
func numberingInput(nodes []model.PaperNode, qids ...uint) Input {
	qs := make(map[uint]QuestionView, len(qids))
	for _, id := range qids {
		// 判断题必须带答案：解析条目在题目块没有手写解析时，
		// 会回落到题库答案，答案为空就渲染不出任何内容。
		yes := true
		qs[id] = QuestionView{
			ID:      id,
			Type:    model.QuestionTypeTrueFalse,
			Stem:    stemDoc("题干"),
			Answer:  model.AnswerSpec{Kind: "boolean", BoolAnswer: &yes},
		}
	}
	return Input{
		Title:            "卷",
		NumberingEnabled: true,
		Nodes:            nodes,
		Questions:        qs,
	}
}

func TestNumberingUsesNodePropWhenPresent(t *testing.T) {
	nodes := []model.PaperNode{
		questionNode("a", 1, map[string]any{"number": "1.1"}),
		questionNode("b", 2, map[string]any{"number": "1.2"}),
	}
	got := docText(assemblePaper(numberingInput(nodes, 1, 2)))
	if !strings.Contains(got, "1.1．") {
		t.Fatalf("应使用节点上的题号 1.1，实际输出:\n%s", got)
	}
	if !strings.Contains(got, "1.2．") {
		t.Fatalf("应使用节点上的题号 1.2，实际输出:\n%s", got)
	}
}

// 老试卷里没有 props.number，必须继续按位置自动编号。
// 这条一旦回归，用户的历史卷子会集体没有题号。
func TestNumberingFallsBackToPositionalCounter(t *testing.T) {
	nodes := []model.PaperNode{
		questionNode("a", 1, nil),
		questionNode("b", 2, nil),
		questionNode("c", 3, nil),
	}
	got := docText(assemblePaper(numberingInput(nodes, 1, 2, 3)))
	for _, want := range []string{"1．", "2．", "3．"} {
		if !strings.Contains(got, want) {
			t.Fatalf("未填题号的节点应按位置编号，缺少 %q，实际输出:\n%s", want, got)
		}
	}
}

// 部分填、部分没填时：填了的用填的，没填的继续按位置，
// 且互不干扰（不能让"填了一个"导致后面全部错位）。
func TestNumberingMixesExplicitAndPositional(t *testing.T) {
	nodes := []model.PaperNode{
		questionNode("a", 1, nil),                  // 位置 1
		questionNode("b", 2, map[string]any{"number": "2.1"}), // 显式
		questionNode("c", 3, nil),                  // 位置 3
	}
	got := docText(assemblePaper(numberingInput(nodes, 1, 2, 3)))
	for _, want := range []string{"1．", "2.1．", "3．"} {
		if !strings.Contains(got, want) {
			t.Fatalf("混合编号应各自正确，缺少 %q，实际输出:\n%s", want, got)
		}
	}
}

// 解析模块的题号必须回查它对应的题目块，而不是用条目上那个位置整数，
// 否则题干页写 2.1、答案页写 2，对不上。
func TestAnswerItemNumberFollowsItsSourceQuestion(t *testing.T) {
	qNode := questionNode("q1", 1, map[string]any{"number": "1.1"})
	qNode.ID = "q1"

	item := model.PaperNode{
		ID:                   "ans1",
		ParentID:             strPtr("mod"),
		NodeKind:             model.NodeKindReference,
		NodeType:             model.NodeTypeAnswerItem,
		SourceQuestionNodeID: strPtr("q1"),
		Props:                jsonOf(map[string]any{"number": 1}),
	}
	module := model.PaperNode{
		ID:       "mod",
		NodeKind: model.NodeKindModule,
		NodeType: model.NodeTypeQuestionDetails,
		Content:  jsonOf(map[string]any{"scope": "all"}),
	}

	in := numberingInput([]model.PaperNode{qNode, module, item}, 1)
	got := docText(assemblePaper(in))
	if !strings.Contains(got, "1.1．【答案】") {
		t.Fatalf("解析条目题号应跟随题目块的 1.1，实际输出:\n%s", got)
	}
}

// 关掉编号开关时，一个题号都不该出现。
func TestNumberingDisabledEmitsNoNumbers(t *testing.T) {
	nodes := []model.PaperNode{questionNode("a", 1, map[string]any{"number": "1.1"})}
	in := numberingInput(nodes, 1)
	in.NumberingEnabled = false
	got := docText(assemblePaper(in))
	if strings.Contains(got, "1.1．") {
		t.Fatalf("关闭编号后不应输出题号，实际输出:\n%s", got)
	}
}

func strPtr(s string) *string { return &s }
