package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/NanamiKyoka/questionmind/internal/aiagent"
	"github.com/NanamiKyoka/questionmind/internal/importing"
	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
	"github.com/NanamiKyoka/questionmind/internal/schema"
)

// toolDeps 工具实现所需的业务服务。
type toolDeps struct {
	questions       *QuestionService
	knowledgePoints *KnowledgePointService
	tags            *TagService
	papers          *PaperService
}

// buildToolRegistry 注册全部暴露给模型的工具。
//
// 服务端工具直接调用 service 层，因此**与 HTTP 接口共用同一套鉴权与校验**；
// 客户端工具只做服务端预处理，真正的执行由前端完成。
func buildToolRegistry(deps toolDeps) *aiagent.Registry {
	r := aiagent.NewRegistry()

	registerQuestionTools(r, deps)
	registerLibraryTools(r, deps)
	registerPaperTools(r, deps)

	return r
}

// ---- 题库检索 ----

func registerQuestionTools(r *aiagent.Registry, deps toolDeps) {
	r.MustRegister(&aiagent.Tool{
		Name: "search_questions",
		Description: "按关键词、题型、难度等条件检索题库中的题目。返回题目 id、题型、难度与题干摘要。" +
			"当用户想找题、想了解题库里有什么时使用。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"keyword":       map[string]any{"type": "string", "description": "题干关键词"},
				"question_type": map[string]any{"type": "string", "enum": []string{"single_choice", "multiple_choice", "fill_blank", "true_false", "essay"}},
				"difficulty":    map[string]any{"type": "integer", "minimum": 1, "maximum": 5},
				"limit":         map[string]any{"type": "integer", "minimum": 1, "maximum": 20, "description": "最多返回几道题，默认 8"},
			},
		},
		Executor: aiagent.ExecutorServer,
		Scenes:   []aiagent.Scene{aiagent.SceneQuestionLibrary, aiagent.SceneCompositionEditor, aiagent.SceneImportReview},
		Handler: func(ctx context.Context, call aiagent.ToolCall) (aiagent.ToolResult, error) {
			limit := call.ArgInt("limit")
			if limit <= 0 || limit > 20 {
				limit = 8
			}

			briefs, total, err := deps.questions.List(ctx, call.User, call.SubjectID, ListQuery{
				Keyword:    call.ArgString("keyword"),
				Type:       model.QuestionType(call.ArgString("question_type")),
				Difficulty: call.ArgInt("difficulty"),
				RootOnly:   true,
			}, limit, 0)
			if err != nil {
				return aiagent.ToolResult{}, err
			}

			if total == 0 {
				return aiagent.ToolResult{
					Content: "没有找到符合条件的题目。可以放宽条件再试，或告诉用户题库里没有相关题目。",
					Data:    map[string]any{"total": 0, "items": []any{}},
				}, nil
			}

			return questionListResult(briefs, total), nil
		},
	})

	r.MustRegister(&aiagent.Tool{
		Name:        "get_question",
		Description: "读取一道题的完整内容，包括题干、选项、答案与解析。需要向用户展示题目细节或核对答案时使用。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"question_id": map[string]any{"type": "integer", "description": "题目 id"},
			},
			"required": []string{"question_id"},
		},
		Executor: aiagent.ExecutorServer,
		Handler: func(ctx context.Context, call aiagent.ToolCall) (aiagent.ToolResult, error) {
			id := uint(call.ArgInt("question_id"))
			if id == 0 {
				return aiagent.ErrorResult("缺少 question_id"), nil
			}
			detail, err := deps.questions.Get(ctx, call.User, id)
			if err != nil {
				return aiagent.ToolResult{}, err
			}
			return questionDetailResult(detail), nil
		},
	})

	r.MustRegister(&aiagent.Tool{
		Name: "propose_questions",
		Description: "把选中的题目作为候选提交给用户挑选。检索到可能合适的题目后、想请用户确认时使用。" +
			"这只是提议，不会修改任何数据。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"question_ids": map[string]any{
					"type": "array", "items": map[string]any{"type": "integer"},
					"description": "候选题目 id 列表",
				},
			},
			"required": []string{"question_ids"},
		},
		Executor: aiagent.ExecutorServer,
		Handler: func(ctx context.Context, call aiagent.ToolCall) (aiagent.ToolResult, error) {
			ids := call.ArgUintSlice("question_ids")
			if len(ids) == 0 {
				return aiagent.ErrorResult("question_ids 不能为空"), nil
			}
			// 只提议，不写库。要真正加入试卷得用客户端工具，
			// 因为只有前端知道用户当前编辑的是哪份卷子。
			return aiagent.ToolResult{
				Content: fmt.Sprintf("已向用户展示 %d 道候选题目，等待用户确认。", len(ids)),
				UI:      []aiagent.UIDirective{aiagent.ProposalDirective("batch", ids)},
				Data:    map[string]any{"ids": ids},
			}, nil
		},
	})
}

