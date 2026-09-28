package domain

import (
	"sort"

	"github.com/google/uuid"

	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
)

// newNodeInput 为服务端派生出的解析条目生成节点。
// 用 UUID 而不是自增序号：解析条目也是文档的一部分，
// 与客户端生成的节点共用一套 id 空间，前端才能统一做脏检查与差异对比。
func newNodeInput() NodeInput {
	return NodeInput{ID: uuid.NewString(), SchemaVersion: 1}
}

// 组卷文档的节点树规则。
//
// 树形结构刻意做得很浅，只有两层：
//
//	block    顶层块：说明文字 / 标题 / 题目 / 分页符 / 答题区
//	module   解析模块（question_details），其子节点由服务端派生
//	reference 解析条目（answer_item），必须挂在某个 module 下
//
// 之所以不让客户端自由构造任意深度的树：排版界面能表达的层级只有这么多，
// 放开之后就会出现"三层嵌套的题目块"这种无法渲染也无法导出的数据。

// NodeInput 客户端提交的节点。
type NodeInput struct {
	ID       string  `json:"id"`
	ParentID *string `json:"parent_id"`

	NodeKind model.NodeKind `json:"node_kind"`
	NodeType model.NodeType `json:"node_type"`

	Content model.JSON `json:"content"`
	Props   model.JSON `json:"props"`

	SchemaVersion int `json:"schema_version"`

	SourceQuestionNodeID *string `json:"source_question_node_id"`
	AnchorBeforeNodeID   *string `json:"anchor_before_node_id"`
}

// QuestionRef 题目块的内容结构。
type QuestionRef struct {
	QuestionID uint `json:"question_id"`
	// ContentRevision 客户端记录的、引用该题目时题目的内容修订号。
	// 与当前值不一致即表示题目已被改动，前端提示"可同步最新内容"。
	ContentRevision int64 `json:"content_revision"`
}

// ModuleContent 解析模块的内容：决定收哪些题的解析。
type ModuleContent struct {
	// Scope: all = 整卷汇总一份解析；before = 每题后跟自己的解析。
	Scope string `json:"scope"`
}

// 解析模块的收题范围常量。
const (
	ModuleScopeAll    = "all"
	ModuleScopeBefore = "before"
)

// answerItemProps 解析条目携带的锚点信息。
type answerItemProps struct {
	// QuestionNodeID 该解析条目对应的题目块节点。
	QuestionNodeID string `json:"question_node_id"`
	// Number 题号，导出时用于"1. 解析"这样的编号。
	Number int `json:"number"`
}

// NormalizeTree 校验并规范化整棵节点树。
//
// 返回的节点已按**前序**排好序，position 为同级内的序号，可直接落库。
// 服务端负责分配 position，客户端不传——否则会出现重复或空洞，
// 导致排序不稳定、每次保存顺序抖动。
func NormalizeTree(inputs []NodeInput) ([]model.PaperNode, error) {
	if len(inputs) == 0 {
		return []model.PaperNode{}, nil
	}

	// 先复制一份再做规范化：validateNode 会补齐 schema_version 等默认值，
	// 直接改调用方的切片是有副作用的，而在循环里用局部副本又会让默认值丢失。
	nodes := make([]NodeInput, len(inputs))
	copy(nodes, inputs)

	byID := make(map[string]NodeInput, len(nodes))
	order := make(map[string]int, len(nodes))
	for i := range nodes {
		n := nodes[i]
		if n.ID == "" {
			return nil, apperr.New(apperr.CodeInvalidParam, "节点 id 不能为空")
		}
		if _, dup := byID[n.ID]; dup {
			return nil, apperr.New(apperr.CodeInvalidParam, "节点 id 重复").
				WithDetail("id=%s", n.ID)
		}
		if err := validateNode(&nodes[i]); err != nil {
			return nil, err
		}
		byID[nodes[i].ID] = nodes[i]
		order[nodes[i].ID] = i
	}

	children, roots, err := groupByParent(nodes, byID, order)
	if err != nil {
		return nil, err
	}

	// 解析模块的子节点由服务端派生，覆盖客户端传来的内容。
	if err := deriveAnswerItems(roots, children, byID); err != nil {
		return nil, err
	}

	return flatten(roots, children), nil
}

