package service

import (
	"context"
	"fmt"

	"github.com/NanamiKyoka/questionmind/internal/aiagent"
	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
	"github.com/NanamiKyoka/questionmind/internal/schema"
)

// registerJobTools 注册耗时任务相关的工具。
//
// 这些工具存在的理由是**预算**：一次运行最多 20 次工具调用、180 秒，
// 因此"逐题给建议再逐题写回"这种 200 道题就撑爆的循环，
// 必须收进一次调用里。代价是过程不可见——所以每个工具都先告诉模型
// 这次大概会做什么，让它决定要不要在有人的场合执行。
func registerJobTools(r *aiagent.Registry, deps toolDeps) {
	r.MustRegister(&aiagent.Tool{
		Name: "reindex_knowledge_vectors",
		Description: "重建当前学科的知识点向量索引。索引缺失、混用了不同的向量模型， " +
			"或换了 embedding 模型之后，semantic_search_knowledge_points 的结果会不准，这时用它修复。" +
			"需要学科管理权限。force=true 会重算全部知识点（较慢，但换了模型后必须用），" +
			"默认 false 只补算没索引或内容变了的。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"force": map[string]any{
					"type":        "boolean",
					"description": "true 全部重算，换过向量模型时必须为 true；默认只补算缺失的",
				},
			},
		},
		Executor:   aiagent.ExecutorServer,
		Mutating:   true,
		Capability: "vector.reindex",
		Scenes:     writeScenes,
		Handler: func(ctx context.Context, call aiagent.ToolCall) (aiagent.ToolResult, error) {
			if deps.vectors == nil {
				return aiagent.ErrorResult("向量功能不可用"), nil
			}
			res, err := deps.vectors.Reindex(ctx, call.User, call.SubjectID, call.ArgBool("force"))
			if err != nil {
				return aiagent.ToolResult{}, err
			}
			out := map[string]any{
				"total":      res.Total,
				"indexed":    res.Indexed,
				"skipped":    res.Skipped,
				"failed":     res.Failed,
				"elapsed_ms": res.ElapsedMs,
			}
			if len(res.Failures) > 0 {
				out["failures"] = res.Failures
			}
			if res.Failed > 0 {
				out["hint"] = fmt.Sprintf("有 %d 个知识点索引失败，语义检索对它们仍然不可用。", res.Failed)
			}
			return marshalResult(out), nil
		},
	})

	r.MustRegister(&aiagent.Tool{
		Name: "match_knowledge_points_batch",
		Description: "按语义为一批题目推荐知识点，并可一次性写回。这是「整理题库」最常用的能力：" +
			"用户说「给这几道题自动归类」「这些题都没关联知识点」时使用。" +
			"**先用 dry_run=true（默认）看一遍推荐结果**，把「哪几道题会挂到哪个知识点下」说给用户听，" +
			"确认后再以 dry_run=false 写回。推荐只是向量检索的近似结果，同义不同词仍可能漏配，" +
			"因此写回前务必让用户过目。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"question_ids": map[string]any{
					"type": "array", "items": map[string]any{"type": "integer"},
					"description": "题目 id；省略则处理当前学科下所有**没有**关联知识点的题目",
				},
				"limit_per_question": map[string]any{
					"type": "integer", "minimum": 1, "maximum": 5,
					"description": "每道题最多关联几个知识点，默认 1",
				},
				"only_unmatched": map[string]any{
					"type":        "boolean",
					"description": "只处理当前没有关联知识点的题目，省略为 true",
				},
				"dry_run": map[string]any{"type": "boolean", "description": "true 只预演不写入，省略等同于 true"},
			},
		},
		Executor:   aiagent.ExecutorServer,
		Mutating:   true,
		Capability: "question.knowledge_points.match",
		Scenes:     batchScenes,
		Handler: func(ctx context.Context, call aiagent.ToolCall) (aiagent.ToolResult, error) {
			if deps.vectors == nil {
				return aiagent.ErrorResult("向量功能不可用"), nil
			}
			// SuggestKnowledgePoints 自己不做鉴权，Status 会。
			// 顺带拿到「索引是否就绪」的答案，省得对着空索引空跑一轮。
			status, err := deps.vectors.Status(ctx, call.User, call.SubjectID)
			if err != nil {
				return aiagent.ToolResult{}, err
			}
			if !status.Ready {
				return aiagent.ErrorResult("语义匹配暂不可用：%s", status.Message), nil
			}

			limitPer := clampArgInt(call.ArgInt("limit_per_question"), 1, 5, 1)
			ids, err := matchTargetQuestions(ctx, deps, call)
			if err != nil {
				return aiagent.ErrorResult("%s", err.Error()), nil
			}
			if len(ids) == 0 {
				return aiagent.ToolResult{
					Content: "没有需要归类的题目：给出的题目都已经关联了知识点。",
					Data:    map[string]any{"total": 0, "items": []any{}},
				}, nil
			}

			type suggestion struct {
				QuestionID uint     `json:"question_id"`
				Stem       string   `json:"stem"`
				Points     []string `json:"knowledge_points"`
				Current    []string `json:"current_points,omitempty"`
			}
			items := make([]suggestion, 0, len(ids))
			matched := make([]uint, 0, len(ids))
			best := make(map[uint][]uint, len(ids))
			for _, id := range ids {
				detail, err := deps.questions.Get(ctx, call.User, id)
				if err != nil {
					continue
				}
				names := make([]string, 0)
				ids2 := make([]uint, 0, limitPer)
				for _, s := range deps.vectors.SuggestKnowledgePoints(ctx, call.SubjectID, detail.StemText, limitPer) {
					ids2 = append(ids2, s.KnowledgePointID)
					names = append(names, s.Name)
				}
				if len(ids2) == 0 {
					continue
				}
				best[id] = ids2
				matched = append(matched, id)

				item := suggestion{QuestionID: id, Stem: truncateRunes(detail.StemText, 60), Points: names}
				for _, kp := range detail.KnowledgePoints {
					item.Current = append(item.Current, kp.Name)
				}
				items = append(items, item)
			}

			if len(items) == 0 {
				return aiagent.ToolResult{
					Content: "没有匹配到任何知识点。可能是这些题目的题干过短，或者知识点索引覆盖不到这个方向；" +
						"可以先用 list_knowledge_points 看看有哪些可用的知识点。",
					Data: map[string]any{"total": len(ids), "items": []any{}},
				}, nil
			}

			if call.ArgBoolOr("dry_run", true) {
				return marshalResult(map[string]any{
					"dry_run":   true,
					"total":     len(items),
					"items":     items,
					"unmatched": len(ids) - len(items),
					"hint":      dryRunHint + " 匹配基于向量近似，同义不同词可能漏配，请让用户核对。",
				}), nil
			}

			applied := make([]uint, 0, len(matched))
			failed := make([]blockedItem, 0)
			for _, id := range matched {
				kpIDs := best[id]
				if _, err := deps.questions.Update(ctx, call.User, id,
					&schema.QuestionUpdateRequest{KnowledgePointIDs: &kpIDs}); err != nil {
					failed = append(failed, blockedItem{ID: id, Reason: shortError(err)})
					continue
				}
				applied = append(applied, id)
			}
			return marshalResult(map[string]any{
				"dry_run":   false,
				"total":     len(items),
				"affected":  applied,
				"blocked":   failed,
				"unmatched": len(ids) - len(items),
			}), nil
		},
	})

	r.MustRegister(&aiagent.Tool{
		Name: "get_vector_status",
		Description: "查看当前学科的知识点向量索引状态：已索引多少、是否就绪、为什么不可用。" +
			"semantic_search_knowledge_points 说不通时先查这个。",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Executor: aiagent.ExecutorServer,
		Scenes:   readScenes,
		Handler: func(ctx context.Context, call aiagent.ToolCall) (aiagent.ToolResult, error) {
			if deps.vectors == nil {
				return aiagent.ErrorResult("向量功能不可用"), nil
			}
			status, err := deps.vectors.Status(ctx, call.User, call.SubjectID)
			if err != nil {
				return aiagent.ToolResult{}, err
			}
			return marshalResult(map[string]any{
				"ready":         status.Ready,
				"total":         status.Total,
				"indexed":       status.Indexed,
				"missing":       status.Missing,
				"needs_rebuild": status.NeedsRebuild,
				"model":         status.ModelName,
				"message":       status.Message,
			}), nil
		},
	})

	r.MustRegister(&aiagent.Tool{
		Name: "get_import_status",
		Description: "查看智能录题的导入任务：最近几批录进来的题现在是什么状态。" +
			"用户问「刚才那批题录完了吗」时使用。" +
			"**助手不能替你上传或启动导入**——录题要走界面上传文件。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"status": map[string]any{
					"type":        "string",
					"enum":        []string{"pending", "processing", "completed", "failed", "cancelled"},
					"description": "按状态筛选，省略则不过滤",
				},
				"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 20},
			},
		},
		Executor: aiagent.ExecutorServer,
		Scenes:   readScenes,
		Handler: func(ctx context.Context, call aiagent.ToolCall) (aiagent.ToolResult, error) {
			if deps.imports == nil {
				return aiagent.ErrorResult("录题功能不可用"), nil
			}
			limit := clampArgInt(call.ArgInt("limit"), 1, 20, 5)
			tasks, total, err := deps.imports.ListTasks(ctx, call.User, call.SubjectID,
				model.ImportTaskStatus(call.ArgString("status")), 0, limit, 0)
			if err != nil {
				return aiagent.ToolResult{}, err
			}
			type item struct {
				ID      uint   `json:"id"`
				Title   string `json:"title"`
				Status  string `json:"status"`
				Saved   int    `json:"saved_count"`
				Total   int    `json:"total_count"`
				Failed  int    `json:"failed_count"`
				Message string `json:"error_message,omitempty"`
			}
			items := make([]item, 0, len(tasks))
			for i := range tasks {
				t := &tasks[i]
				items = append(items, item{
					ID:      t.ID,
					Title:   t.OriginalFilename,
					Status:  string(t.Status),
					Saved:   t.SavedCount,
					Total:   t.TotalCount,
					Failed:  t.FailedCount,
					Message: t.ErrorMsg,
				})
			}
			return marshalResult(map[string]any{"total": total, "items": items}), nil
		},
	})
}

