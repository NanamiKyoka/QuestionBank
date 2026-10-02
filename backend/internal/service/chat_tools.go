package service

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/NanamiKyoka/questionmind/internal/aiagent"
	"github.com/NanamiKyoka/questionmind/internal/domain"
	"github.com/NanamiKyoka/questionmind/internal/importing"
	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
	"github.com/NanamiKyoka/questionmind/internal/schema"
)

// toolDeps 工具实现所需的业务服务。
//
// 全部走 service 层，因此**与 HTTP 接口共用同一套鉴权与校验**；
// 客户端工具只做服务端预处理，真正的执行由前端完成。
type toolDeps struct {
	questions       *QuestionService
	knowledgePoints *KnowledgePointService
	tags            *TagService
	papers          *PaperService
	subjects        *SubjectService
	stats           *StatsService
	vectors         *VectorService
	imports         *ImportService
}

// buildToolRegistry 注册全部暴露给模型的工具。
func buildToolRegistry(deps toolDeps) *aiagent.Registry {
	r := aiagent.NewRegistry()

	registerQuestionTools(r, deps)
	registerLibraryTools(r, deps)
	registerPaperTools(r, deps)
	registerReadTools(r, deps)

	return r
}

// ---- 题库检索 ----

func registerQuestionTools(r *aiagent.Registry, deps toolDeps) {
	r.MustRegister(&aiagent.Tool{
		Name: "search_questions",
		Description: "按关键词、题型、难度、状态、知识点或标签检索题库中的题目。返回题目 id、题型、难度与题干摘要。" +
			"当用户想找题、想了解题库里有什么，或要整理前先摸清某一类的现状时使用。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"keyword":       map[string]any{"type": "string", "description": "题干关键词"},
				"question_type": map[string]any{"type": "string", "enum": []string{"single_choice", "multiple_choice", "fill_blank", "true_false", "essay"}},
				"difficulty":    map[string]any{"type": "integer", "minimum": 1, "maximum": 5},
				"status": map[string]any{
					"type": "string", "enum": []string{"draft", "pending", "published", "archived"},
					"description": "按状态筛选。省略则不限状态。",
				},
				"knowledge_point_ids": map[string]any{
					"type": "array", "items": map[string]any{"type": "integer"},
					"description": "只返回关联了这些知识点的题目，id 取自 list_knowledge_points",
				},
				"include_sub": map[string]any{
					"type":        "boolean",
					"description": "是否包含子知识点下的题目，默认 true",
				},
				"source":       map[string]any{"type": "string", "description": "来源模糊匹配，可省略"},
				"only_deleted": map[string]any{"type": "boolean", "description": "查回收站里的已删除题目"},
				"limit":        map[string]any{"type": "integer", "minimum": 1, "maximum": 30, "description": "最多返回几道题，默认 8"},
				"offset":       map[string]any{"type": "integer", "minimum": 0, "description": "翻页用，默认 0"},
			},
		},
		Executor: aiagent.ExecutorServer,
		Scenes:   readScenes,
		Handler: func(ctx context.Context, call aiagent.ToolCall) (aiagent.ToolResult, error) {
			limit := clampArgInt(call.ArgInt("limit"), 1, 30, 8)
			offset := call.ArgInt("offset")
			if offset < 0 {
				offset = 0
			}

			briefs, total, err := deps.questions.List(ctx, call.User, call.SubjectID, ListQuery{
				Keyword:           call.ArgString("keyword"),
				Type:              model.QuestionType(call.ArgString("question_type")),
				Difficulty:        call.ArgInt("difficulty"),
				Status:            model.QuestionStatus(call.ArgString("status")),
				KnowledgePointIDs: call.ArgUintSlice("knowledge_point_ids"),
				IncludeSub:        call.ArgBool("include_sub"),
				Source:            call.ArgString("source"),
				OnlyDeleted:       call.ArgBool("only_deleted"),
				RootOnly:          true,
			}, limit, offset)
			if err != nil {
				return aiagent.ToolResult{}, err
			}

			if total == 0 {
				return aiagent.ToolResult{
					Content: "没有找到符合条件的题目。可以放宽条件再试，或告诉用户题库里没有相关题目。",
					Data:    map[string]any{"total": 0, "items": []any{}},
				}, nil
			}

			return questionListResult(briefs, total, offset), nil
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
	//
	// 三条共同的前端契约（frontend/src/composables/usePaperAiTools.ts 逐条实现）：
	//  1. 读的是**未保存**的工作副本；
	//  2. 写操作一律先挂成待确认预览，用户点「应用」才落到工作副本；
	//  3. 工具本身**立即**返回——人工确认可能要几分钟，
	//     远超前端的 30 秒票据与服务端的运行预算。等到用户点确认再回执，
	//     等于每次都要白等一次超时。

	r.MustRegister(&aiagent.Tool{
		Name: "read_paper_outline",
		Description: "读取用户当前正在编辑的试卷结构（含未保存的改动）。" +
			"在改动试卷之前先调用它，避免基于过期的结构做修改。" +
			"返回的每一行都带 node_id，edit_paper 的 node_id / before_id 只能取这些值。" +
			"仅在组卷编辑器页面内可用，在其它页面调用会直接失败。",
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
			"改动会先在前端预览，由用户点「应用」后才生效；本工具立即返回预览结果，" +
			"请据此告诉用户还有若干处改动等待确认，不要重复提交同一批题目。" +
			"仅在组卷编辑器页面内可用。",
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
		Prepare: prepareAddQuestions(deps),
	})

	r.MustRegister(&aiagent.Tool{
		Name: "edit_paper",
		Description: "修改用户当前正在编辑的试卷：新增说明文字、插入分页符、删除节点、调整顺序等。" +
			"所有改动都会先在前端展示差异，由用户点「应用」确认后才生效；本工具立即返回预览结果。" +
			"node_id / before_id 只能取自 read_paper_outline 返回的值。" +
			"仅在组卷编辑器页面内可用。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"summary": map[string]any{
					"type":        "string",
					"description": "一句话说明这批改动做了什么，显示给用户确认，建议填写",
				},
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

// ---- 客户端工具的预处理 ----

const (
	// maxAddQuestions 单次 add_questions_to_paper 的题目数上限。
	maxAddQuestions = 50
	// maxEditPaperOps 单次 edit_paper 的操作数上限。
	//
	// 没有上限时，模型一次吐出上千条操作会让那一帧 SSE 膨胀到几 MB，
	// 前端也要照单全收地重排上千次——而人类根本不会逐条确认这么长的改动。
	maxEditPaperOps = 50
	// maxInsertContentRunes 单条插入内容的字符数上限。
	//
	// 一份试卷的说明文字不会有几万字；超了基本可以断定是模型把整篇参考资料
	// 原样灌了进来（提示注入的典型表现），直接拦掉。
	maxInsertContentRunes = 20000
	// maxSummaryRunes 改动摘要的字数上限，展示用，超出即截断。
	maxSummaryRunes = 200
	// maxNodeIDLen 节点 id 长度上限，与 nodeIDPattern 的 {1,64} 保持一致。
	maxNodeIDLen = 64
)

// nodeIDPattern 节点 id 的形状。
//
// 节点 id 由客户端的 crypto.randomUUID() 生成，服务端保存时也用 uuid，
// 因此永远是 [A-Za-z0-9_-] 这类不透明标识符，模型给不出别的形状，
// 而"给不出"正是这里要的：这条 id 一路走到前端的节点查找与写回，
// 放任任意字符串穿过去，等于把一段模型可控文本当成内部标识符使用。
// （提示注入的载体正是外部文档：模型完全可能被文档里的文字
// "指挥"去操作一个看起来很正常的路径或 URL。）
var nodeIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// prepareAddQuestions 校验加题请求并去重。
//
// 返回的参数会**替换**下发给前端的入参，因此去重放在这里比放在前端更合适：
// 前端拿到的必然是排好序、不含重复的列表，界面上的"N 处改动"
// 也就与实际将要插入的节点数一致。
func prepareAddQuestions(deps toolDeps) aiagent.ToolPreparer {
	return func(ctx context.Context, call aiagent.ToolCall) (map[string]any, error) {
		ids := call.ArgUintSlice("question_ids")
		if len(ids) == 0 {
			return nil, apperr.New(apperr.CodeInvalidParam, "question_ids 不能为空")
		}
		if len(ids) > maxAddQuestions {
			return nil, apperr.New(apperr.CodeInvalidParam,
				fmt.Sprintf("一次最多加入 %d 道题", maxAddQuestions)).
				WithDetail("实际 %d 道", len(ids))
		}

		seen := make(map[uint]struct{}, len(ids))
		uniq := make([]uint, 0, len(ids))
		for _, id := range ids {
			// ArgUintSlice 会把模型写的 0 也当成合法 id 收进来，
			// 而它会在下面的 Get 里变成一条看不懂的 404，这里直接说清楚。
			if id == 0 {
				return nil, apperr.New(apperr.CodeInvalidParam, "question_ids 含有无效的题目 id 0")
			}
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			uniq = append(uniq, id)
		}

		for _, id := range uniq {
			if _, err := deps.questions.Get(ctx, call.User, id); err != nil {
				return nil, err
			}
		}
		return map[string]any{"question_ids": uniq}, nil
	}
}

// prepareEditPaper 校验并富化 edit_paper 的操作列表。
//
// 放在服务端做的价值：前端不必再实现一遍 Markdown → 富文本的转换，
// 两条路径（AI 改卷与用户手动编辑）产出的节点结构也完全一致；
// 同时这里就成了唯一一道闸——模型给出的每个字符串在进入前端之前
// 都要先过形状与长度校验。
func prepareEditPaper(_ context.Context, call aiagent.ToolCall) (map[string]any, error) {
	raw, ok := call.Arguments["operations"]
	if !ok {
		return nil, apperr.New(apperr.CodeInvalidParam, "缺少 operations")
	}
	list, ok := raw.([]any)
	if !ok || len(list) == 0 {
		return nil, apperr.New(apperr.CodeInvalidParam, "operations 必须是非空数组")
	}
	if len(list) > maxEditPaperOps {
		return nil, apperr.New(apperr.CodeInvalidParam,
			fmt.Sprintf("一次最多提交 %d 个操作", maxEditPaperOps)).
			WithDetail("实际 %d 个", len(list))
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
		case "insert_text", "insert_heading":
			content, _ := op["content"].(string)
			if strings.TrimSpace(content) == "" {
				return nil, apperr.New(apperr.CodeInvalidParam, "操作缺少 content").
					WithDetail("第 %d 个", i+1)
			}
			if n := utf8.RuneCountInString(content); n > maxInsertContentRunes {
				return nil, apperr.New(apperr.CodeInvalidParam, "插入内容过长").
					WithDetail("第 %d 个：%d 字，上限 %d 字", i+1, n, maxInsertContentRunes)
			}
			normalized["content"] = importing.MarkdownToRichDoc(content)
			normalized["markdown"] = content
			if kind == "insert_heading" {
				level := 1
				if v, ok := op["level"].(float64); ok && v >= 1 && v <= 6 {
					level = int(v)
				}
				normalized["level"] = level
			}
		case "insert_page_break":
			// 无附加参数
		case "remove_node", "move_node":
			nodeID, err := readNodeID(op, "node_id", i+1)
			if err != nil {
				return nil, err
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

		if _, has := op["before_id"]; has {
			before, err := readNodeID(op, "before_id", i+1)
			if err != nil {
				return nil, err
			}
			normalized["before_id"] = before
		}
		out = append(out, normalized)
	}

	prepared := map[string]any{"operations": out}
	if summary := truncateRunes(call.ArgString("summary"), maxSummaryRunes); summary != "" {
		prepared["summary"] = summary
	}
	return prepared, nil
}

// readNodeID 读一个节点标识。
//
// 节点 id 由前端的 crypto.randomUUID() 与服务端的 uuid 生成，永远是字符串。
// 因此模型把它写成数字**不是可以宽容一点的笔误**，而是它手上根本没有这个节点：
// 转成字符串只会把问题推迟成一句更难懂的"找不到节点 42"。
// 形状对不上（路径、URL、一段注入文本）同样在这里拦下——
// 这条 id 会一路走到前端的节点查找与写回。
func readNodeID(op map[string]any, key string, at int) (string, error) {
	raw, has := op[key]
	if !has {
		return "", apperr.New(apperr.CodeInvalidParam, "操作缺少 "+key).
			WithDetail("第 %d 个", at)
	}

	id, isString := raw.(string)
	// 空串算"缺失"、非字符串算"无效"：两句引导会把模型带去完全不同的地方
	// （前者去补参数，后者去读大纲），不能混成一句。
	invalid := func() error {
		return apperr.New(apperr.CodeInvalidParam, key+" 不是有效的节点标识").
			WithDetail("第 %d 个：请先用 read_paper_outline 取得真实节点 id", at)
	}
	if !isString {
		return "", invalid()
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return "", apperr.New(apperr.CodeInvalidParam, "操作缺少 "+key).
			WithDetail("第 %d 个", at)
	}
	if !nodeIDPattern.MatchString(id) {
		return "", invalid()
	}
	return id, nil
}

// ---- 结果组装 ----

// questionListResult 把题目列表压成模型易读的紧凑结构。
//
// 刻意不把富文本、选项、答案都塞进去：一次检索可能返回 8 道题，
// 全套内容会占用大量上下文，而且模型在"挑题"阶段并不需要这些细节。
// 需要细节时它应该再调 get_question。
func questionListResult(briefs []schema.QuestionBrief, total int64, offset int) aiagent.ToolResult {
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

	hints := []string{"题干为摘要。需要完整内容时请用 get_question。"}
	// 翻页提示要给出**下一个** offset 而不是"还有更多"：
	// 模型据此就能自己走到第二页，不必为同一批条件反复试不同的 offset。
	if next := offset + len(briefs); int64(next) < total {
		hints = append([]string{fmt.Sprintf("还有更多结果：本次从第 %d 条开始，请用 offset=%d 继续取。", offset+1, next)}, hints...)
	}
	return marshalResult(map[string]any{
		"total": total,
		"items": items,
		"hint":  strings.Join(hints, " "),
	})
}

func questionDetailResult(d *schema.QuestionDetail) aiagent.ToolResult {
	payload := map[string]any{
		"id":             d.ID,
		"type":           string(d.QuestionType),
		"type_label":     d.QuestionTypeLabel,
		"difficulty":     d.Difficulty,
		"status":         string(d.Status),
		"stem":           markdownFromJSON(d.Stem),
		"answer":         d.Answer,
		"stem_richtext":  json.RawMessage(d.Stem),
		"analysis_text":  markdownFromJSON(d.Analysis),
		"children_count": len(d.Children),
	}
	if len(d.Options) > 0 {
		opts := make([]map[string]string, 0, len(d.Options))
		for _, o := range d.Options {
			opts = append(opts, map[string]string{
				"label": o.Label,
				"text":  markdownFromJSON(o.Content),
			})
		}
		payload["options"] = opts
	}
	if len(d.Children) > 0 {
		kids := make([]map[string]any, 0, len(d.Children))
		for i := range d.Children {
			kids = append(kids, map[string]any{
				"id":   d.Children[i].ID,
				"stem": truncateRunes(markdownFromJSON(d.Children[i].StemDoc), 80),
			})
		}
		payload["children"] = kids
	}
	return marshalResult(payload)
}

// markdownFromJSON 把富文本还原成带 $ 定界符的 Markdown 文本。
//
// 早先这里是一个只认 text 字段的手写递归，于是 inlineMath / blockMath
// 节点**整段消失**：公式的文字藏在 attrs.latex 里而不是 content 里，
// 走不到 text 分支。模型看到的是"若 ，则…"——题干残缺、选项变空串，
// 于是它给出的判断多半是错的，而错误原因却看不出在这里。
//
// domain.MarkdownText 是同一份文档树在"喂给模型"这条路径上的既定实现，
// 公式、空位、表格、图片占位都处理过，直接复用而不是再写一份。
func markdownFromJSON(raw model.JSON) string {
	if raw.IsEmpty() {
		return ""
	}
	var doc domain.RichDoc
	if err := raw.Into(&doc); err != nil {
		return ""
	}
	return domain.MarkdownText(doc)
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
