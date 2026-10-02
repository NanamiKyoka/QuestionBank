package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/NanamiKyoka/questionmind/internal/aiagent"
	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
	"github.com/NanamiKyoka/questionmind/internal/schema"
)

// 批量写工具的可见场景。题库与录题审核页。
var batchScenes = []aiagent.Scene{
	aiagent.SceneQuestionLibrary,
	aiagent.SceneImportReview,
}

// maxBatchIDs 单次批量操作的题目数上限。
//
// 与 HTTP 层的 binding max=200 对齐。200 是"一次人工确认还看得过来"的上限：
// 再多就没有人会逐条核对，而没被核对的批量写入正是最需要拦住的。
const maxBatchIDs = 200

// dryRunHint 预演结果的统一后缀。
const dryRunHint = "这是预演：数据库尚未改动。确认无误后用 dry_run=false 再执行一次。"

// registerBatchTools 注册批量写工具。
//
// 每一个都带 dry_run 参数，且**省略等同于 true**。
// 这是"让助手直接动手"与"不让它闯祸"之间唯一的分界：模型可以先算出
// 影响面，再决定要不要落笔；而漏写参数落到的分支是"先别动"。
//
// dry_run 的结果只是**预演**，真正执行时以执行结果为准——
// 两次调用之间用户可能又改了题库。
func registerBatchTools(r *aiagent.Registry, deps toolDeps) {
	r.MustRegister(&aiagent.Tool{
		Name: "batch_update_questions",
		Description: "对一批题目应用同一组修改。整理题库的主力工具：" +
			"用户说「把这一批题的难度都改成 4」「这几道题的来源补上」时使用。" +
			"**动手前先用 dry_run=true 预演一次**，把影响面告诉用户并得到确认，再以 dry_run=false 执行。" +
			"逐条独立处理，某一条不满足条件只会跳过它，不影响其余。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"question_ids": map[string]any{
					"type": "array", "items": map[string]any{"type": "integer"},
					"description": "题目 id，取自 search_questions",
				},
				"changes":     questionChangesSchema(),
				"change_note": map[string]any{"type": "string", "description": "改动理由，会记进审核日志"},
				"dry_run":     map[string]any{"type": "boolean", "description": "true 只预演不写入，省略等同于 true"},
			},
			"required": []string{"question_ids", "changes"},
		},
		Executor:   aiagent.ExecutorServer,
		Mutating:   true,
		Capability: "question.batch_update",
		Scenes:     batchScenes,
		Handler: func(ctx context.Context, call aiagent.ToolCall) (aiagent.ToolResult, error) {
			ids, err := batchIDList(call.Arguments, "question_ids")
			if err != nil {
				return aiagent.ErrorResult("%s", err.Error()), nil
			}
			rawChanges, _ := call.Arguments["changes"].(map[string]any)
			req, err := buildQuestionUpdate(rawChanges, call.ArgString("change_note"))
			if err != nil {
				return aiagent.ErrorResult("%s", err.Error()), nil
			}
			fields := describeChanges(rawChanges)

			if call.ArgBoolOr("dry_run", true) {
				plan, err := planQuestionBatch(ctx, deps, call, ids,
					func(d *schemaDetail) (string, string) {
						if !d.Permissions.CanEdit {
							return "blocked", "没有编辑权限"
						}
						return "affected", ""
					})
				if err != nil {
					return aiagent.ToolResult{}, err
				}
				plan["fields"] = fields
				plan["hint"] = dryRunHint
				return marshalResult(plan), nil
			}

			updated := make([]uint, 0, len(ids))
			failed := make([]blockedItem, 0)
			for _, id := range ids {
				if _, err := deps.questions.Update(ctx, call.User, id, req); err != nil {
					failed = append(failed, blockedItem{ID: id, Reason: shortError(err)})
					continue
				}
				updated = append(updated, id)
			}
			return marshalResult(map[string]any{
				"dry_run":  false,
				"total":    len(ids),
				"affected": updated,
				"blocked":  failed,
				"fields":   fields,
			}), nil
		},
	})

	r.MustRegister(&aiagent.Tool{
		Name: "batch_review_questions",
		Description: "对一批题目做状态流转：submit 提交审核、approve 通过、reject 驳回。" +
			"approve 达到学科复核阈值后题目才会发布，没达到的仍留在待审。" +
			"**动手前先用 dry_run=true 预演一次**。用户说「这批都过了」「剩下几道都提交」时使用。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"question_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
				"action": map[string]any{
					"type": "string", "enum": []string{"submit", "approve", "reject"},
					"description": "submit 提交审核，approve 通过，reject 驳回",
				},
				"comment": map[string]any{"type": "string", "description": "审核意见，可省略"},
				"dry_run": map[string]any{"type": "boolean", "description": "true 只预演不写入，省略等同于 true"},
			},
			"required": []string{"question_ids", "action"},
		},
		Executor:   aiagent.ExecutorServer,
		Mutating:   true,
		Capability: "question.batch_review",
		Scenes:     batchScenes,
		Handler: func(ctx context.Context, call aiagent.ToolCall) (aiagent.ToolResult, error) {
			ids, err := batchIDList(call.Arguments, "question_ids")
			if err != nil {
				return aiagent.ErrorResult("%s", err.Error()), nil
			}
			action := strings.TrimSpace(call.ArgString("action"))
			switch action {
			case "submit", "approve", "reject":
			default:
				return aiagent.ErrorResult("action 只支持 submit / approve / reject，实际 %q", action), nil
			}
			dry := call.ArgBoolOr("dry_run", true)

			if dry {
				plan, err := planQuestionBatch(ctx, deps, call, ids, func(d *schemaDetail) (string, string) {
					return reviewPrecondition(d, action)
				})
				if err != nil {
					return aiagent.ToolResult{}, err
				}
				plan["action"] = action
				plan["hint"] = dryRunHint
				return marshalResult(plan), nil
			}

			handled, skipped, err := deps.questions.BatchReview(ctx, call.User, ids, action, call.ArgString("comment"))
			if err != nil {
				return aiagent.ToolResult{}, err
			}
			return marshalResult(map[string]any{
				"dry_run":  false,
				"action":   action,
				"total":    len(ids),
				"affected": handled,
				"blocked":  skipped,
			}), nil
		},
	})

	r.MustRegister(&aiagent.Tool{
		Name: "batch_archive_questions",
		Description: "批量归档或取消归档题目。只有已发布的题能被归档。" +
			"**动手前先用 dry_run=true 预演一次**。" +
			"用户说「把这几道过时的题收起来」时使用。归档不影响已引用它们的试卷。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"question_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
				"archived":     map[string]any{"type": "boolean", "description": "true 归档，false 取消归档"},
				"comment":      map[string]any{"type": "string", "description": "归档理由，可省略"},
				"dry_run":      map[string]any{"type": "boolean", "description": "true 只预演不写入，省略等同于 true"},
			},
			"required": []string{"question_ids", "archived"},
		},
		Executor:   aiagent.ExecutorServer,
		Mutating:   true,
		Capability: "question.batch_archive",
		Scenes:     batchScenes,
		Handler: func(ctx context.Context, call aiagent.ToolCall) (aiagent.ToolResult, error) {
			ids, err := batchIDList(call.Arguments, "question_ids")
			if err != nil {
				return aiagent.ErrorResult("%s", err.Error()), nil
			}
			archived := call.ArgBool("archived")
			comment := call.ArgString("comment")

			if call.ArgBoolOr("dry_run", true) {
				plan, err := planQuestionBatch(ctx, deps, call, ids, func(d *schemaDetail) (string, string) {
					return archivePrecondition(d, archived)
				})
				if err != nil {
					return aiagent.ToolResult{}, err
				}
				plan["archived"] = archived
				plan["hint"] = dryRunHint
				return marshalResult(plan), nil
			}

			affected := make([]uint, 0, len(ids))
			blocked := make([]blockedItem, 0)
			for _, id := range ids {
				if _, err := deps.questions.SetArchived(ctx, call.User, id, archived, comment); err != nil {
					blocked = append(blocked, blockedItem{ID: id, Reason: shortError(err)})
					continue
				}
				affected = append(affected, id)
			}
			return marshalResult(map[string]any{
				"dry_run":  false,
				"total":    len(ids),
				"affected": affected,
				"blocked":  blocked,
			}), nil
		},
	})

	r.MustRegister(&aiagent.Tool{
		Name: "batch_delete_questions",
		Description: "批量把题目移入回收站（软删除），子题会一并删除，可在回收站恢复。" +
			"**动手前先用 dry_run=true 预演一次并把清单念给用户听**——这是破坏性最强的工具，" +
			"用户只说「清理一下」这类模糊的话时，先问清是不是真的要删。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"question_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
				"dry_run":      map[string]any{"type": "boolean", "description": "true 只预演不写入，省略等同于 true"},
			},
			"required": []string{"question_ids"},
		},
		Executor:   aiagent.ExecutorServer,
		Mutating:   true,
		Capability: "question.batch_delete",
		Scenes:     batchScenes,
		Handler: func(ctx context.Context, call aiagent.ToolCall) (aiagent.ToolResult, error) {
			ids, err := batchIDList(call.Arguments, "question_ids")
			if err != nil {
				return aiagent.ErrorResult("%s", err.Error()), nil
			}

			if call.ArgBoolOr("dry_run", true) {
				plan, err := planQuestionBatch(ctx, deps, call, ids, func(d *schemaDetail) (string, string) {
					if !d.Permissions.CanDelete {
						return "blocked", "没有删除权限"
					}
					if len(d.Children) > 0 {
						return "blocked", fmt.Sprintf("含 %d 道子题，会被一并删除", len(d.Children))
					}
					return "affected", ""
				})
				if err != nil {
					return aiagent.ToolResult{}, err
				}
				plan["hint"] = dryRunHint + " 请把 affected 里的题目念给用户确认后再执行。"
				return marshalResult(plan), nil
			}

			deleted, skipped, err := deps.questions.Delete(ctx, call.User, ids)
			if err != nil {
				return aiagent.ToolResult{}, err
			}
			return marshalResult(map[string]any{
				"dry_run":  false,
				"total":    len(ids),
				"affected": deleted,
				"blocked":  skipped,
				"hint":     "题目已进入回收站，用户可以在回收站页面上恢复。",
			}), nil
		},
	})

	r.MustRegister(&aiagent.Tool{
		Name: "batch_set_source",
		Description: "批量设置题目的来源字段。用户说「这几道题都是 2024 年的，来源补上」时使用。" +
			"**动手前先用 dry_run=true 预演一次**。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"question_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
				"source":       map[string]any{"type": "string", "description": "新的来源，如「2024 全国甲卷」"},
				"dry_run":      map[string]any{"type": "boolean", "description": "true 只预演不写入，省略等同于 true"},
			},
			"required": []string{"question_ids", "source"},
		},
		Executor:   aiagent.ExecutorServer,
		Mutating:   true,
		Capability: "question.batch_set_source",
		Scenes:     batchScenes,
		Handler: func(ctx context.Context, call aiagent.ToolCall) (aiagent.ToolResult, error) {
			ids, err := batchIDList(call.Arguments, "question_ids")
			if err != nil {
				return aiagent.ErrorResult("%s", err.Error()), nil
			}
			source := strings.TrimSpace(call.ArgString("source"))
			if source == "" {
				return aiagent.ErrorResult("source 不能为空"), nil
			}
			if len([]rune(source)) > 255 {
				return aiagent.ErrorResult("source 过长"), nil
			}

			if call.ArgBoolOr("dry_run", true) {
				plan, err := planQuestionBatch(ctx, deps, call, ids, func(d *schemaDetail) (string, string) {
					if !d.Permissions.CanEdit {
						return "blocked", "没有编辑权限"
					}
					if d.Source == source {
						return "blocked", "来源已经是这个值"
					}
					return "affected", ""
				})
				if err != nil {
					return aiagent.ToolResult{}, err
				}
				plan["source"] = source
				plan["hint"] = dryRunHint
				return marshalResult(plan), nil
			}

			updated, skipped, err := deps.questions.BatchUpdateSource(ctx, call.User, ids, source)
			if err != nil {
				return aiagent.ToolResult{}, err
			}
			return marshalResult(map[string]any{
				"dry_run":  false,
				"total":    len(ids),
				"affected": updated,
				"blocked":  skipped,
			}), nil
		},
	})
}

