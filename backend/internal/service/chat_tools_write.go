package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/NanamiKyoka/questionmind/internal/aiagent"
	"github.com/NanamiKyoka/questionmind/internal/importing"
	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
	"github.com/NanamiKyoka/questionmind/internal/schema"
)

// 单条改动的可见场景。
//
// 刻意不给组卷场景：编辑器里的用户改的是**试卷**，题库是另一处数据。
// 两边的写操作混在一个会话里，模型很容易把「改了题库」说成「卷子里的
// 那道题也改了」——它其实只改了前者。
var writeScenes = []aiagent.Scene{
	aiagent.SceneQuestionLibrary,
	aiagent.SceneImportReview,
}

// maxContentRunes 单个文本字段的字符数上限。
//
// 和 prepareEditPaper 的 maxInsertContentRunes 同源：超长基本可以断定
// 是模型把整篇参考资料灌了进来（提示注入的典型表现），直接拦掉。
const maxContentRunes = 20000

// registerQuestionWriteTools 注册单条写工具。
//
// 全部走 QuestionService，因此权限、状态机、题型与答案的一致性校验
// 与用户在界面上点「保存」完全一致——不存在"从对话进来能绕过校验"的可能。
func registerQuestionWriteTools(r *aiagent.Registry, deps toolDeps) {
	r.MustRegister(&aiagent.Tool{
		Name: "update_question",
		Description: "修改一道题的题干、选项、答案、难度、题型、来源或解析。" +
			"只改动 parameters 里实际给出的字段，没给的保持原样。" +
			"选项与答案是**整份替换**而不是逐项合并：改选项时必须把现有选项一并给出。" +
			"用户说「这道题难度标错了」「把答案改成 B」「补一段解析」时使用。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"question_id": map[string]any{
					"type": "integer", "description": "题目 id，取自 search_questions 或 get_question",
				},
				"change_note": map[string]any{
					"type":        "string",
					"description": "一句话说明这次改动的理由，会记进该题的审核日志",
				},
				"changes": questionChangesSchema(),
			},
			"required": []string{"question_id", "changes"},
		},
		Executor:   aiagent.ExecutorServer,
		Mutating:   true,
		Capability: "question.update",
		Scenes:     writeScenes,
		Handler: func(ctx context.Context, call aiagent.ToolCall) (aiagent.ToolResult, error) {
			id := uint(call.ArgInt("question_id"))
			if id == 0 {
				return aiagent.ErrorResult("缺少 question_id"), nil
			}
			raw, _ := call.Arguments["changes"].(map[string]any)
			req, err := buildQuestionUpdate(raw, call.ArgString("change_note"))
			if err != nil {
				return aiagent.ErrorResult("%s", err.Error()), nil
			}
			detail, err := deps.questions.Update(ctx, call.User, id, req)
			if err != nil {
				return aiagent.ToolResult{}, err
			}
			return marshalResult(map[string]any{
				"id":               detail.ID,
				"changed":          describeChanges(raw),
				"difficulty":       detail.Difficulty,
				"status":           string(detail.Status),
				"content_revision": detail.ContentRevision,
				"hint": "内容改动会让引用了这道题的试卷提示「题目已更新」，用户可在界面上同步。" +
					"如果这道题出现在用户正在编辑的试卷里，请改用 add_questions_to_paper 之外的说明告知用户手动同步。",
			}), nil
		},
	})

	// 归类拆成两个独立工具而不是 update_question 的两个字段：
	// 「把这批题归到某个知识点」是整理场景里最高频的诉求，
	// 混在一个大而全的更新工具里，模型经常只改了一半。
	r.MustRegister(&aiagent.Tool{
		Name: "set_question_knowledge_points",
		Description: "设置一道题关联的知识点，**整份替换**现有关联。" +
			"用户说「把这两道题归到导数下面」「这几道题关联错了」时使用。" +
			"知识点 id 取自 list_knowledge_points 或 semantic_search_knowledge_points。" +
			"传空数组表示清空关联。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"question_id":         map[string]any{"type": "integer", "description": "题目 id"},
				"knowledge_point_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
			},
			"required": []string{"question_id", "knowledge_point_ids"},
		},
		Executor:   aiagent.ExecutorServer,
		Mutating:   true,
		Capability: "question.knowledge_points.set",
		Scenes:     writeScenes,
		Handler: func(ctx context.Context, call aiagent.ToolCall) (aiagent.ToolResult, error) {
			id := uint(call.ArgInt("question_id"))
			if id == 0 {
				return aiagent.ErrorResult("缺少 question_id"), nil
			}
			ids, err := explicitIDList(call.Arguments, "knowledge_point_ids", "knowledge_point_ids")
			if err != nil {
				return aiagent.ErrorResult("%s", err.Error()), nil
			}
			detail, err := deps.questions.Update(ctx, call.User, id, &schema.QuestionUpdateRequest{
				KnowledgePointIDs: &ids,
			})
			if err != nil {
				return aiagent.ToolResult{}, err
			}
			names := make([]string, 0, len(detail.KnowledgePoints))
			for _, kp := range detail.KnowledgePoints {
				names = append(names, kp.Name)
			}
			return marshalResult(map[string]any{
				"id":               detail.ID,
				"knowledge_points": names,
			}), nil
		},
	})

	r.MustRegister(&aiagent.Tool{
		Name: "set_question_tags",
		Description: "设置一道题的标签，**整份替换**现有标签。" +
			"用户说「给这几道题打上「重点」标签」时使用。" +
			"标签 id 取自 list_tags。传空数组表示清空标签。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"question_id": map[string]any{"type": "integer", "description": "题目 id"},
				"tag_ids":     map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
			},
			"required": []string{"question_id", "tag_ids"},
		},
		Executor:   aiagent.ExecutorServer,
		Mutating:   true,
		Capability: "question.tags.set",
		Scenes:     writeScenes,
		Handler: func(ctx context.Context, call aiagent.ToolCall) (aiagent.ToolResult, error) {
			id := uint(call.ArgInt("question_id"))
			if id == 0 {
				return aiagent.ErrorResult("缺少 question_id"), nil
			}
			ids, err := explicitIDList(call.Arguments, "tag_ids", "tag_ids")
			if err != nil {
				return aiagent.ErrorResult("%s", err.Error()), nil
			}
			detail, err := deps.questions.Update(ctx, call.User, id, &schema.QuestionUpdateRequest{
				TagIDs: &ids,
			})
			if err != nil {
				return aiagent.ToolResult{}, err
			}
			names := make([]string, 0, len(detail.Tags))
			for _, tag := range detail.Tags {
				names = append(names, tag.Name)
			}
			return marshalResult(map[string]any{"id": detail.ID, "tags": names}), nil
		},
	})

	r.MustRegister(&aiagent.Tool{
		Name: "create_question",
		Description: "在当前学科下新建一道题。用户说「加一道题」「把这道题录进来」时使用。" +
			"有审核权限的用户录入后直接发布，普通编辑录入的是草稿，需要再调用 submit_question。" +
			"题干与选项用 Markdown 写，公式写成 $...$。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"stem_markdown":       map[string]any{"type": "string", "description": "题干，Markdown 格式"},
				"question_type":       map[string]any{"type": "string", "enum": []string{"single_choice", "multiple_choice", "fill_blank", "true_false", "essay"}},
				"difficulty":          map[string]any{"type": "integer", "minimum": 1, "maximum": 5},
				"options":             optionsSchema(),
				"answer":              answerSchema(),
				"analysis_markdown":   map[string]any{"type": "string", "description": "解析，Markdown 格式"},
				"source":              map[string]any{"type": "string", "description": "来源，如「2024 全国甲卷」"},
				"tag_ids":             map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
				"knowledge_point_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
			},
			"required": []string{"stem_markdown", "question_type"},
		},
		Executor:   aiagent.ExecutorServer,
		Mutating:   true,
		Capability: "question.create",
		Scenes:     writeScenes,
		Handler: func(ctx context.Context, call aiagent.ToolCall) (aiagent.ToolResult, error) {
			stem, err := markdownArg(call.Arguments, "stem_markdown", true)
			if err != nil {
				return aiagent.ErrorResult("%s", err.Error()), nil
			}
			qtype := model.QuestionType(call.ArgString("question_type"))
			if !qtype.Valid() {
				return aiagent.ErrorResult("question_type 不受支持，实际 %q", call.ArgString("question_type")), nil
			}

			analysis, err := markdownArg(call.Arguments, "analysis_markdown", false)
			if err != nil {
				return aiagent.ErrorResult("%s", err.Error()), nil
			}
			options, err := buildOptions(call.Arguments["options"])
			if err != nil {
				return aiagent.ErrorResult("%s", err.Error()), nil
			}
			answer, err := buildAnswer(call.Arguments["answer"])
			if err != nil {
				return aiagent.ErrorResult("%s", err.Error()), nil
			}

			req := &schema.QuestionCreateRequest{
				SubjectID:         call.SubjectID,
				QuestionType:      qtype,
				Stem:              importing.MarkdownToRichDoc(stem),
				Options:           options,
				TagIDs:            call.ArgUintSlice("tag_ids"),
				KnowledgePointIDs: call.ArgUintSlice("knowledge_point_ids"),
				Source:            strings.TrimSpace(call.ArgString("source")),
			}
			if d := call.ArgInt("difficulty"); d >= 1 && d <= 5 {
				req.Difficulty = &d
			}
			if analysis != "" {
				req.Analysis = importing.MarkdownToRichDoc(analysis)
			}
			if answer != nil {
				req.Answer = *answer
			}

			detail, err := deps.questions.Create(ctx, call.User, req)
			if err != nil {
				return aiagent.ToolResult{}, err
			}
			hint := "题目已入库。"
			if detail.Status == model.QuestionStatusDraft {
				hint = "题目已入库，当前是草稿状态，需要提交审核后才会发布。"
			}
			return marshalResult(map[string]any{
				"id":     detail.ID,
				"status": string(detail.Status),
				"hint":   hint,
			}), nil
		},
	})

	registerQuestionStateTools(r, deps)
}

