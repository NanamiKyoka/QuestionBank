package service

import (
	"context"
	"strings"
	"testing"

	"github.com/NanamiKyoka/questionmind/internal/aiagent"
	"github.com/NanamiKyoka/questionmind/internal/model"
)

// preview_question_edits 的服务端闸门。
//
// 这批参数最终会变成对题库的写入请求，因此在放行之前必须钉死三件事：
// 形状对不对、题目存不存在且可改、模型给的文本有没有超长。
// 少放行一条，表现就是用户看着一份漂亮的预览，点「应用」却有一半失败。

func previewCall(args map[string]any, user *model.User, subjectID uint) aiagent.ToolCall {
	return aiagent.ToolCall{User: user, SubjectID: subjectID, Arguments: args}
}

func TestPreviewQuestionEditsAttachesBeforeValues(t *testing.T) {
	deps, user, subjectID, question := newWriteDeps(t)

	out, err := prepareQuestionEdits(deps)(context.Background(), previewCall(map[string]any{
		"summary": "  统一难度  ",
		"operations": []any{
			map[string]any{
				"op":          "update",
				"question_id": float64(question.ID),
				"difficulty":  float64(4),
			},
		},
	}, user, subjectID))
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if out["summary"] != "统一难度" {
		t.Fatalf("summary 应去掉首尾空白，实际 %q", out["summary"])
	}

	ops, _ := out["operations"].([]map[string]any)
	if len(ops) != 1 {
		t.Fatalf("应有 1 个操作，实际 %d", len(ops))
	}
	before, ok := ops[0]["before"].(map[string]any)
	if !ok {
		t.Fatalf("操作应带上改前值，实际 %T", ops[0]["before"])
	}
	if before["stem"] != "原题干" {
		t.Fatalf("改前值应带上题干摘要，实际 %v", before["stem"])
	}
	payload, _ := ops[0]["payload"].(map[string]any)
	if payload["difficulty"] != 4 {
		t.Fatalf("载荷应带新难度，实际 %v", payload["difficulty"])
	}
	if payload["fields"] == nil {
		t.Fatal("载荷应列出改动的字段名，供前端渲染差异标题")
	}
}

// 题干的 Markdown 必须在服务端转好：前端只做纯粹的展示与写回。
func TestPreviewQuestionEditsConvertsStemMarkdown(t *testing.T) {
	deps, user, subjectID, question := newWriteDeps(t)

	out, err := prepareQuestionEdits(deps)(context.Background(), previewCall(map[string]any{
		"operations": []any{map[string]any{
			"op":            "update",
			"question_id":   float64(question.ID),
			"stem_markdown": "已知 $f(x)=x^2$",
		}},
	}, user, subjectID))
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}

	payload, _ := out["operations"].([]map[string]any)[0]["payload"].(map[string]any)
	stem, ok := payload["stem"]
	if !ok {
		t.Fatal("载荷应带上新题干")
	}
	doc, err := model.JSONOf(stem)
	if err != nil {
		t.Fatalf("题干不是合法 JSON: %v", err)
	}
	if !strings.Contains(string(doc), `"type":"doc"`) {
		t.Fatalf("题干应转成富文本，实际 %s", doc)
	}
	if payload["stem_markdown"] != "已知 $f(x)=x^2$" {
		t.Fatalf("应保留原始 Markdown 供展示，实际 %v", payload["stem_markdown"])
	}
}

func TestPreviewQuestionEditsRejectsBadInput(t *testing.T) {
	deps, user, subjectID, question := newWriteDeps(t)
	prepare := prepareQuestionEdits(deps)

	tooMany := make([]any, 0, maxQuestionEdits+1)
	for i := 0; i <= maxQuestionEdits; i++ {
		tooMany = append(tooMany, map[string]any{
			"op": "update", "question_id": float64(question.ID), "difficulty": float64(3),
		})
	}

	cases := []struct {
		name string
		args map[string]any
	}{
		{"缺少 operations", map[string]any{}},
		{"operations 不是数组", map[string]any{"operations": "update"}},
		{"operations 为空", map[string]any{"operations": []any{}}},
		{"未知操作", map[string]any{"operations": []any{map[string]any{
			"op": "drop_everything", "question_id": float64(question.ID),
		}}}},
		{"操作不是对象", map[string]any{"operations": []any{"update"}}},
		{"题目不存在", map[string]any{"operations": []any{map[string]any{
			"op": "update", "question_id": float64(999999), "difficulty": float64(3),
		}}}},
		{"update 没给字段", map[string]any{"operations": []any{map[string]any{
			"op": "update", "question_id": float64(question.ID),
		}}}},
		{"难度越界", map[string]any{"operations": []any{map[string]any{
			"op": "update", "question_id": float64(question.ID), "difficulty": float64(8),
		}}}},
		{"题干为空", map[string]any{"operations": []any{map[string]any{
			"op": "update", "question_id": float64(question.ID), "stem_markdown": "   ",
		}}}},
		{"题干超长", map[string]any{"operations": []any{map[string]any{
			"op":            "update",
			"question_id":   float64(question.ID),
			"stem_markdown": strings.Repeat("字", maxContentRunes+1),
		}}}},
		{"标签不是数组", map[string]any{"operations": []any{map[string]any{
			"op": "update", "question_id": float64(question.ID), "tag_ids": "1,2",
		}}}},
		{"超量操作", map[string]any{"operations": tooMany}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := prepare(context.Background(), previewCall(c.args, user, subjectID)); err == nil {
				t.Fatalf("%s 应被拒绝", c.name)
			}
		})
	}
}