// ---- 预演 ----

// schemaDetail 是预演读到的题目详情。
type schemaDetail = schema.QuestionDetail

// blockedItem 一条被拦下的题目。
type blockedItem struct {
	ID     uint   `json:"id"`
	Reason string `json:"reason"`
}

// planQuestionBatch 读出每道题的现状并逐条判定。
//
// classify 返回 ("affected", "") 或 ("blocked", 原因)。
// 判定基于 Get 返回的 Permissions 与当前状态——权限由 service 层算好，
// 这里不重复实现任何鉴权逻辑。
//
// 注意这是**预演**：两次调用之间用户可能又改了题库，
// 因此真正执行时以执行结果为准，预演只用来决定"值不值得动手"。
func planQuestionBatch(
	ctx context.Context,
	deps toolDeps,
	call aiagent.ToolCall,
	ids []uint,
	classify func(*schemaDetail) (string, string),
) (map[string]any, error) {
	affected := make([]uint, 0, len(ids))
	blocked := make([]blockedItem, 0)
	skipped := make([]blockedItem, 0)

	for _, id := range ids {
		detail, err := deps.questions.Get(ctx, call.User, id)
		if err != nil {
			reason := shortError(err)
			if apperr.Is(err, apperr.CodeNotFound) {
				skipped = append(skipped, blockedItem{ID: id, Reason: reason})
			} else {
				blocked = append(blocked, blockedItem{ID: id, Reason: reason})
			}
			continue
		}
		verdict, reason := classify(detail)
		if verdict == "blocked" {
			blocked = append(blocked, blockedItem{ID: id, Reason: reason})
			continue
		}
		affected = append(affected, id)
	}

	plan := map[string]any{
		"dry_run":  true,
		"total":    len(ids),
		"affected": affected,
	}
	if len(blocked) > 0 {
		plan["blocked"] = blocked
	}
	if len(skipped) > 0 {
		plan["missing"] = skipped
	}
	return plan, nil
}