// registerQuestionStateTools 注册状态流转工具。
//
// 拆成独立工具而不是 update_question 的一个参数：状态机有前置条件
// （草稿→待审→已发布），混在一个工具里模型会尝试直接跳状态。
func registerQuestionStateTools(r *aiagent.Registry, deps toolDeps) {
	r.MustRegister(&aiagent.Tool{
		Name:        "submit_question",
		Description: "把草稿或被驳回的题目提交审核。用户说「这几道题我改好了，帮我提交」时使用。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"question_id": map[string]any{"type": "integer"},
			},
			"required": []string{"question_id"},
		},
		Executor:   aiagent.ExecutorServer,
		Mutating:   true,
		Capability: "question.submit",
		Scenes:     writeScenes,
		Handler: func(ctx context.Context, call aiagent.ToolCall) (aiagent.ToolResult, error) {
			id := uint(call.ArgInt("question_id"))
			if id == 0 {
				return aiagent.ErrorResult("缺少 question_id"), nil
			}
			detail, err := deps.questions.Submit(ctx, call.User, id)
			if err != nil {
				return aiagent.ToolResult{}, err
			}
			return marshalResult(map[string]any{
				"id":     detail.ID,
				"status": string(detail.Status),
			}), nil
		},
	})

	r.MustRegister(&aiagent.Tool{
		Name: "archive_question",
		Description: "归档或取消归档一道题。归档后题目仍留在题库里，只是不再参与检索与组卷。" +
			"用户说「这几道题过时了，先收起来」时使用。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"question_id": map[string]any{"type": "integer"},
				"archived":    map[string]any{"type": "boolean", "description": "true 归档，false 取消归档"},
				"comment":     map[string]any{"type": "string", "description": "归档理由，可省略"},
			},
			"required": []string{"question_id", "archived"},
		},
		Executor:   aiagent.ExecutorServer,
		Mutating:   true,
		Capability: "question.archive",
		Scenes:     writeScenes,
		Handler: func(ctx context.Context, call aiagent.ToolCall) (aiagent.ToolResult, error) {
			id := uint(call.ArgInt("question_id"))
			if id == 0 {
				return aiagent.ErrorResult("缺少 question_id"), nil
			}
			detail, err := deps.questions.SetArchived(ctx, call.User, id, call.ArgBool("archived"), call.ArgString("comment"))
			if err != nil {
				return aiagent.ToolResult{}, err
			}
			return marshalResult(map[string]any{"id": detail.ID, "status": string(detail.Status)}), nil
		},
	})

	r.MustRegister(&aiagent.Tool{
		Name: "delete_question",
		Description: "把题目移入回收站（软删除），子题会一并删除。" +
			"用户明确要求删除时使用；只是想暂时收起来请用 archive_question。" +
			"删除可以在回收站里恢复。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"question_id": map[string]any{"type": "integer"},
			},
			"required": []string{"question_id"},
		},
		Executor:   aiagent.ExecutorServer,
		Mutating:   true,
		Capability: "question.delete",
		Scenes:     writeScenes,
		Handler: func(ctx context.Context, call aiagent.ToolCall) (aiagent.ToolResult, error) {
			id := uint(call.ArgInt("question_id"))
			if id == 0 {
				return aiagent.ErrorResult("缺少 question_id"), nil
			}
			deleted, skipped, err := deps.questions.Delete(ctx, call.User, []uint{id})
			if err != nil {
				return aiagent.ToolResult{}, err
			}
			if len(deleted) == 0 {
				return aiagent.ErrorResult("这道题没有删除（可能没有删除权限或已被删除）"), nil
			}
			return marshalResult(map[string]any{
				"deleted": deleted,
				"skipped": skipped,
				"hint":    "题目已进入回收站，用户可以在回收站页面上恢复。",
			}), nil
		},
	})

	r.MustRegister(&aiagent.Tool{
		Name:        "restore_question",
		Description: "从回收站恢复一道被删除的题目。用户说「刚才那几道题删错了」时使用。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"question_id": map[string]any{"type": "integer"},
			},
			"required": []string{"question_id"},
		},
		Executor:   aiagent.ExecutorServer,
		Mutating:   true,
		Capability: "question.restore",
		Scenes:     writeScenes,
		Handler: func(ctx context.Context, call aiagent.ToolCall) (aiagent.ToolResult, error) {
			id := uint(call.ArgInt("question_id"))
			if id == 0 {
				return aiagent.ErrorResult("缺少 question_id"), nil
			}
			if err := deps.questions.Restore(ctx, call.User, id); err != nil {
				return aiagent.ToolResult{}, err
			}
			detail, err := deps.questions.Get(ctx, call.User, id)
			if err != nil {
				return aiagent.ToolResult{}, err
			}
			return marshalResult(map[string]any{"id": detail.ID, "status": string(detail.Status)}), nil
		},
	})
}