// ---- 知识点与标签 ----

func registerLibraryTools(r *aiagent.Registry, deps toolDeps) {
	r.MustRegister(&aiagent.Tool{
		Name: "list_knowledge_points",
		Description: "列出当前学科的知识点。需要把题目关联到知识点、或用户问知识点体系时使用。" +
			"关键词匹配在服务端完成，模型不必自己筛选长列表。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"keyword": map[string]any{"type": "string", "description": "按名称筛选，可省略"},
				"limit":   map[string]any{"type": "integer", "minimum": 1, "maximum": 100},
			},
		},
		Executor: aiagent.ExecutorServer,
		Handler: func(ctx context.Context, call aiagent.ToolCall) (aiagent.ToolResult, error) {
			tree, err := deps.knowledgePoints.Tree(ctx, call.User, call.SubjectID)
			if err != nil {
				return aiagent.ToolResult{}, err
			}

			keyword := strings.TrimSpace(call.ArgString("keyword"))
			limit := call.ArgInt("limit")
			if limit <= 0 || limit > 100 {
				limit = 50
			}

			type item struct {
				ID   uint   `json:"id"`
				Name string `json:"name"`
				Path string `json:"path"`
			}
			var items []item
			var walk func(nodes []schema.KnowledgePointResponse)
			walk = func(nodes []schema.KnowledgePointResponse) {
				for i := range nodes {
					n := &nodes[i]
					if keyword == "" || strings.Contains(n.Name, keyword) {
						if len(items) < limit {
							items = append(items, item{ID: n.ID, Name: n.Name, Path: n.Path})
						}
					}
					walk(n.Children)
				}
			}
			walk(tree)

			if len(items) == 0 {
				return aiagent.ToolResult{
					Content: "当前学科下没有匹配的知识点。",
					Data:    map[string]any{"items": []any{}},
				}, nil
			}
			return marshalResult(map[string]any{
				"items": items,
				"hint":  "关联知识点时请使用这里的 id。",
			}), nil
		},
	})

	r.MustRegister(&aiagent.Tool{
		Name:        "list_tags",
		Description: "列出当前学科的标签。需要给题目打标签时使用。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 200},
			},
		},
		Executor: aiagent.ExecutorServer,
		Handler: func(ctx context.Context, call aiagent.ToolCall) (aiagent.ToolResult, error) {
			list, err := deps.tags.List(ctx, call.User, call.SubjectID, nil)
			if err != nil {
				return aiagent.ToolResult{}, err
			}
			limit := call.ArgInt("limit")
			if limit <= 0 || limit > 200 {
				limit = 100
			}

			type item struct {
				ID   uint   `json:"id"`
				Name string `json:"name"`
			}
			items := make([]item, 0, len(list))
			for i := range list {
				if len(items) >= limit {
					break
				}
				items = append(items, item{ID: list[i].ID, Name: list[i].Name})
			}
			if len(items) == 0 {
				return aiagent.ToolResult{
					Content: "当前学科还没有标签。",
					Data:    map[string]any{"items": []any{}},
				}, nil
			}
			return marshalResult(map[string]any{"items": items}), nil
		},
	})
}

