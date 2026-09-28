package domain

import (
	"testing"

	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
)

// jsonVal 把值编码为 JSON 列。与 jsonOf 的区别是不需要 *testing.T，
// 因为这些辅助函数在构造测试用例（子测试之前）就会被调用。
func jsonVal(v any) model.JSON {
	j, err := model.JSONOf(v)
	if err != nil {
		panic(err)
	}
	return j
}

func questionNode(id string, questionID uint, revision int64) NodeInput {
	return NodeInput{
		ID:       id,
		NodeKind: model.NodeKindBlock,
		NodeType: model.NodeTypeQuestion,
		Content:  jsonVal(QuestionRef{QuestionID: questionID, ContentRevision: revision}),
	}
}

func moduleNode(id, scope string) NodeInput {
	return NodeInput{
		ID:       id,
		NodeKind: model.NodeKindModule,
		NodeType: model.NodeTypeQuestionDetails,
		Content:  jsonVal(ModuleContent{Scope: scope}),
	}
}

func blockNode(id string, t model.NodeType) NodeInput {
	kind, _ := model.KindOf(t)
	return NodeInput{ID: id, NodeKind: kind, NodeType: t}
}

func TestNormalizeTreeRejectsInvalidStructures(t *testing.T) {
	parent := "missing"

	cases := []struct {
		name  string
		nodes []NodeInput
	}{
		{"空 id", []NodeInput{{ID: "", NodeKind: model.NodeKindBlock, NodeType: model.NodeTypeRichText}}},
		{"重复 id", []NodeInput{
			blockNode("dup", model.NodeTypeRichText),
			blockNode("dup", model.NodeTypeHeading),
		}},
		{"未知节点类型", []NodeInput{{ID: "a", NodeKind: model.NodeKindBlock, NodeType: model.NodeType("nope")}}},
		{"kind 与 type 不匹配", []NodeInput{{ID: "a", NodeKind: model.NodeKindModule, NodeType: model.NodeTypeRichText}}},
		{"顶层放解析条目", []NodeInput{{ID: "a", NodeKind: model.NodeKindReference, NodeType: model.NodeTypeAnswerItem}}},
		{"父节点不存在", []NodeInput{{
			ID: "a", NodeKind: model.NodeKindReference, NodeType: model.NodeTypeAnswerItem, ParentID: &parent,
		}}},
		{"父节点不是模块", []NodeInput{
			blockNode("p", model.NodeTypeRichText),
			{ID: "a", NodeKind: model.NodeKindReference, NodeType: model.NodeTypeAnswerItem, ParentID: strPtr("p")},
		}},
		{"模块子节点必须是解析条目", []NodeInput{
			moduleNode("m", ModuleScopeAll),
			{ID: "a", NodeKind: model.NodeKindBlock, NodeType: model.NodeTypeRichText, ParentID: strPtr("m")},
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NormalizeTree(tc.nodes); err == nil {
				t.Fatal("期望报错但没有")
			}
		})
	}
}

func strPtr(s string) *string { return &s }

// 顶层可以同时有普通块和解析模块——这是排版文档最常见的形态。
func TestNormalizeTreeAllowsModuleAtRoot(t *testing.T) {
	nodes, err := NormalizeTree([]NodeInput{
		blockNode("h", model.NodeTypeHeading),
		questionNode("q1", 1, 1),
		moduleNode("m", ModuleScopeAll),
	})
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if len(nodes) != 4 {
		t.Fatalf("期望 4 个节点（含 1 个派生条目），实际 %d", len(nodes))
	}
}

// 解析条目的数量与顺序应当跟随题目块，且 position 由服务端分配。
func TestNormalizeTreeDerivesAnswerItems(t *testing.T) {
	nodes, err := NormalizeTree([]NodeInput{
		blockNode("h", model.NodeTypeHeading),
		questionNode("q1", 101, 1),
		questionNode("q2", 102, 1),
		moduleNode("m", ModuleScopeAll),
	})
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}

	// 顶层 4 个 + 派生 2 个
	if len(nodes) != 6 {
		t.Fatalf("期望 6 个节点，实际 %d", len(nodes))
	}

	var items []model.PaperNode
	for _, n := range nodes {
		if n.NodeType == model.NodeTypeAnswerItem {
			items = append(items, n)
		}
	}
	if len(items) != 2 {
		t.Fatalf("期望 2 个解析条目，实际 %d", len(items))
	}
	for i, it := range items {
		if it.NodeKind != model.NodeKindReference {
			t.Fatalf("条目 %d 的 kind 应为 reference", i)
		}
		if it.ParentID == nil || *it.ParentID != "m" {
			t.Fatalf("条目 %d 应挂在模块 m 下", i)
		}
		if it.Position != i {
			t.Fatalf("条目 %d 的 position 应为 %d，实际 %d", i, i, it.Position)
		}
	}
	if items[0].SourceQuestionNodeID == nil || *items[0].SourceQuestionNodeID != "q1" {
		t.Fatal("第一个条目应指向 q1")
	}
	if items[1].SourceQuestionNodeID == nil || *items[1].SourceQuestionNodeID != "q2" {
		t.Fatal("第二个条目应指向 q2")
	}
	// 整卷汇总模式不设锚点，导出时统一排在卷末。
	if items[0].AnchorBeforeNodeID != nil {
		t.Fatal("scope=all 时不应设置锚点")
	}
}