// ---- 题目内容参数的共用解析 ----

// questionChangesSchema 题目可改字段的 schema。
//
// update_question 与 batch_update_questions 共用同一份：两者的语义完全
// 相同（都是"只改给出的字段"），分开定义迟早会漂移。
func questionChangesSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"stem_markdown": map[string]any{
				"type":        "string",
				"description": "新题干，Markdown 格式，公式写成 $...$。省略表示不改题干。",
			},
			"options": withDescription(optionsSchema(),
				"新的选项列表（**整份替换**）。省略表示不改选项。"),
			"answer": withDescription(answerSchema(),
				"新的答案（**整份替换**）。省略表示不改答案。"),
			"analysis_markdown": map[string]any{
				"type":        "string",
				"description": "解析，Markdown 格式。省略表示不改。",
			},
			"difficulty":    map[string]any{"type": "integer", "minimum": 1, "maximum": 5},
			"question_type": map[string]any{"type": "string", "enum": []string{"single_choice", "multiple_choice", "fill_blank", "true_false", "essay"}},
			"source":        map[string]any{"type": "string", "description": "来源。省略表示不改。"},
		},
	}
}

func optionsSchema() map[string]any {
	return map[string]any{
		"type":        "array",
		"description": "选项列表，label 用 A/B/C/D。",
		"items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"label":         map[string]any{"type": "string", "description": "选项标号，如 A"},
				"text_markdown": map[string]any{"type": "string", "description": "选项内容，Markdown 格式"},
			},
			"required": []string{"label", "text_markdown"},
		},
	}
}

func answerSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"description": "答案。kind 决定用哪个字段：single/multiple 用 correct（选项标号数组），" +
			"true_false 用 bool_answer，fill_blank 用 blanks，essay 用 reference_markdown。",
		"properties": map[string]any{
			"kind": map[string]any{
				"type": "string",
				"enum": []string{"single", "multiple", "boolean", "blanks", "reference"},
			},
			"correct": map[string]any{
				"type": "array", "items": map[string]any{"type": "string"},
				"description": "正确的选项标号，single/multiple 用",
			},
			"bool_answer":        map[string]any{"type": "boolean", "description": "判断题答案"},
			"reference_markdown": map[string]any{"type": "string", "description": "解答题参考答案，Markdown 格式"},
			"blanks": map[string]any{
				"type":        "array",
				"description": "填空题各空的答案，顺序与题干中的空一致",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"accepts": map[string]any{
							"type":        "array",
							"description": "每个空的可接受答案，格式 [[\"3\",\"三\"]]：外层任选其一，内层按序对应。",
							"items": map[string]any{
								"type":  "array",
								"items": map[string]any{"type": "string"},
							},
						},
					},
					"required": []string{"accepts"},
				},
			},
		},
		"required": []string{"kind"},
	}
}

func withDescription(schema map[string]any, desc string) map[string]any {
	schema["description"] = desc
	return schema
}