// ---- 试卷 ----

func registerPaperTools(r *aiagent.Registry, deps toolDeps) {
	r.MustRegister(&aiagent.Tool{
		Name: "create_paper",
		Description: "新建一份试卷（稿件）。用户说「帮我建一份试卷」「组一套练习」时使用。" +
			"创建后会返回试卷 id。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"title":       map[string]any{"type": "string", "description": "试卷标题"},
				"description": map[string]any{"type": "string"},
			},
			"required": []string{"title"},
		},
		Executor:   aiagent.ExecutorServer,
		Mutating:   true,
		Capability: "composition.create",
		Handler: func(ctx context.Context, call aiagent.ToolCall) (aiagent.ToolResult, error) {
			title := strings.TrimSpace(call.ArgString("title"))
			if title == "" {
				return aiagent.ErrorResult("试卷标题不能为空"), nil
			}
			// 走 PaperService，因此权限判定与 HTTP 接口完全一致。
			paper, err := deps.papers.CreatePaper(ctx, call.User, call.SubjectID, &schema.PaperCreateRequest{
				Title:       title,
				Description: call.ArgString("description"),
			})
			if err != nil {
				return aiagent.ToolResult{}, err
			}
			return aiagent.ToolResult{
				Content: fmt.Sprintf("已创建试卷《%s》（id=%d）。可以继续往里面加题。", paper.Title, paper.ID),
				Data:    map[string]any{"paper_id": paper.ID, "title": paper.Title},
			}, nil
		},
	})

	// ---- 客户端工具 ----
	//
	// 为什么必须是客户端工具：用户在组卷编辑器里改了一半还没保存，
	// 后端只看到上一次保存的状态。如果 AI 直接改数据库，用户界面不会变，
	// 而且下一次自动保存会把 AI 的改动整体覆盖掉。
	// 让前端执行，AI 操作的才是"用户眼前的那份卷子"。

	r.MustRegister(&aiagent.Tool{
		Name: "read_paper_outline",
		Description: "读取用户当前正在编辑的试卷结构（含未保存的改动）。" +
			"在改动试卷之前先调用它，避免基于过期的结构做修改。",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Executor: aiagent.ExecutorClient,
		Scenes:   []aiagent.Scene{aiagent.SceneCompositionEditor},
	})

	r.MustRegister(&aiagent.Tool{
		Name: "add_questions_to_paper",
		Description: "把题目加入用户当前正在编辑的试卷末尾。用户说「把这几道题加进去」时使用。" +
			"改动会先在前端预览，由用户决定是否应用。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"question_ids": map[string]any{
					"type": "array", "items": map[string]any{"type": "integer"},
					"description": "要加入的题目 id",
				},
			},
			"required": []string{"question_ids"},
		},
		Executor: aiagent.ExecutorClient,
		Scenes:   []aiagent.Scene{aiagent.SceneCompositionEditor},
		// 预处理：在服务端先确认这些题目确实存在且对用户可见。
		// 不先查一遍的话，请求推给前端才发现引用无效，
		// 用户要白等一轮 30 秒超时才看到"题目不存在"。
		Prepare: func(ctx context.Context, call aiagent.ToolCall) (map[string]any, error) {
			ids := call.ArgUintSlice("question_ids")
			if len(ids) == 0 {
				return nil, apperr.New(apperr.CodeInvalidParam, "question_ids 不能为空")
			}
			if len(ids) > 50 {
				return nil, apperr.New(apperr.CodeInvalidParam,
					"一次最多加入 50 道题").WithDetail("实际 %d 道", len(ids))
			}
			for _, id := range ids {
				if _, err := deps.questions.Get(ctx, call.User, id); err != nil {
					return nil, err
				}
			}
			return map[string]any{"question_ids": ids}, nil
		},
	})

	r.MustRegister(&aiagent.Tool{
		Name: "edit_paper",
		Description: "修改用户当前正在编辑的试卷：新增说明文字、插入分页符、删除节点、调整顺序等。" +
			"所有改动都会先在前端展示差异，由用户决定是否应用。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"operations": map[string]any{
					"type":        "array",
					"description": "操作列表",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"op": map[string]any{
								"type": "string",
								"enum": []string{"insert_text", "insert_heading", "insert_page_break", "remove_node", "move_node"},
							},
							"content":   map[string]any{"type": "string", "description": "insert_text / insert_heading 的内容，Markdown 格式"},
							"node_id":   map[string]any{"type": "string", "description": "remove_node / move_node 的目标节点 id"},
							"before_id": map[string]any{"type": "string", "description": "插到哪个节点之前，省略表示末尾"},
							"position":  map[string]any{"type": "integer", "description": "move_node 的目标序号"},
							"level":     map[string]any{"type": "integer", "description": "insert_heading 的标题级别"},
						},
						"required": []string{"op"},
					},
				},
			},
			"required": []string{"operations"},
		},
		Executor: aiagent.ExecutorClient,
		Scenes:   []aiagent.Scene{aiagent.SceneCompositionEditor},
		// 预处理把 Markdown 转成富文本结构，让前端只做纯粹的结构操作。
		Prepare: prepareEditPaper,
	})
}

