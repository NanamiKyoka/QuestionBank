package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/NanamiKyoka/questionmind/internal/aiagent"
	"github.com/NanamiKyoka/questionmind/internal/model"
)

// registerReadTools 注册只读工具。
//
// 全部标记 Mutating=false：它们只回答"现在是什么样"，不改任何状态。
// 这类工具是写操作的安全前提——模型在动手之前必须先能查清现状，
// 而现状只能从读工具里来。
func registerReadTools(r *aiagent.Registry, deps toolDeps) {
	r.MustRegister(&aiagent.Tool{
		Name: "list_papers",
		Description: "列出当前学科下的试卷。用户问「我有哪些卷子」「上周那套卷叫什么」时使用。" +
			"返回试卷 id、标题、状态与题数；id 可直接用于 get_paper、update_paper、duplicate_paper。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"keyword": map[string]any{"type": "string", "description": "标题关键词，可省略"},
				"status": map[string]any{
					"type": "string", "enum": []string{"draft", "finalized", "archived"},
					"description": "按状态筛选，可省略",
				},
				"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 30},
			},
		},
		Executor: aiagent.ExecutorServer,
		Scenes:   readScenes,
		Handler: func(ctx context.Context, call aiagent.ToolCall) (aiagent.ToolResult, error) {
			limit := clampArgInt(call.ArgInt("limit"), 1, 30, 10)

			papers, total, err := deps.papers.ListPapers(ctx, call.User, call.SubjectID, ListPapersQuery{
				Keyword: strings.TrimSpace(call.ArgString("keyword")),
				Status:  model.PaperStatus(call.ArgString("status")),
			}, limit, 0)
			if err != nil {
				return aiagent.ToolResult{}, err
			}

			type item struct {
				ID     uint   `json:"id"`
				Title  string `json:"title"`
				Status string `json:"status"`
				Count  int    `json:"question_count"`
				Score  string `json:"total_score"`
			}
			items := make([]item, 0, len(papers))
			for i := range papers {
				p := &papers[i]
				items = append(items, item{
					ID:     p.ID,
					Title:  p.Title,
					Status: string(p.Status),
					Count:  p.QuestionCount,
					Score:  fmt.Sprintf("%g", p.TotalScore),
				})
			}
			return marshalResult(map[string]any{
				"total": total,
				"items": items,
			}), nil
		},
	})

	r.MustRegister(&aiagent.Tool{
		Name: "get_paper",
		Description: "读取一份**已保存**的试卷，包括题目内容与节点结构。" +
			"注意与 read_paper_outline 的区别：本工具读到的是数据库里的状态，" +
			"不含用户在编辑器里尚未保存的改动；需要后者请在组卷页面用 read_paper_outline。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"paper_id": map[string]any{"type": "integer", "description": "试卷 id"},
			},
			"required": []string{"paper_id"},
		},
		Executor: aiagent.ExecutorServer,
		Scenes:   readScenes,
		Handler: func(ctx context.Context, call aiagent.ToolCall) (aiagent.ToolResult, error) {
			id := uint(call.ArgInt("paper_id"))
			if id == 0 {
				return aiagent.ErrorResult("缺少 paper_id"), nil
			}
			paper, err := deps.papers.GetPaper(ctx, call.User, id)
			if err != nil {
				return aiagent.ToolResult{}, err
			}

			questions := make([]map[string]any, 0, len(paper.Nodes))
			for _, node := range paper.Nodes {
				if node.QuestionID == nil {
					continue
				}
				entry := map[string]any{
					"node_id":     node.ID,
					"question_id": *node.QuestionID,
					"position":    node.Position,
				}
				if proj, ok := paper.Questions[*node.QuestionID]; ok {
					entry["stem"] = truncateRunes(proj.StemText, 120)
					entry["type"] = string(proj.QuestionType)
					entry["difficulty"] = proj.Difficulty
					entry["stale"] = proj.Stale
				}
				questions = append(questions, entry)
			}
			return marshalResult(map[string]any{
				"id":             paper.ID,
				"title":          paper.Title,
				"description":    paper.Description,
				"status":         string(paper.Status),
				"revision":       paper.Revision,
				"question_count": paper.QuestionCount,
				"total_score":    paper.TotalScore,
				"questions":      questions,
				"hint":           "stale 为真表示题库里的题目内容已改动，用户可以在界面上同步。",
			}), nil
		},
	})

	r.MustRegister(&aiagent.Tool{
		Name: "get_subject_stats",
		Description: "当前学科的题库概览：题目总数与状态分布、知识点数、标签数、试卷数。" +
			"用户问「我题库里有多少题」「哪类题最多」或在做整理前需要现状快照时使用。",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Executor: aiagent.ExecutorServer,
		Scenes:   readScenes,
		Handler: func(ctx context.Context, call aiagent.ToolCall) (aiagent.ToolResult, error) {
			if deps.stats == nil {
				return aiagent.ErrorResult("统计功能不可用"), nil
			}
			stats, err := deps.stats.Subject(ctx, call.User, call.SubjectID)
			if err != nil {
				return aiagent.ToolResult{}, err
			}
			return marshalResult(map[string]any{
				"subject":          stats.SubjectName,
				"questions":        stats.Questions,
				"knowledge_points": stats.KnowledgePoints,
				"tags":             stats.Tags,
				"papers":           stats.Papers,
			}), nil
		},
	})

	r.MustRegister(&aiagent.Tool{
		Name: "list_review_queue",
		Description: "列出待审核的题目（状态为「待审」）。用户问「有哪些题等着审」时使用。" +
			"返回题目 id 与题干摘要，可配合 batch_review_questions 批量处理。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 30},
			},
		},
		Executor: aiagent.ExecutorServer,
		Scenes:   readScenes,
		Handler: func(ctx context.Context, call aiagent.ToolCall) (aiagent.ToolResult, error) {
			limit := clampArgInt(call.ArgInt("limit"), 1, 30, 10)
			briefs, total, err := deps.questions.List(ctx, call.User, call.SubjectID, ListQuery{
				Status:   model.QuestionStatusPending,
				RootOnly: true,
			}, limit, 0)
			if err != nil {
				return aiagent.ToolResult{}, err
			}
			if total == 0 {
				return aiagent.ToolResult{
					Content: "当前没有待审核的题目。",
					Data:    map[string]any{"total": int64(0), "items": []any{}},
				}, nil
			}
			return questionListResult(briefs, total, 0), nil
		},
	})

	r.MustRegister(&aiagent.Tool{
		Name: "semantic_search_knowledge_points",
		Description: "按语义检索知识点。关键词匹配找不到合适的知识点时用它——" +
			"例如题干说的是「导数单调性」而知识点叫「函数单调性与极值」。" +
			"返回知识点 id，可直接用于 set_question_knowledge_points。" +
			"需要配置向量模型且索引完整，否则会说明为什么不可用。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "要检索的语义描述"},
				"top_k": map[string]any{"type": "integer", "minimum": 1, "maximum": 20},
			},
			"required": []string{"query"},
		},
		Executor: aiagent.ExecutorServer,
		Scenes:   readScenes,
		Handler: func(ctx context.Context, call aiagent.ToolCall) (aiagent.ToolResult, error) {
			if deps.vectors == nil {
				return aiagent.ErrorResult("语义检索不可用"), nil
			}
			query := strings.TrimSpace(call.ArgString("query"))
			if query == "" {
				return aiagent.ErrorResult("缺少 query"), nil
			}
			topK := clampArgInt(call.ArgInt("top_k"), 1, 20, 8)

			res, err := deps.vectors.Search(ctx, call.User, call.SubjectID, query, topK)
			if err != nil {
				return aiagent.ToolResult{}, err
			}
			if len(res.Results) == 0 {
				return aiagent.ToolResult{
					Content: "没有语义匹配的知识点。可改用 list_knowledge_points 按名称筛选。",
					Data:    map[string]any{"results": []any{}},
				}, nil
			}
			return marshalResult(map[string]any{
				"query":   res.Query,
				"model":   res.ModelName,
				"results": res.Results,
				"hint":    "score 越接近 1 越匹配。关联知识点时使用 knowledge_point_id。",
			}), nil
		},
	})

	r.MustRegister(&aiagent.Tool{
		Name:        "list_subjects",
		Description: "列出当前用户可见的学科。会话没有绑定学科时，用它让用户挑一个再开始。",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Executor: aiagent.ExecutorServer,
		Scenes:   readScenes,
		Handler: func(ctx context.Context, call aiagent.ToolCall) (aiagent.ToolResult, error) {
			if deps.subjects == nil {
				return aiagent.ErrorResult("学科功能不可用"), nil
			}
			list, err := deps.subjects.List(ctx, call.User)
			if err != nil {
				return aiagent.ToolResult{}, err
			}
			type item struct {
				ID   uint   `json:"id"`
				Name string `json:"name"`
				Role string `json:"role_label"`
			}
			items := make([]item, 0, len(list))
			for i := range list {
				items = append(items, item{ID: list[i].ID, Name: list[i].Name, Role: list[i].RoleLabel})
			}
			return marshalResult(map[string]any{
				"items": items,
				"hint":  "让用户在界面上切换到某个学科后，本会话的题目与试卷操作才会作用到该学科。",
			}), nil
		},
	})
}

// readScenes 只读工具的可见场景。
//
// 刻意不挂到 SceneCompositionEditor 之外的地方以外：不写场景意味着
// "处处可用"，而这是个安全默认值——scene 由客户端自报、不可信。
var readScenes = []aiagent.Scene{
	aiagent.SceneQuestionLibrary,
	aiagent.SceneCompositionEditor,
	aiagent.SceneImportReview,
}

// clampArgInt 把模型给的整数夹进区间。
//
// 模型经常把 limit 写成 0、负数或 1000。夹住比报错好：0 与 1000 都是
// "想拿一批"的意思，而报错会让它在同一个参数上反复重试。
func clampArgInt(v, lo, hi, fallback int) int {
	if v == 0 {
		return fallback
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