// buildQuestionUpdate 把工具参数构造为一次局部更新。
//
// 指针语义由"参数给没给"决定：没给就是 nil，而 QuestionService.Update
// 对 nil 的解释是"保持原样"。因此不需要在这里比对当前值——
// 「改成它已经是的值」是一次没有副作用的写入，不值得拦。
func buildQuestionUpdate(args map[string]any, changeNote string) (*schema.QuestionUpdateRequest, error) {
	if len(args) == 0 {
		return nil, apperr.New(apperr.CodeInvalidParam, "changes 是空的，没有任何要改的字段")
	}

	req := &schema.QuestionUpdateRequest{ChangeNote: strings.TrimSpace(changeNote)}

	if _, ok := args["stem_markdown"]; ok {
		stem, err := markdownArg(args, "stem_markdown", true)
		if err != nil {
			return nil, err
		}
		req.Stem = importing.MarkdownToRichDoc(stem)
	}
	if _, ok := args["analysis_markdown"]; ok {
		analysis, err := markdownArg(args, "analysis_markdown", false)
		if err != nil {
			return nil, err
		}
		req.Analysis = importing.MarkdownToRichDoc(analysis)
	}
	if raw, ok := args["options"]; ok {
		options, err := buildOptions(raw)
		if err != nil {
			return nil, err
		}
		req.Options = &options
	}
	if raw, ok := args["answer"]; ok {
		answer, err := buildAnswer(raw)
		if err != nil {
			return nil, err
		}
		if answer == nil {
			return nil, apperr.New(apperr.CodeInvalidParam, "answer 不是有效对象")
		}
		req.Answer = answer
	}
	if v, ok := args["difficulty"]; ok {
		d, valid := intArg(v, 1, 5)
		if !valid {
			return nil, apperr.New(apperr.CodeInvalidParam, "difficulty 必须是 1 到 5 的整数")
		}
		req.Difficulty = &d
	}
	if v, ok := args["question_type"]; ok {
		qtype, _ := v.(string)
		if !model.QuestionType(qtype).Valid() {
			return nil, apperr.New(apperr.CodeInvalidParam, "question_type 不受支持").
				WithDetail("实际 %q", qtype)
		}
		t := model.QuestionType(qtype)
		req.QuestionType = &t
	}
	if v, ok := args["source"]; ok {
		source, _ := v.(string)
		source = strings.TrimSpace(source)
		if len([]rune(source)) > 255 {
			return nil, apperr.New(apperr.CodeInvalidParam, "source 过长")
		}
		req.Source = &source
	}

	if req.Stem == nil && req.Analysis == nil && req.Options == nil && req.Answer == nil &&
		req.Difficulty == nil && req.QuestionType == nil && req.Source == nil {
		return nil, apperr.New(apperr.CodeInvalidParam, "changes 里没有可识别的字段，没有产生任何改动")
	}
	return req, nil
}

