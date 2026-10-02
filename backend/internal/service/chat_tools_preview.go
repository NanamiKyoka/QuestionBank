package service

import (
	"context"
	"strings"

	"github.com/NanamiKyoka/questionmind/internal/aiagent"
	"github.com/NanamiKyoka/questionmind/internal/importing"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
	"github.com/NanamiKyoka/questionmind/internal/schema"
)

// questionPreviewScenes 题目改动确认通道的可见场景。
//
// 与组卷的三个客户端工具不同，这里在**题库**与**录题审核**两处都可用：
// 两个场景的用户都在处理题目本身，而"改完先看一眼"的需求与他在哪个
// 页面无关。客户端工具必须在这些页面都注册了处理器才会被调用，
// 少注册一个的表现是模型收到一句"当前页面没有实现该工具"。
var questionPreviewScenes = []aiagent.Scene{
	aiagent.SceneQuestionLibrary,
	aiagent.SceneImportReview,
}

// maxQuestionEdits 单次 preview_question_edits 的操作数上限。
//
// 与 maxEditPaperOps 取同一个数量级：超过这个量，界面上那份差异清单
// 就没有人愿意逐条看了，而没被逐条核对的批量写入正是最该拦下的。
const maxQuestionEdits = 50

// registerQuestionPreviewTool 注册题目改动的确认通道。
//
// 它与 batch_* 工具的 dry_run 是两条不同的路，选哪条看用户在不在场：
//
//	dry_run         —— 无人在场或批量很大时用。只有 id 清单，没有渲染好的差异。
//	preview_...     —— 用户在题库页面前时用。逐条 before/after，用户点一次「应用」。
//
// 关键差别在"点一次"：dry_run 的预演与执行是两次独立的模型调用，
// 两次之间模型可能改了主意、用户可能又动了题库；而这里的预览与应用
// 是同一个用户手势，中间没有模型参与。
func registerQuestionPreviewTool(r *aiagent.Registry, deps toolDeps) {
	r.MustRegister(&aiagent.Tool{
		Name: "preview_question_edits",
		Description: "生成一批题目改动的预览，交给用户逐条过目后一次性应用。" +
			"这是用户**在题库页面上**时做批量整理的首选：改动会先显示成一份 before/after 清单，" +
			"用户点「应用」后才真正落库。" +
			"本工具立即返回预览结果，请据此告诉用户还有若干处改动等待确认，不要重复提交同一批。" +
			"可用操作：update（改题干/难度/来源/标签/知识点）、archive（归档）、delete（移入回收站）。" +
			"用户不在题库页面、或者只是想看看影响面时，改用 batch_* 工具的 dry_run。",
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
								"type":        "string",
								"enum":        []string{"update", "archive", "delete"},
								"description": "update 改字段，archive 归档，delete 移入回收站",
							},
							"question_id":         map[string]any{"type": "integer", "description": "题目 id"},
							"stem_markdown":       map[string]any{"type": "string", "description": "update 专用：新题干，Markdown 格式"},
							"difficulty":          map[string]any{"type": "integer", "minimum": 1, "maximum": 5, "description": "update 专用：新难度"},
							"source":              map[string]any{"type": "string", "description": "update 专用：新来源"},
							"tag_ids":             map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "update 专用：整份替换标签，传空数组清空"},
							"knowledge_point_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "update 专用：整份替换知识点，传空数组清空"},
							"change_note":         map[string]any{"type": "string", "description": "update 专用：改动理由，会记进审核日志"},
							"comment":             map[string]any{"type": "string", "description": "archive 专用：归档理由"},
						},
						"required": []string{"op", "question_id"},
					},
				},
			},
			"required": []string{"operations"},
		},
		Executor: aiagent.ExecutorClient,
		Scenes:   questionPreviewScenes,
		Prepare:  prepareQuestionEdits(deps),
	})
}