// matchTargetQuestions 解析要归类的题目集合。
//
// 省略 question_ids 时取"当前学科下没有关联知识点的题目"——
// 那正是用户说「这些题还没归类」时指的那一批。靠 search_questions
// 逐页取再让模型自己数一遍，只会让它在第三页丢掉上下文。
func matchTargetQuestions(ctx context.Context, deps toolDeps, call aiagent.ToolCall) ([]uint, error) {
	if _, ok := call.Arguments["question_ids"]; ok {
		return batchIDList(call.Arguments, "question_ids")
	}
	onlyUnmatched := true
	if v, ok := call.Arguments["only_unmatched"]; ok {
		onlyUnmatched, _ = v.(bool)
	}
	if !onlyUnmatched {
		return nil, apperr.New(apperr.CodeInvalidParam,
			"不给 question_ids 时只能处理未关联知识点的题目；"+
				"要处理指定的几道题请显式给出 question_ids")
	}

	const scanLimit = 500
	briefs, _, err := deps.questions.List(ctx, call.User, call.SubjectID, ListQuery{RootOnly: true}, scanLimit, 0)
	if err != nil {
		return nil, err
	}
	ids := make([]uint, 0, len(briefs))
	for i := range briefs {
		if len(briefs[i].KnowledgePoints) == 0 {
			ids = append(ids, briefs[i].ID)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	return ids, nil
}