// markdownArg 读一个 Markdown 文本参数。
//
// require 为 true 时空串算错误：题干不能是空的，而"把解析清空"
// 用一次不带该字段的调用表达更自然。
func markdownArg(args map[string]any, key string, require bool) (string, error) {
	raw, _ := args[key].(string)
	md := strings.TrimSpace(raw)
	if md == "" {
		if require {
			return "", apperr.New(apperr.CodeInvalidParam, key+" 不能为空")
		}
		return "", nil
	}
	if n := len([]rune(md)); n > maxContentRunes {
		return "", apperr.New(apperr.CodeInvalidParam, key+" 过长").
			WithDetail("%d 字，上限 %d 字", n, maxContentRunes)
	}
	return md, nil
}

// buildOptions 解析选项列表。
func buildOptions(raw any) ([]schema.OptionInput, error) {
	list, ok := raw.([]any)
	if !ok {
		return nil, apperr.New(apperr.CodeInvalidParam, "options 必须是数组")
	}
	if len(list) > 12 {
		return nil, apperr.New(apperr.CodeInvalidParam, "选项最多 12 个")
	}
	out := make([]schema.OptionInput, 0, len(list))
	for i, item := range list {
		opt, ok := item.(map[string]any)
		if !ok {
			return nil, apperr.New(apperr.CodeInvalidParam, "选项格式不正确").WithDetail("第 %d 个", i+1)
		}
		label, _ := opt["label"].(string)
		label = strings.TrimSpace(label)
		if label == "" {
			return nil, apperr.New(apperr.CodeInvalidParam, "选项缺少 label").WithDetail("第 %d 个", i+1)
		}
		text, _ := opt["text_markdown"].(string)
		text = strings.TrimSpace(text)
		if text == "" {
			return nil, apperr.New(apperr.CodeInvalidParam, "选项内容为空").WithDetail("第 %d 个", i+1)
		}
		if n := len([]rune(text)); n > maxContentRunes {
			return nil, apperr.New(apperr.CodeInvalidParam, "选项内容过长").WithDetail("第 %d 个", i+1)
		}
		out = append(out, schema.OptionInput{
			Label:   label,
			Content: importing.MarkdownToRichDoc(text),
		})
	}
	return out, nil
}

