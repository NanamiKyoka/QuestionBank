package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/NanamiKyoka/questionmind/internal/aiagent"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
	"github.com/NanamiKyoka/questionmind/internal/schema"
)

// 试卷与元数据写工具的可见场景。
//
// 试卷相关的不给题库场景：用户在题库里点开助手，问的多半是"我的题"，
// 把建卷能力一并塞进去只会让模型在两个意图之间摇摆。
var paperWriteScenes = []aiagent.Scene{
	aiagent.SceneCompositionEditor,
}

// registerMetadataWriteTools 注册试卷与元数据的写工具。
//
// 知识点与标签的写操作需要学科管理权限，越权会被 service 层挡下；
// 这里不必重复判定，但描述要如实告诉模型它需要什么权限。
func registerMetadataWriteTools(r *aiagent.Registry, deps toolDeps) {
	r.MustRegister(&aiagent.Tool{
		Name: "update_paper",
		Description: "修改试卷的标题、说明、编号与计分开关，或把它移到某个文件夹。" +
			"**不修改试卷里的题目**——改卷面的内容请用 add_questions_to_paper / edit_paper。" +
			"用户说「把这份卷子改个名字」「归到期末那个文件夹」时使用。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"paper_id":          map[string]any{"type": "integer"},
				"title":             map[string]any{"type": "string", "description": "新标题，省略表示不改"},
				"description":       map[string]any{"type": "string", "description": "试卷说明，省略表示不改"},
				"folder_id":         map[string]any{"type": "integer", "description": "目标文件夹 id"},
				"clear_folder":      map[string]any{"type": "boolean", "description": "移出文件夹"},
				"numbering_enabled": map[string]any{"type": "boolean", "description": "是否自动编号"},
				"scoring_enabled":   map[string]any{"type": "boolean", "description": "是否显示分值"},
			},
			"required": []string{"paper_id"},
		},
		Executor:   aiagent.ExecutorServer,
		Mutating:   true,
		Capability: "paper.update",
		Scenes:     paperWriteScenes,
		Handler: func(ctx context.Context, call aiagent.ToolCall) (aiagent.ToolResult, error) {
			id := uint(call.ArgInt("paper_id"))
			if id == 0 {
				return aiagent.ErrorResult("缺少 paper_id"), nil
			}
			req := &schema.PaperUpdateRequest{}
			touched := make([]string, 0, 6)

			if v, ok := call.Arguments["title"]; ok {
				title, _ := v.(string)
				title = strings.TrimSpace(title)
				if title == "" {
					return aiagent.ErrorResult("title 不能为空"), nil
				}
				if len([]rune(title)) > 200 {
					return aiagent.ErrorResult("title 过长"), nil
				}
				req.Title = &title
				touched = append(touched, "标题")
			}
			if v, ok := call.Arguments["description"]; ok {
				text, _ := v.(string)
				text = strings.TrimSpace(text)
				if len([]rune(text)) > 512 {
					return aiagent.ErrorResult("description 过长"), nil
				}
				req.Description = &text
				touched = append(touched, "说明")
			}
			if v, ok := call.Arguments["folder_id"]; ok {
				n, valid := intArg(v, 1, 1<<20)
				if !valid {
					return aiagent.ErrorResult("folder_id 无效"), nil
				}
				folderID := uint(n)
				req.FolderID = &folderID
				touched = append(touched, "文件夹")
			}
			if call.ArgBool("clear_folder") {
				req.ClearFolder = true
				touched = append(touched, "文件夹")
			}
			if v, ok := call.Arguments["numbering_enabled"]; ok {
				enabled, _ := v.(bool)
				req.NumberingEnabled = &enabled
				touched = append(touched, "自动编号")
			}
			if v, ok := call.Arguments["scoring_enabled"]; ok {
				enabled, _ := v.(bool)
				req.ScoringEnabled = &enabled
				touched = append(touched, "计分")
			}

			if len(touched) == 0 {
				return aiagent.ErrorResult("没有给出任何要改的字段"), nil
			}

			paper, err := deps.papers.UpdatePaper(ctx, call.User, id, req)
			if err != nil {
				return aiagent.ToolResult{}, err
			}
			return marshalResult(map[string]any{
				"id":      paper.ID,
				"title":   paper.Title,
				"changed": touched,
			}), nil
		},
	})

	r.MustRegister(&aiagent.Tool{
		Name: "duplicate_paper",
		Description: "复制一份已保存的试卷，得到一份可独立编辑的新试卷（副本是草稿）。" +
			"用户说「照着这份再出一套」「复制一份改数字」时使用。" +
			"复制的只是**已保存**的内容；用户编辑器里未保存的改动不会带过来。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"paper_id": map[string]any{"type": "integer"},
				"title":    map[string]any{"type": "string", "description": "副本标题，省略则自动加「（副本）」"},
			},
			"required": []string{"paper_id"},
		},
		Executor:   aiagent.ExecutorServer,
		Mutating:   true,
		Capability: "paper.duplicate",
		Scenes:     paperWriteScenes,
		Handler: func(ctx context.Context, call aiagent.ToolCall) (aiagent.ToolResult, error) {
			id := uint(call.ArgInt("paper_id"))
			if id == 0 {
				return aiagent.ErrorResult("缺少 paper_id"), nil
			}
			clone, err := deps.papers.DuplicatePaper(ctx, call.User, id, strings.TrimSpace(call.ArgString("title")))
			if err != nil {
				return aiagent.ToolResult{}, err
			}
			return marshalResult(map[string]any{
				"id":     clone.ID,
				"title":  clone.Title,
				"status": string(clone.Status),
			}), nil
		},
	})

	r.MustRegister(&aiagent.Tool{
		Name: "delete_paper",
		Description: "把试卷移入回收站（软删除）。用户明确要求删除时使用；" +
			"只是想收起来请改 status=archived（用 update_paper）。可在回收站恢复。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"paper_id": map[string]any{"type": "integer"},
			},
			"required": []string{"paper_id"},
		},
		Executor:   aiagent.ExecutorServer,
		Mutating:   true,
		Capability: "paper.delete",
		Scenes:     paperWriteScenes,
		Handler: func(ctx context.Context, call aiagent.ToolCall) (aiagent.ToolResult, error) {
			id := uint(call.ArgInt("paper_id"))
			if id == 0 {
				return aiagent.ErrorResult("缺少 paper_id"), nil
			}
			if err := deps.papers.DeletePaper(ctx, call.User, id); err != nil {
				return aiagent.ToolResult{}, err
			}
			return marshalResult(map[string]any{
				"id":   id,
				"hint": "试卷已进入回收站，用户可以在回收站页面上恢复。",
			}), nil
		},
	})

	r.MustRegister(&aiagent.Tool{
		Name:        "restore_paper",
		Description: "从回收站恢复一份试卷。用户说「刚才那份卷子删错了」时使用。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"paper_id": map[string]any{"type": "integer"},
			},
			"required": []string{"paper_id"},
		},
		Executor:   aiagent.ExecutorServer,
		Mutating:   true,
		Capability: "paper.restore",
		Scenes:     paperWriteScenes,
		Handler: func(ctx context.Context, call aiagent.ToolCall) (aiagent.ToolResult, error) {
			id := uint(call.ArgInt("paper_id"))
			if id == 0 {
				return aiagent.ErrorResult("缺少 paper_id"), nil
			}
			if err := deps.papers.RestorePaper(ctx, call.User, id); err != nil {
				return aiagent.ToolResult{}, err
			}
			return marshalResult(map[string]any{"id": id}), nil
		},
	})

	r.MustRegister(&aiagent.Tool{
		Name: "manage_folder",
		Description: "管理试卷文件夹：op=create 建文件夹、op=rename 改名、op=move 移动、op=delete 删除。" +
			"用户说「建个期末文件夹」「把这份卷子归到里面」时使用。" +
			"删除文件夹不会删除里面的试卷，只会把它们移到未分类。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"op": map[string]any{
					"type": "string", "enum": []string{"create", "rename", "move", "delete"},
				},
				"folder_id":    map[string]any{"type": "integer", "description": "目标文件夹 id，rename/move/delete 必填"},
				"name":         map[string]any{"type": "string", "description": "文件夹名，create/rename 必填"},
				"parent_id":    map[string]any{"type": "integer", "description": "父文件夹 id，create 时可省略"},
				"clear_parent": map[string]any{"type": "boolean", "description": "move 时移到顶层"},
			},
			"required": []string{"op"},
		},
		Executor:   aiagent.ExecutorServer,
		Mutating:   true,
		Capability: "paper.folder.manage",
		Scenes:     paperWriteScenes,
		Handler: func(ctx context.Context, call aiagent.ToolCall) (aiagent.ToolResult, error) {
			op := strings.TrimSpace(call.ArgString("op"))
			switch op {
			case "create":
				name := strings.TrimSpace(call.ArgString("name"))
				if name == "" {
					return aiagent.ErrorResult("create 需要 name"), nil
				}
				req := &schema.FolderCreateRequest{Name: name}
				if call.ArgInt("parent_id") > 0 {
					parentID := uint(call.ArgInt("parent_id"))
					req.ParentID = &parentID
				}
				folder, err := deps.papers.CreateFolder(ctx, call.User, call.SubjectID, req)
				if err != nil {
					return aiagent.ToolResult{}, err
				}
				return marshalResult(map[string]any{"id": folder.ID, "name": folder.Name}), nil

			case "rename":
				id, name, err := folderTarget(call)
				if err != nil {
					return aiagent.ErrorResult("%s", err.Error()), nil
				}
				if name == "" {
					return aiagent.ErrorResult("rename 需要 name"), nil
				}
				folder, err := deps.papers.UpdateFolder(ctx, call.User, id, &schema.FolderUpdateRequest{Name: &name})
				if err != nil {
					return aiagent.ToolResult{}, err
				}
				return marshalResult(map[string]any{"id": folder.ID, "name": folder.Name}), nil

			case "move":
				id, _, err := folderTarget(call)
				if err != nil {
					return aiagent.ErrorResult("%s", err.Error()), nil
				}
				req := &schema.FolderUpdateRequest{}
				if call.ArgBool("clear_parent") {
					req.ClearParent = true
				} else if call.ArgInt("parent_id") > 0 {
					parentID := uint(call.ArgInt("parent_id"))
					req.ParentID = &parentID
				} else {
					return aiagent.ErrorResult("move 需要 parent_id 或 clear_parent=true"), nil
				}
				folder, err := deps.papers.UpdateFolder(ctx, call.User, id, req)
				if err != nil {
					return aiagent.ToolResult{}, err
				}
				return marshalResult(map[string]any{"id": folder.ID, "name": folder.Name}), nil

			case "delete":
				id, _, err := folderTarget(call)
				if err != nil {
					return aiagent.ErrorResult("%s", err.Error()), nil
				}
				if err := deps.papers.DeleteFolder(ctx, call.User, id); err != nil {
					return aiagent.ToolResult{}, err
				}
				return marshalResult(map[string]any{
					"id":   id,
					"hint": "文件夹已删除，里面的试卷已移到未分类，题目没有受影响。",
				}), nil

			default:
				return aiagent.ErrorResult("op 只支持 create/rename/move/delete，实际 %q", op), nil
			}
		},
	})

	r.MustRegister(&aiagent.Tool{
		Name: "create_knowledge_point",
		Description: "在当前学科下新建一个知识点。需要学科管理权限（学科负责人或管理员）。" +
			"**只在确实没有相近知识点时使用**——先用 list_knowledge_points 或 " +
			"semantic_search_knowledge_points 查一遍，重复的知识点会污染整棵知识树，事后清理成本极高。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":      map[string]any{"type": "string", "description": "知识点名称"},
				"parent_id": map[string]any{"type": "integer", "description": "父知识点 id，省略表示建在顶层"},
			},
			"required": []string{"name"},
		},
		Executor:   aiagent.ExecutorServer,
		Mutating:   true,
		Capability: "knowledge_point.create",
		Scenes:     writeScenes,
		Handler: func(ctx context.Context, call aiagent.ToolCall) (aiagent.ToolResult, error) {
			name := strings.TrimSpace(call.ArgString("name"))
			if name == "" {
				return aiagent.ErrorResult("name 不能为空"), nil
			}
			req := &schema.KnowledgePointCreateRequest{Name: name}
			if call.ArgInt("parent_id") > 0 {
				parentID := uint(call.ArgInt("parent_id"))
				req.ParentID = &parentID
			}
			kp, err := deps.knowledgePoints.Create(ctx, call.User, call.SubjectID, req)
			if err != nil {
				return aiagent.ToolResult{}, err
			}
			return marshalResult(map[string]any{
				"id":   kp.ID,
				"name": kp.Name,
				"path": kp.Path,
			}), nil
		},
	})

	r.MustRegister(&aiagent.Tool{
		Name: "update_knowledge_point",
		Description: "重命名或移动一个知识点。移动会把**整棵子树**一起搬走，" +
			"所以先用 dry_run=true 看清楚有多少个子节点会跟着动，确认后再执行。" +
			"用户说「把这个知识点改名」「把它挪到 XX 下面」时使用。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"knowledge_point_id": map[string]any{"type": "integer"},
				"name":               map[string]any{"type": "string", "description": "新名称，省略表示不改名"},
				"parent_id":          map[string]any{"type": "integer", "description": "新的父知识点 id"},
				"clear_parent":       map[string]any{"type": "boolean", "description": "移到顶层"},
				"dry_run":            map[string]any{"type": "boolean", "description": "true 只预演不写入，省略等同于 true"},
			},
			"required": []string{"knowledge_point_id"},
		},
		Executor:   aiagent.ExecutorServer,
		Mutating:   true,
		Capability: "knowledge_point.update",
		Scenes:     writeScenes,
		Handler: func(ctx context.Context, call aiagent.ToolCall) (aiagent.ToolResult, error) {
			id := uint(call.ArgInt("knowledge_point_id"))
			if id == 0 {
				return aiagent.ErrorResult("缺少 knowledge_point_id"), nil
			}
			tree, err := deps.knowledgePoints.Tree(ctx, call.User, call.SubjectID)
			if err != nil {
				return aiagent.ToolResult{}, err
			}
			node, descendants := findKnowledgePoint(tree, id)
			if node == nil {
				return aiagent.ErrorResult("找不到知识点 %d", id), nil
			}

			changes := make([]string, 0, 2)
			if v, ok := call.Arguments["name"]; ok {
				name, _ := v.(string)
				name = strings.TrimSpace(name)
				if name == "" {
					return aiagent.ErrorResult("name 不能为空"), nil
				}
				if name != node.Name {
					changes = append(changes, fmt.Sprintf("改名 %q → %q", node.Name, name))
				}
			}
			if call.ArgBool("clear_parent") || call.ArgInt("parent_id") > 0 {
				changes = append(changes, "移动位置")
			}
			if len(changes) == 0 {
				return aiagent.ErrorResult("没有给出任何要改的字段"), nil
			}

			// 预演必须报出子树规模：用户对"挪一下"的直觉里
			// 通常不包括"连带 30 个子知识点一起换了父节点"。
			if call.ArgBoolOr("dry_run", true) {
				return marshalResult(map[string]any{
					"dry_run":     true,
					"id":          node.ID,
					"name":        node.Name,
					"path":        node.Path,
					"changes":     changes,
					"descendants": descendants,
					"hint":        fmt.Sprintf("移动会连带 %d 个子知识点一起换父节点。%s", descendants, dryRunHint),
				}), nil
			}

			if v, ok := call.Arguments["name"]; ok {
				name := strings.TrimSpace(v.(string))
				req := &schema.KnowledgePointUpdateRequest{Name: &name}
				if _, err := deps.knowledgePoints.Update(ctx, call.User, id, req); err != nil {
					return aiagent.ToolResult{}, err
				}
			}
			if call.ArgBool("clear_parent") || call.ArgInt("parent_id") > 0 {
				// MoveRequest 用「ParentID 是否为 nil」表达提升为根，
				// 没有单独的 clear 开关：工具参数多一个，就多一个说不清的状态。
				req := &schema.KnowledgePointMoveRequest{}
				if !call.ArgBool("clear_parent") {
					parentID := uint(call.ArgInt("parent_id"))
					req.ParentID = &parentID
				}
				if _, err := deps.knowledgePoints.Move(ctx, call.User, id, req); err != nil {
					return aiagent.ToolResult{}, err
				}
			}
			updated, err := deps.knowledgePoints.Tree(ctx, call.User, call.SubjectID)
			if err != nil {
				return aiagent.ToolResult{}, err
			}
			node, _ = findKnowledgePoint(updated, id)
			return marshalResult(map[string]any{
				"id":   id,
				"name": node.Name,
				"path": node.Path,
			}), nil
		},
	})

	r.MustRegister(&aiagent.Tool{
		Name: "create_tag",
		Description: "在当前学科下新建一个标签。需要学科管理权限（学科负责人或管理员）。" +
			"打标签前先用 list_tags 查一遍，能复用就不要新建。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":        map[string]any{"type": "string", "description": "标签名"},
				"color":       map[string]any{"type": "string", "description": "颜色，如 #409EFF，省略用默认色"},
				"category_id": map[string]any{"type": "integer", "description": "所属分类 id，省略表示不归类"},
			},
			"required": []string{"name"},
		},
		Executor:   aiagent.ExecutorServer,
		Mutating:   true,
		Capability: "tag.create",
		Scenes:     writeScenes,
		Handler: func(ctx context.Context, call aiagent.ToolCall) (aiagent.ToolResult, error) {
			name := strings.TrimSpace(call.ArgString("name"))
			if name == "" {
				return aiagent.ErrorResult("name 不能为空"), nil
			}
			req := &schema.TagCreateRequest{Name: name, Color: strings.TrimSpace(call.ArgString("color"))}
			if call.ArgInt("category_id") > 0 {
				categoryID := uint(call.ArgInt("category_id"))
				req.CategoryID = &categoryID
			}
			tag, err := deps.tags.Create(ctx, call.User, call.SubjectID, req)
			if err != nil {
				return aiagent.ToolResult{}, err
			}
			return marshalResult(map[string]any{"id": tag.ID, "name": tag.Name}), nil
		},
	})
}

// folderTarget 读 manage_folder 的 folder_id 与 name。
func folderTarget(call aiagent.ToolCall) (uint, string, error) {
	id := uint(call.ArgInt("folder_id"))
	if id == 0 {
		return 0, "", apperr.New(apperr.CodeInvalidParam, "该操作需要 folder_id")
	}
	return id, strings.TrimSpace(call.ArgString("name")), nil
}

// findKnowledgePoint 在知识点树里定位一个节点，并统计它的子树规模。
func findKnowledgePoint(
	nodes []schema.KnowledgePointResponse,
	id uint,
) (*schema.KnowledgePointResponse, int) {
	for i := range nodes {
		node := &nodes[i]
		if node.ID == id {
			total := 0
			for j := range node.Children {
				total += 1 + countSubtree(&node.Children[j])
			}
			return node, total
		}
		if found, n := findKnowledgePoint(node.Children, id); found != nil {
			return found, n
		}
	}
	return nil, 0
}

func countSubtree(node *schema.KnowledgePointResponse) int {
	total := 0
	for i := range node.Children {
		total += 1 + countSubtree(&node.Children[i])
	}
	return total
}