func validateNode(n *NodeInput) error {
	kind, ok := model.KindOf(n.NodeType)
	if !ok {
		return apperr.New(apperr.CodeInvalidParam, "未知的节点类型").
			WithDetail("node_type=%s", n.NodeType)
	}
	if n.NodeKind != kind {
		return apperr.New(apperr.CodeInvalidParam, "节点类别与类型不匹配").
			WithDetail("node_type=%s 应为 %s，实际 %s", n.NodeType, kind, n.NodeKind)
	}
	if n.SchemaVersion <= 0 {
		n.SchemaVersion = 1
	}
	return nil
}

// groupByParent 按父子关系分组，并检测环。
func groupByParent(inputs []NodeInput, byID map[string]NodeInput, order map[string]int) (map[string][]NodeInput, []NodeInput, error) {
	children := make(map[string][]NodeInput)
	var roots []NodeInput

	for i := range inputs {
		n := inputs[i]
		if n.ParentID == nil {
			// 顶层可以放块（说明/标题/题目/分页）与解析模块；
			// 解析条目必须挂在模块下，不能悬空在顶层。
			if n.NodeKind == model.NodeKindReference {
				return nil, nil, apperr.New(apperr.CodeInvalidParam,
					"解析条目必须挂在解析模块下").WithDetail("id=%s", n.ID)
			}
			roots = append(roots, n)
			continue
		}

		parent, ok := byID[*n.ParentID]
		if !ok {
			// 父节点不在本次提交的集合里，说明客户端漏传或引用了别的稿件。
			return nil, nil, apperr.New(apperr.CodeInvalidParam, "父节点不存在").
				WithDetail("id=%s parent_id=%s", n.ID, *n.ParentID)
		}
		if parent.NodeKind != model.NodeKindModule {
			return nil, nil, apperr.New(apperr.CodeInvalidParam,
				"只有解析模块可以包含子节点").WithDetail("parent_type=%s", parent.NodeType)
		}
		if n.NodeKind != model.NodeKindReference {
			return nil, nil, apperr.New(apperr.CodeInvalidParam,
				"解析模块的子节点必须是解析条目").WithDetail("id=%s kind=%s", n.ID, n.NodeKind)
		}
		if err := detectCycle(n, byID); err != nil {
			return nil, nil, err
		}
		children[*n.ParentID] = append(children[*n.ParentID], n)
	}

	sortRoots(roots, order)
	return children, roots, nil
}

// detectCycle 沿父链上溯，若回到自身则说明成环。
func detectCycle(start NodeInput, byID map[string]NodeInput) error {
	seen := map[string]bool{start.ID: true}
	cur := start
	for cur.ParentID != nil {
		next, ok := byID[*cur.ParentID]
		if !ok {
			return nil
		}
		if seen[next.ID] {
			return apperr.New(apperr.CodeInvalidParam, "节点树存在环").
				WithDetail("id=%s", start.ID)
		}
		seen[next.ID] = true
		cur = next
	}
	return nil
}

// sortRoots 按客户端提交顺序排列顶层节点。
// 排序键是「在请求中的下标」，而不是 position——position 由服务端分配。
func sortRoots(roots []NodeInput, order map[string]int) {
	sort.SliceStable(roots, func(i, j int) bool {
		return order[roots[i].ID] < order[roots[j].ID]
	})
}