// prepareQuestionEdits 校验操作列表，并把每条操作的**改前值**一并下发。
//
// 把 before 塞进下发的参数里，是为了让前端不必为渲染差异再发一轮请求：
// 这批改动此刻要立刻显示出来，多一次往返就多一次"用户盯着空白等待"的时间。
//
// 同时这也是唯一一道闸：模型给出的每个字符串在进入前端之前都要先过
// 形状与长度校验，而每道题的 id 必须真实存在且当前用户可编辑。
func prepareQuestionEdits(deps toolDeps) aiagent.ToolPreparer {
	return func(ctx context.Context, call aiagent.ToolCall) (map[string]any, error) {
		raw, ok := call.Arguments["operations"]
		if !ok {
			return nil, apperr.New(apperr.CodeInvalidParam, "缺少 operations")
		}
		list, ok := raw.([]any)
		if !ok || len(list) == 0 {
			return nil, apperr.New(apperr.CodeInvalidParam, "operations 必须是非空数组")
		}
		if len(list) > maxQuestionEdits {
			return nil, apperr.New(apperr.CodeInvalidParam,
				"一次最多提交 50 处改动").WithDetail("实际 %d 处", len(list))
		}

		out := make([]map[string]any, 0, len(list))
		for i, item := range list {
			at := i + 1
			op, ok := item.(map[string]any)
			if !ok {
				return nil, apperr.New(apperr.CodeInvalidParam, "操作格式不正确").WithDetail("第 %d 个", at)
			}
			kind, _ := op["op"].(string)
			id := uint(callID(op["question_id"]))

			detail, err := deps.questions.Get(ctx, call.User, id)
			if err != nil {
				return nil, err
			}
			if err := checkPreviewPermission(detail, kind); err != nil {
				return nil, err
			}

			normalized := map[string]any{
				"op":          kind,
				"question_id": id,
				"before":      beforeSnapshot(detail),
			}

			switch kind {
			case "update":
				payload, err := previewUpdatePayload(op, at)
				if err != nil {
					return nil, err
				}
				normalized["payload"] = payload
			case "archive":
				comment, _ := op["comment"].(string)
				normalized["comment"] = truncateRunes(strings.TrimSpace(comment), maxSummaryRunes)
			case "delete":
				// 无附加参数
			default:
				return nil, apperr.New(apperr.CodeInvalidParam,
					"操作类型不受支持").WithDetail("第 %d 个 op=%q，应为 update/archive/delete", at, kind)
			}
			out = append(out, normalized)
		}

		prepared := map[string]any{"operations": out}
		if summary := truncateRunes(call.ArgString("summary"), maxSummaryRunes); summary != "" {
			prepared["summary"] = summary
		}
		return prepared, nil
	}
}

// checkPreviewPermission 拦下用户本来就无权执行的操作。
//
// 不在这里拦、推给前端再被 REST 接口拒绝，代价是用户白等一轮 30 秒
// 票据才看到一句"403"，而模型已经把这批改动说成"已生成预览"。
func checkPreviewPermission(d *schema.QuestionDetail, kind string) error {
	switch kind {
	case "update":
		if !d.Permissions.CanEdit {
			return apperr.New(apperr.CodeForbidden, "这道题当前不可编辑").
				WithDetail("题目 id=%d", d.ID)
		}
	case "archive", "delete":
		verb := "删除"
		if kind == "archive" {
			verb = "归档"
		}
		if !d.Permissions.CanDelete {
			return apperr.New(apperr.CodeForbidden, "没有"+verb+"这道题的权限").
				WithDetail("题目 id=%d", d.ID)
		}
	}
	return nil
}