// reviewPrecondition 判定一条审核动作的前置条件。
func reviewPrecondition(d *schemaDetail, action string) (string, string) {
	switch action {
	case "submit":
		if !d.Permissions.CanEdit {
			return "blocked", "没有编辑权限"
		}
		if d.Status != model.QuestionStatusDraft {
			return "blocked", "当前状态不是草稿（" + d.Status.Label() + "），只有草稿能提交审核"
		}
		return "affected", ""
	default:
		if !d.Permissions.CanReview {
			return "blocked", "没有审核权限"
		}
		if d.Status != model.QuestionStatusDraft && d.Status != model.QuestionStatusPending {
			return "blocked", "当前状态是" + d.Status.Label() + "，无需审核"
		}
		return "affected", ""
	}
}

// archivePrecondition 判定一条归档动作的前置条件。
func archivePrecondition(d *schemaDetail, archived bool) (string, string) {
	if archived {
		if d.Status != model.QuestionStatusPublished {
			return "blocked", "当前状态是" + d.Status.Label() + "，只有已发布的题才能归档"
		}
		return "affected", ""
	}
	if d.Status != model.QuestionStatusArchived {
		return "blocked", "当前状态不是归档"
	}
	return "affected", ""
}

// batchIDList 读批量操作的题目 id。
//
// 去重但不排序：保序让「预演念出来的清单」与用户挑题的顺序一致，
// 这在删除这类不可逆操作上是值得的。
func batchIDList(args map[string]any, key string) ([]uint, error) {
	raw, ok := args[key]
	if !ok {
		return nil, apperr.New(apperr.CodeInvalidParam, "缺少 "+key)
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, apperr.New(apperr.CodeInvalidParam, key+" 必须是数组")
	}
	if len(list) == 0 {
		return nil, apperr.New(apperr.CodeInvalidParam, key+" 不能为空")
	}
	if len(list) > maxBatchIDs {
		return nil, apperr.New(apperr.CodeInvalidParam,
			fmt.Sprintf("一次最多处理 %d 道题", maxBatchIDs)).
			WithDetail("实际 %d 道，请分批处理", len(list))
	}

	seen := make(map[uint]struct{}, len(list))
	out := make([]uint, 0, len(list))
	for i, item := range list {
		n, ok := item.(float64)
		if !ok || n <= 0 {
			return nil, apperr.New(apperr.CodeInvalidParam, key+" 含有无效的 id").
				WithDetail("第 %d 项", i+1)
		}
		id := uint(n)
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out, nil
}

// shortError 把错误压成一句能回喂给模型的短文本。
//
// 直接把 apperr 的完整错误回喂会连带上链路的 cause 与堆栈片段，
// 既占上下文也会把模型引去读一堆与它无关的内部细节。
func shortError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if i := strings.Index(msg, ": "); i > 0 && i < 60 {
		// apperr 的错误串形如「状态冲突: 只有草稿才能提交审核」。
		if j := strings.LastIndex(msg, ": "); j > i {
			msg = msg[j+2:]
		}
	}
	if len([]rune(msg)) > 120 {
		msg = string([]rune(msg)[:120]) + "…"
	}
	return msg
}