// 清空关联要能表达：空数组合法，缺失才是错误。
func TestPreviewQuestionEditsAcceptsEmptyIDLists(t *testing.T) {
	deps, user, subjectID, question := newWriteDeps(t)

	out, err := prepareQuestionEdits(deps)(context.Background(), previewCall(map[string]any{
		"operations": []any{map[string]any{
			"op":                  "update",
			"question_id":         float64(question.ID),
			"knowledge_point_ids": []any{},
		}},
	}, user, subjectID))
	if err != nil {
		t.Fatalf("空数组应表示清空: %v", err)
	}
	payload, _ := out["operations"].([]map[string]any)[0]["payload"].(map[string]any)
	ids, ok := payload["knowledge_point_ids"].([]uint)
	if !ok || ids == nil || len(ids) != 0 {
		t.Fatalf("应得到非 nil 的空切片，实际 %#v", payload["knowledge_point_ids"])
	}
}

// 删除是无权限时最该被拦下的一条：它过了闸门，用户就会点「应用」。
func TestPreviewQuestionEditsBlocksUnauthorizedDelete(t *testing.T) {
	d := &schemaDetail{}
	d.Permissions.CanDelete = false
	if err := checkPreviewPermission(d, "delete"); err == nil {
		t.Fatal("没有删除权限时应被拦下")
	}
	if err := checkPreviewPermission(d, "archive"); err == nil {
		t.Fatal("没有删除权限时归档同样应被拦下（归档用的是同一档权限）")
	}

	d.Permissions.CanDelete = true
	if err := checkPreviewPermission(d, "delete"); err != nil {
		t.Fatalf("有权限时不该被拦: %v", err)
	}

	e := &schemaDetail{}
	e.Permissions.CanEdit = true
	if err := checkPreviewPermission(e, "update"); err != nil {
		t.Fatalf("可编辑时不该被拦: %v", err)
	}
	e.Permissions.CanEdit = false
	if err := checkPreviewPermission(e, "update"); err == nil {
		t.Fatal("不可编辑时 update 应被拦下")
	}
}

// 这个工具是客户端工具：没有服务端 Handler，场景必须在题库与录题审核两处。
func TestPreviewQuestionEditsIsAClientTool(t *testing.T) {
	deps, _, _, _ := newWriteDeps(t)
	tool, ok := buildToolRegistry(deps).Get("preview_question_edits")
	if !ok {
		t.Fatal("preview_question_edits 未注册")
	}
	if tool.Executor != aiagent.ExecutorClient {
		t.Fatalf("执行方应为 client，实际 %q", tool.Executor)
	}
	if tool.Handler != nil {
		t.Fatal("客户端工具不该带服务端 Handler")
	}
	if tool.Prepare == nil {
		t.Fatal("写操作的客户端工具必须有预处理")
	}
	if tool.AvailableIn(aiagent.SceneCompositionEditor) {
		t.Fatal("组卷场景不应暴露题目改动工具：那里用户改的是试卷")
	}
	// 审核场景背后没有注册处理器的页面组件，放出去只会让模型撞一句"未实现"。
	if tool.AvailableIn(aiagent.SceneImportReview) {
		t.Fatal("审核场景没有对应的前端组件，不该暴露客户端工具")
	}
	if !tool.AvailableIn(aiagent.SceneQuestionLibrary) {
		t.Fatal("题库场景应可用")
	}
	if !strings.Contains(tool.Description, "应用") {
		t.Fatal("描述应告诉模型改动需要用户点应用确认")
	}
}