// buildAnswer 解析答案。
//
// kind 与字段的对应关系由 domain 层的校验兜底，这里只负责把模型给的结构
// 翻译成 AnswerSpec；翻译不出一个像样的 AnswerSpec 时返回 nil，
// 由调用方决定是报错还是"没给答案"。
func buildAnswer(raw any) (*model.AnswerSpec, error) {
	if raw == nil {
		return nil, nil
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil, apperr.New(apperr.CodeInvalidParam, "answer 必须是对象")
	}
	kind, _ := obj["kind"].(string)
	kind = strings.TrimSpace(kind)

	spec := &model.AnswerSpec{Kind: kind}
	switch kind {
	case "single", "multiple":
		for _, v := range stringList(obj["correct"]) {
			spec.Correct = append(spec.Correct, strings.TrimSpace(v))
		}
		if len(spec.Correct) == 0 {
			return nil, apperr.New(apperr.CodeInvalidParam, kind+" 题的答案需要 correct")
		}
	case "boolean":
		b, ok := obj["bool_answer"].(bool)
		if !ok {
			return nil, apperr.New(apperr.CodeInvalidParam, "判断题答案需要 bool_answer")
		}
		spec.BoolAnswer = &b
	case "blanks":
		list, ok := obj["blanks"].([]any)
		if !ok || len(list) == 0 {
			return nil, apperr.New(apperr.CodeInvalidParam, "填空题答案需要非空的 blanks")
		}
		for i, item := range list {
			blank, ok := item.(map[string]any)
			if !ok {
				return nil, apperr.New(apperr.CodeInvalidParam, "填空答案格式不正确").WithDetail("第 %d 个", i+1)
			}
			groups, ok := blank["accepts"].([]any)
			if !ok || len(groups) == 0 {
				return nil, apperr.New(apperr.CodeInvalidParam, "填空答案缺少 accepts").WithDetail("第 %d 个", i+1)
			}
			built := model.Blank{ID: fmt.Sprintf("blk_%d", i+1)}
			for _, group := range groups {
				values, ok := group.([]any)
				if !ok || len(values) == 0 {
					return nil, apperr.New(apperr.CodeInvalidParam, "accepts 的每项必须是非空数组").
						WithDetail("第 %d 个", i+1)
				}
				docs := make([]model.JSON, 0, len(values))
				for _, v := range values {
					text, _ := v.(string)
					text = strings.TrimSpace(text)
					if text == "" {
						return nil, apperr.New(apperr.CodeInvalidParam, "可接受答案里有空串").WithDetail("第 %d 个", i+1)
					}
					docs = append(docs, importing.MarkdownToRichDoc(text))
				}
				built.Accepts = append(built.Accepts, docs)
			}
			spec.Blanks = append(spec.Blanks, built)
		}
	case "reference":
		text, _ := obj["reference_markdown"].(string)
		text = strings.TrimSpace(text)
		if text == "" {
			return nil, apperr.New(apperr.CodeInvalidParam, "解答题答案需要 reference_markdown")
		}
		spec.Reference = importing.MarkdownToRichDoc(text)
	default:
		return nil, apperr.New(apperr.CodeInvalidParam, "answer.kind 不受支持").
			WithDetail("实际 %q，应为 single/multiple/boolean/blanks/reference", kind)
	}
	return spec, nil
}

// explicitIDList 读一个必须存在的 id 列表。
//
// 与 ArgUintSlice 的区别只有一处，但很关键：缺参数时这里报错，
// 而 ArgUintSlice 返回 nil。service 层正是靠 nil 与空切片的区别
// 表达"不动关联"与"清空关联"——少这一次区分，"清空"这个能力就没了。
func explicitIDList(args map[string]any, key, label string) ([]uint, error) {
	raw, ok := args[key]
	if !ok {
		return nil, apperr.New(apperr.CodeInvalidParam, "缺少 "+label+"。传空数组表示清空。")
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, apperr.New(apperr.CodeInvalidParam, label+" 必须是数组")
	}
	if len(list) > 50 {
		return nil, apperr.New(apperr.CodeInvalidParam, label+" 最多 50 项")
	}
	out := make([]uint, 0, len(list))
	for i, item := range list {
		n, ok := item.(float64)
		if !ok || n <= 0 {
			return nil, apperr.New(apperr.CodeInvalidParam, label+" 含有无效的 id").
				WithDetail("第 %d 项", i+1)
		}
		out = append(out, uint(n))
	}
	return out, nil
}

// describeChanges 列出本次实际改动的字段名。
//
// 回给模型是为了让它别把"请求里写了"当成"改成了"：一次没有实际变化的
// 写入和一次成功的写入，返回给用户的说法不该一样。
func describeChanges(args map[string]any) []string {
	names := []string{
		"stem_markdown", "options", "answer", "analysis_markdown",
		"difficulty", "question_type", "source",
	}
	out := make([]string, 0, len(args))
	for _, name := range names {
		if _, ok := args[name]; ok {
			out = append(out, name)
		}
	}
	return out
}

// intArg 读一个落在区间内的整数。
func intArg(raw any, lo, hi int) (int, bool) {
	n, ok := raw.(float64)
	if !ok {
		return 0, false
	}
	v := int(n)
	if v < lo || v > hi {
		return 0, false
	}
	return v, true
}

func stringList(raw any) []string {
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