// previewUpdatePayload 把一条 update 操作翻译成 HTTP 更新接口的载荷。
func previewUpdatePayload(op map[string]any, at int) (map[string]any, error) {
	payload := map[string]any{}
	touched := make([]string, 0, 5)

	if v, ok := op["stem_markdown"]; ok {
		md, _ := v.(string)
		md = strings.TrimSpace(md)
		if md == "" {
			return nil, apperr.New(apperr.CodeInvalidParam, "题干不能为空").WithDetail("第 %d 个", at)
		}
		if n := len([]rune(md)); n > maxContentRunes {
			return nil, apperr.New(apperr.CodeInvalidParam, "题干过长").WithDetail("第 %d 个", at)
		}
		payload["stem"] = importing.MarkdownToRichDoc(md)
		payload["stem_markdown"] = md
		touched = append(touched, "题干")
	}
	if v, ok := op["difficulty"]; ok {
		d, valid := intArg(v, 1, 5)
		if !valid {
			return nil, apperr.New(apperr.CodeInvalidParam, "difficulty 必须是 1 到 5 的整数").WithDetail("第 %d 个", at)
		}
		payload["difficulty"] = d
		touched = append(touched, "难度")
	}
	if v, ok := op["source"]; ok {
		source, _ := v.(string)
		source = strings.TrimSpace(source)
		if len([]rune(source)) > 255 {
			return nil, apperr.New(apperr.CodeInvalidParam, "来源过长").WithDetail("第 %d 个", at)
		}
		payload["source"] = source
		touched = append(touched, "来源")
	}
	if v, ok := op["tag_ids"]; ok {
		ids, err := previewIDList(v, "tag_ids", at)
		if err != nil {
			return nil, err
		}
		payload["tag_ids"] = ids
		touched = append(touched, "标签")
	}
	if v, ok := op["knowledge_point_ids"]; ok {
		ids, err := previewIDList(v, "knowledge_point_ids", at)
		if err != nil {
			return nil, err
		}
		payload["knowledge_point_ids"] = ids
		touched = append(touched, "知识点")
	}
	if v, ok := op["change_note"]; ok {
		note, _ := v.(string)
		payload["change_note"] = truncateRunes(strings.TrimSpace(note), 255)
	}

	if len(touched) == 0 {
		return nil, apperr.New(apperr.CodeInvalidParam,
			"update 没有给出任何要改的字段").WithDetail("第 %d 个", at)
	}
	payload["fields"] = touched
	return payload, nil
}

func previewIDList(raw any, label string, at int) ([]uint, error) {
	list, ok := raw.([]any)
	if !ok {
		return nil, apperr.New(apperr.CodeInvalidParam, label+" 必须是数组").WithDetail("第 %d 个", at)
	}
	out := make([]uint, 0, len(list))
	for i, item := range list {
		n, ok := item.(float64)
		if !ok || n <= 0 {
			return nil, apperr.New(apperr.CodeInvalidParam, label+" 含有无效的 id").
				WithDetail("第 %d 个的第 %d 项", at, i+1)
		}
		out = append(out, uint(n))
	}
	return out, nil
}

// beforeSnapshot 改前值。
//
// 字段名与前端 QuestionBrief 对齐：前端要拿它渲染差异，
// 不必为了显示「原来是什么」再拉一次详情。
func beforeSnapshot(d *schema.QuestionDetail) map[string]any {
	tags := make([]string, 0, len(d.Tags))
	for _, t := range d.Tags {
		tags = append(tags, t.Name)
	}
	points := make([]string, 0, len(d.KnowledgePoints))
	for _, k := range d.KnowledgePoints {
		points = append(points, k.Name)
	}
	return map[string]any{
		"stem":         truncateRunes(d.StemText, 200),
		"difficulty":   d.Difficulty,
		"source":       d.Source,
		"tags":         tags,
		"tag_ids":      tagIDs(d.Tags),
		"points":       points,
		"point_ids":    kpIDs(d.KnowledgePoints),
		"status":       string(d.Status),
		"status_label": d.StatusLabel,
	}
}

func tagIDs(tags []schema.TagBrief) []uint {
	out := make([]uint, 0, len(tags))
	for _, t := range tags {
		out = append(out, t.ID)
	}
	return out
}

func kpIDs(points []schema.KnowledgePointBrief) []uint {
	out := make([]uint, 0, len(points))
	for _, k := range points {
		out = append(out, k.ID)
	}
	return out
}

func callID(raw any) int {
	n, _ := raw.(float64)
	return int(n)
}