// prepareEditPaper 校验并富化 edit_paper 的操作列表。
//
// 放在服务端做的价值：前端不必再实现一遍 Markdown → 富文本的转换，
// 两条路径（AI 改卷与用户手动编辑）产出的节点结构也完全一致。
func prepareEditPaper(_ context.Context, call aiagent.ToolCall) (map[string]any, error) {
	raw, ok := call.Arguments["operations"]
	if !ok {
		return nil, apperr.New(apperr.CodeInvalidParam, "缺少 operations")
	}
	list, ok := raw.([]any)
	if !ok || len(list) == 0 {
		return nil, apperr.New(apperr.CodeInvalidParam, "operations 必须是非空数组")
	}

	out := make([]map[string]any, 0, len(list))
	for i, item := range list {
		op, ok := item.(map[string]any)
		if !ok {
			return nil, apperr.New(apperr.CodeInvalidParam, "操作格式不正确").
				WithDetail("第 %d 个", i+1)
		}
		kind, _ := op["op"].(string)

		normalized := map[string]any{"op": kind}
		switch kind {
		case "insert_text":
			content, _ := op["content"].(string)
			if strings.TrimSpace(content) == "" {
				return nil, apperr.New(apperr.CodeInvalidParam, "操作缺少 content").
					WithDetail("第 %d 个", i+1)
			}
			normalized["content"] = importing.MarkdownToRichDoc(content)
			normalized["markdown"] = content
		case "insert_heading":
			content, _ := op["content"].(string)
			if strings.TrimSpace(content) == "" {
				return nil, apperr.New(apperr.CodeInvalidParam, "操作缺少 content").
					WithDetail("第 %d 个", i+1)
			}
			level := 1
			if v, ok := op["level"].(float64); ok && v >= 1 && v <= 6 {
				level = int(v)
			}
			normalized["content"] = importing.MarkdownToRichDoc(content)
			normalized["markdown"] = content
			normalized["level"] = level
		case "insert_page_break":
			// 无附加参数
		case "remove_node", "move_node":
			nodeID, _ := op["node_id"].(string)
			if strings.TrimSpace(nodeID) == "" {
				return nil, apperr.New(apperr.CodeInvalidParam, "操作缺少 node_id").
					WithDetail("第 %d 个", i+1)
			}
			normalized["node_id"] = nodeID
			if kind == "move_node" {
				if v, ok := op["position"].(float64); ok {
					normalized["position"] = int(v)
				}
			}
		default:
			return nil, apperr.New(apperr.CodeInvalidParam, "操作类型不受支持").
				WithDetail("第 %d 个 op=%q", i+1, kind)
		}

		if before, ok := op["before_id"].(string); ok && before != "" {
			normalized["before_id"] = before
		}
		out = append(out, normalized)
	}

	return map[string]any{"operations": out}, nil
}