// deriveAnswerItems 为每个解析模块重新生成子节点。
//
// 复用客户端已经存在的解析条目（按其所对应的题目块配对），
// 这样用户在解析条目里填的内容不会在每次自动保存时被清空。
// 新增/删除题目时，条目随之增减。
func deriveAnswerItems(roots []NodeInput, children map[string][]NodeInput, byID map[string]NodeInput) error {
	// 文档中所有题目块，按出现顺序。
	var questions []NodeInput
	for _, r := range roots {
		if r.NodeType == model.NodeTypeQuestion {
			questions = append(questions, r)
		}
	}

	for _, r := range roots {
		if r.NodeType != model.NodeTypeQuestionDetails {
			continue
		}

		var mc ModuleContent
		if !r.Content.IsEmpty() {
			_ = r.Content.Into(&mc)
		}
		if mc.Scope == "" {
			mc.Scope = ModuleScopeAll
		}
		if mc.Scope != ModuleScopeAll && mc.Scope != ModuleScopeBefore {
			return apperr.New(apperr.CodeInvalidParam, "解析模块的 scope 取值非法").
				WithDetail("scope=%s", mc.Scope)
		}

		// 已有条目按题目节点分桶，供复用。
		buckets := make(map[string][]NodeInput)
		for _, c := range children[r.ID] {
			key := ""
			if c.SourceQuestionNodeID != nil {
				key = *c.SourceQuestionNodeID
			}
			buckets[key] = append(buckets[key], c)
		}

		rebuilt := make([]NodeInput, 0, len(questions))
		for idx, q := range questions {
			var item NodeInput
			if pool := buckets[q.ID]; len(pool) > 0 {
				item = pool[0]
				buckets[q.ID] = pool[1:]
			} else {
				item = newNodeInput()
			}

			parentID := r.ID
			item.ParentID = &parentID
			item.NodeKind = model.NodeKindReference
			item.NodeType = model.NodeTypeAnswerItem
			qid := q.ID
			item.SourceQuestionNodeID = &qid
			// anchor_before_node_id 表示"插在这道题之后"。
			// 整卷汇总（all）模式下不设锚点，导出时统一排在卷末。
			if mc.Scope == ModuleScopeBefore {
				item.AnchorBeforeNodeID = &qid
			} else {
				item.AnchorBeforeNodeID = nil
			}

			props, err := model.JSONOf(answerItemProps{QuestionNodeID: q.ID, Number: idx + 1})
			if err == nil {
				item.Props = props
			}
			rebuilt = append(rebuilt, item)
		}

		children[r.ID] = rebuilt
		byID[r.ID] = r
	}
	return nil
}

// flatten 前序遍历，分配 position。
//
// 顶层节点先按顺序输出，再输出其子节点——这样一条 INSERT 就能保住顺序，
// 读取时按 (parent_id, position) 排序即可还原。
func flatten(roots []NodeInput, children map[string][]NodeInput) []model.PaperNode {
	out := make([]model.PaperNode, 0, len(roots)+8)

	var walk func(nodes []NodeInput, parentID *string)
	walk = func(nodes []NodeInput, parentID *string) {
		for pos := range nodes {
			n := nodes[pos]
			out = append(out, model.PaperNode{
				ID:                   n.ID,
				ParentID:             parentID,
				Position:             pos,
				NodeKind:             n.NodeKind,
				NodeType:             n.NodeType,
				Content:              n.Content,
				Props:                n.Props,
				SchemaVersion:        n.SchemaVersion,
				SourceQuestionNodeID: n.SourceQuestionNodeID,
				AnchorBeforeNodeID:   n.AnchorBeforeNodeID,
			})
			walk(children[n.ID], &n.ID)
		}
	}
	walk(roots, nil)
	return out
}

// CollectQuestionIDs 取出树中所有题目块引用的题目 id，用于装载题目内容。
func CollectQuestionIDs(nodes []model.PaperNode) []uint {
	seen := make(map[uint]bool)
	var ids []uint
	for i := range nodes {
		if nodes[i].NodeType != model.NodeTypeQuestion {
			continue
		}
		var ref QuestionRef
		if nodes[i].Content.IsEmpty() {
			continue
		}
		if err := nodes[i].Content.Into(&ref); err != nil {
			continue
		}
		if ref.QuestionID == 0 || seen[ref.QuestionID] {
			continue
		}
		seen[ref.QuestionID] = true
		ids = append(ids, ref.QuestionID)
	}
	return ids
}

// ValidateQuestionRefs 校验每个题目块都引用了合法的题目 id。
func ValidateQuestionRefs(nodes []model.PaperNode) error {
	for i := range nodes {
		if nodes[i].NodeType != model.NodeTypeQuestion {
			continue
		}
		var ref QuestionRef
		if !nodes[i].Content.IsEmpty() {
			_ = nodes[i].Content.Into(&ref)
		}
		if ref.QuestionID == 0 {
			return apperr.New(apperr.CodeInvalidParam, "题目块缺少 question_id").
				WithDetail("node_id=%s", nodes[i].ID)
		}
	}
	return nil
}