// scope=before 时每题后跟自己的解析，锚点指向对应题目块。
func TestNormalizeTreeBeforeScopeSetsAnchor(t *testing.T) {
	nodes, err := NormalizeTree([]NodeInput{
		questionNode("q1", 101, 1),
		questionNode("q2", 102, 1),
		moduleNode("m", ModuleScopeBefore),
	})
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	for _, n := range nodes {
		if n.NodeType != model.NodeTypeAnswerItem {
			continue
		}
		if n.AnchorBeforeNodeID == nil {
			t.Fatal("scope=before 时应设置锚点")
		}
		if n.SourceQuestionNodeID == nil || *n.SourceQuestionNodeID != *n.AnchorBeforeNodeID {
			t.Fatal("锚点应与来源题目一致")
		}
	}
}

// 客户端已存在的解析条目要被复用，否则用户在解析里写的内容
// 会在每次自动保存时被清空。
func TestNormalizeTreeReusesExistingAnswerItems(t *testing.T) {
	existing := NodeInput{
		ID:                   "keep-me",
		NodeKind:             model.NodeKindReference,
		NodeType:             model.NodeTypeAnswerItem,
		ParentID:             strPtr("m"),
		SourceQuestionNodeID: strPtr("q1"),
		Content:              jsonVal(doc(t, "用户手写的解析")),
	}

	nodes, err := NormalizeTree([]NodeInput{
		questionNode("q1", 101, 1),
		moduleNode("m", ModuleScopeAll),
		existing,
	})
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}

	found := false
	for _, n := range nodes {
		if n.ID != "keep-me" {
			continue
		}
		found = true
		if n.Content.IsEmpty() {
			t.Fatal("复用条目时不应丢失原有内容")
		}
	}
	if !found {
		t.Fatal("已存在的解析条目应被复用而不是重新生成")
	}
}

// position 在顶层按提交顺序分配，客户端传的 position 不参与。
func TestNormalizeTreeAssignsPositionsByOrder(t *testing.T) {
	nodes, err := NormalizeTree([]NodeInput{
		blockNode("c", model.NodeTypeRichText),
		blockNode("a", model.NodeTypeRichText),
		blockNode("b", model.NodeTypeRichText),
	})
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	want := map[string]int{"c": 0, "a": 1, "b": 2}
	for _, n := range nodes {
		if n.Position != want[n.ID] {
			t.Fatalf("节点 %s 的 position 应为 %d，实际 %d", n.ID, want[n.ID], n.Position)
		}
	}
}

// schema_version 必须原样保留：前端靠它决定空段落是否允许自动保存。
func TestNormalizeTreePreservesSchemaVersion(t *testing.T) {
	n := blockNode("a", model.NodeTypeRichText)
	n.SchemaVersion = 3
	nodes, err := NormalizeTree([]NodeInput{n})
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if nodes[0].SchemaVersion != 3 {
		t.Fatalf("schema_version 应为 3，实际 %d", nodes[0].SchemaVersion)
	}

	// 未提供时回落为 1，避免落库为 0 导致前端判定异常。
	n2 := blockNode("b", model.NodeTypeRichText)
	out, _ := NormalizeTree([]NodeInput{n2})
	if out[0].SchemaVersion != 1 {
		t.Fatalf("缺省 schema_version 应为 1，实际 %d", out[0].SchemaVersion)
	}
}

func TestCollectQuestionIDs(t *testing.T) {
	nodes, err := NormalizeTree([]NodeInput{
		questionNode("q1", 7, 1),
		questionNode("q2", 8, 1),
		questionNode("q3", 7, 1), // 重复引用同一道题
		blockNode("h", model.NodeTypeHeading),
	})
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	ids := CollectQuestionIDs(nodes)
	if len(ids) != 2 {
		t.Fatalf("期望去重后 2 个题目 id，实际 %v", ids)
	}
}

func TestValidateQuestionRefs(t *testing.T) {
	bad := []model.PaperNode{{
		ID: "q", NodeType: model.NodeTypeQuestion, NodeKind: model.NodeKindBlock,
		Content: jsonVal(QuestionRef{QuestionID: 0}),
	}}
	err := ValidateQuestionRefs(bad)
	if !apperr.Is(err, apperr.CodeInvalidParam) {
		t.Fatalf("期望 CodeInvalidParam，实际 %v", err)
	}

	good := []model.PaperNode{{
		ID: "q", NodeType: model.NodeTypeQuestion, NodeKind: model.NodeKindBlock,
		Content: jsonVal(QuestionRef{QuestionID: 42, ContentRevision: 1}),
	}}
	if err := ValidateQuestionRefs(good); err != nil {
		t.Fatalf("不应报错: %v", err)
	}
}