// ---- 结果组装 ----

// questionListResult 把题目列表压成模型易读的紧凑结构。
//
// 刻意不把富文本、选项、答案都塞进去：一次检索可能返回 8 道题，
// 全套内容会占用大量上下文，而且模型在"挑题"阶段并不需要这些细节。
// 需要细节时它应该再调 get_question。
func questionListResult(briefs []schema.QuestionBrief, total int64) aiagent.ToolResult {
	type item struct {
		ID     uint     `json:"id"`
		Type   string   `json:"type"`
		Label  string   `json:"type_label"`
		Diff   int      `json:"difficulty"`
		Status string   `json:"status"`
		Stem   string   `json:"stem"`
		Tags   []string `json:"tags,omitempty"`
		Points []string `json:"knowledge_points,omitempty"`
	}
	items := make([]item, 0, len(briefs))
	for i := range briefs {
		b := &briefs[i]
		it := item{
			ID:     b.ID,
			Type:   string(b.QuestionType),
			Label:  b.QuestionTypeLabel,
			Diff:   b.Difficulty,
			Status: string(b.Status),
			Stem:   truncateRunes(b.StemText, 120),
		}
		for _, t := range b.Tags {
			it.Tags = append(it.Tags, t.Name)
		}
		for _, k := range b.KnowledgePoints {
			it.Points = append(it.Points, k.Name)
		}
		items = append(items, it)
	}
	return marshalResult(map[string]any{
		"total": total,
		"items": items,
		"hint":  "题干为摘要。需要完整内容时请用 get_question。",
	})
}

func questionDetailResult(d *schema.QuestionDetail) aiagent.ToolResult {
	payload := map[string]any{
		"id":             d.ID,
		"type":           string(d.QuestionType),
		"type_label":     d.QuestionTypeLabel,
		"difficulty":     d.Difficulty,
		"status":         string(d.Status),
		"stem":           d.StemText,
		"answer":         d.Answer,
		"stem_richtext":  json.RawMessage(d.Stem),
		"analysis_text":  plainFromJSON(d.Analysis),
		"children_count": len(d.Children),
	}
	if len(d.Options) > 0 {
		opts := make([]map[string]string, 0, len(d.Options))
		for _, o := range d.Options {
			opts = append(opts, map[string]string{
				"label": o.Label,
				"text":  plainFromJSON(o.Content),
			})
		}
		payload["options"] = opts
	}
	if len(d.Children) > 0 {
		kids := make([]map[string]any, 0, len(d.Children))
		for i := range d.Children {
			kids = append(kids, map[string]any{
				"id":   d.Children[i].ID,
				"stem": truncateRunes(d.Children[i].StemText, 80),
			})
		}
		payload["children"] = kids
	}
	return marshalResult(payload)
}

// plainFromJSON 从富文本 JSON 中提取纯文本。
func plainFromJSON(raw model.JSON) string {
	if raw.IsEmpty() {
		return ""
	}
	var sb strings.Builder
	var walk func(v any)
	walk = func(v any) {
		switch node := v.(type) {
		case map[string]any:
			if t, ok := node["text"].(string); ok {
				sb.WriteString(t)
			}
			if node["type"] == "blank" {
				sb.WriteString("______")
			}
			if content, ok := node["content"].([]any); ok {
				for _, c := range content {
					walk(c)
				}
			}
		case []any:
			for _, c := range node {
				walk(c)
			}
		}
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return ""
	}
	walk(decoded)
	return strings.TrimSpace(sb.String())
}

// marshalResult 把结构序列化为回喂模型的文本。
func marshalResult(v any) aiagent.ToolResult {
	b, err := json.Marshal(v)
	if err != nil {
		return aiagent.ErrorResult("结果序列化失败")
	}
	data, _ := v.(map[string]any)
	return aiagent.ToolResult{Content: string(b), Data: data}
}
