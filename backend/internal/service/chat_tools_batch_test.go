package service

import (
	"context"
	"strings"
	"testing"

	"github.com/NanamiKyoka/questionmind/internal/model"
)

// 批量工具的 dry_run 契约。
//
// 整套设计的安全性压在一个约定上：模型漏写 dry_run 时，数据库必须**不动**。
// 一旦这条反了，"预演"就只是提示词里的一句建议，而批量写入不可逆。

func TestBatchIDListRejectsUnusableInput(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"缺参数", map[string]any{}, "缺少"},
		{"空数组", map[string]any{"question_ids": []any{}}, "不能为空"},
		{"非数组", map[string]any{"question_ids": "1,2"}, "必须是数组"},
		{"含 id 0", map[string]any{"question_ids": []any{float64(0)}}, "无效的 id"},
		{"含负数", map[string]any{"question_ids": []any{float64(-1)}}, "无效的 id"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := batchIDList(c.args, "question_ids"); err == nil {
				t.Fatalf("%s 应被拒绝", c.name)
			} else if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("错误应包含 %q，实际 %v", c.want, err)
			}
		})
	}

	tooMany := make([]any, 0, maxBatchIDs+1)
	for i := 0; i <= maxBatchIDs; i++ {
		tooMany = append(tooMany, float64(i+1))
	}
	_, err := batchIDList(map[string]any{"question_ids": tooMany}, "question_ids")
	if err == nil || !strings.Contains(err.Error(), "分批") {
		t.Fatalf("超量应被拒绝并提示分批，实际 %v", err)
	}

	// 重复 id 折叠，但保序：删除这类不可逆操作要能照着清单念。
	ids, err := batchIDList(map[string]any{
		"question_ids": []any{float64(7), float64(3), float64(7)},
	}, "question_ids")
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if len(ids) != 2 || ids[0] != 7 || ids[1] != 3 {
		t.Fatalf("应折叠重复且保持原序，实际 %v", ids)
	}
}

// 漏写 dry_run 时必须停在预演分支。
func TestBatchToolsDefaultToDryRun(t *testing.T) {
	deps, user, subjectID, question := newWriteDeps(t)

	tool := writeTool(t, deps, "batch_set_source")
	res := runWriteTool(t, tool, user, subjectID, map[string]any{
		"question_ids": []any{float64(question.ID)},
		"source":       "2024 全国甲卷",
	})
	if !strings.Contains(res.Content, `"dry_run":true`) {
		t.Fatalf("缺省应走预演分支，实际 %s", res.Content)
	}

	detail, err := deps.questions.Get(context.Background(), user, question.ID)
	if err != nil {
		t.Fatalf("读回失败: %v", err)
	}
	if detail.Source != "" {
		t.Fatalf("预演不该写入库，来源实际 %q", detail.Source)
	}
}

func TestBatchSetSourceDryRunReportsThenExecutes(t *testing.T) {
	deps, user, subjectID, question := newWriteDeps(t)
	tool := writeTool(t, deps, "batch_set_source")

	preview := runWriteTool(t, tool, user, subjectID, map[string]any{
		"question_ids": []any{float64(question.ID)},
		"source":       "2024 全国甲卷",
		"dry_run":      true,
	})
	if !strings.Contains(preview.Content, `"affected":[`) || !strings.Contains(preview.Content, dryRunHint) {
		t.Fatalf("预演应给出可执行清单并提示尚未改动，实际 %s", preview.Content)
	}

	run := runWriteTool(t, tool, user, subjectID, map[string]any{
		"question_ids": []any{float64(question.ID)},
		"source":       "2024 全国甲卷",
		"dry_run":      false,
	})
	if !strings.Contains(run.Content, `"dry_run":false`) {
		t.Fatalf("执行应标明不是预演，实际 %s", run.Content)
	}

	detail, err := deps.questions.Get(context.Background(), user, question.ID)
	if err != nil {
		t.Fatalf("读回失败: %v", err)
	}
	if detail.Source != "2024 全国甲卷" {
		t.Fatalf("执行后来源应为新值，实际 %q", detail.Source)
	}
}

// 重复执行同一批时，第二轮应如实报告"没有可改的"，
// 而不是谎报一次成功的写入。
func TestBatchSetSourceDryRunSkipsNoOps(t *testing.T) {
	deps, user, subjectID, question := newWriteDeps(t)
	tool := writeTool(t, deps, "batch_set_source")
	args := map[string]any{
		"question_ids": []any{float64(question.ID)},
		"source":       "2024 全国甲卷",
		"dry_run":      false,
	}
	runWriteTool(t, tool, user, subjectID, args)

	again := runWriteTool(t, tool, user, subjectID, map[string]any{
		"question_ids": []any{float64(question.ID)},
		"source":       "2024 全国甲卷",
		"dry_run":      true,
	})
	if !strings.Contains(again.Content, "来源已经是这个值") {
		t.Fatalf("预演应指出无变化，实际 %s", again.Content)
	}
}

func TestBatchDeleteDryRunListsBeforeDeleting(t *testing.T) {
	deps, user, subjectID, question := newWriteDeps(t)
	tool := writeTool(t, deps, "batch_delete_questions")

	preview := runWriteTool(t, tool, user, subjectID, map[string]any{
		"question_ids": []any{float64(question.ID)},
	})
	if !strings.Contains(preview.Content, dryRunHint) {
		t.Fatalf("删除预演必须明确说明未改动，实际 %s", preview.Content)
	}
	if _, err := deps.questions.Get(context.Background(), user, question.ID); err != nil {
		t.Fatalf("预演不该删除题目: %v", err)
	}
}

// 预演要按 service 层的真实前置条件判定，而不是一律放行。
func TestReviewPreconditionMirrorsTheStateMachine(t *testing.T) {
	cases := []struct {
		status model.QuestionStatus
		action string
		ok     bool
	}{
		{model.QuestionStatusDraft, "submit", true},
		{model.QuestionStatusPending, "submit", false},
		{model.QuestionStatusDraft, "approve", true},
		{model.QuestionStatusPending, "approve", true},
		{model.QuestionStatusPublished, "approve", false},
		{model.QuestionStatusArchived, "approve", false},
		{model.QuestionStatusPending, "reject", true},
		{model.QuestionStatusPublished, "reject", false},
	}
	for _, c := range cases {
		t.Run(string(c.status)+"/"+c.action, func(t *testing.T) {
			d := &schemaDetail{Status: c.status}
			d.Permissions.CanEdit = true
			d.Permissions.CanReview = true
			verdict, reason := reviewPrecondition(d, c.action)
			if c.ok && verdict != "affected" {
				t.Fatalf("应可执行，实际 %s：%s", verdict, reason)
			}
			if !c.ok {
				if verdict != "blocked" {
					t.Fatalf("应被拦下，实际 %s", verdict)
				}
				if reason == "" {
					t.Fatal("拦下时必须给出原因，否则模型无从修正")
				}
			}
		})
	}
}

// 缺权限时也要拦下：预演如果说"可以"，用户点确认之后才发现不行，
// 那这次预演就是在骗人。
func TestReviewPreconditionHonoursPermissions(t *testing.T) {
	d := &schemaDetail{Status: model.QuestionStatusDraft}
	if verdict, _ := reviewPrecondition(d, "approve"); verdict != "blocked" {
		t.Fatal("没有审核权限时不应判为可执行")
	}
	if verdict, _ := reviewPrecondition(d, "submit"); verdict != "blocked" {
		t.Fatal("没有编辑权限时不应判为可提交")
	}
}

func TestArchivePreconditionMirrorsTheStateMachine(t *testing.T) {
	published := &schemaDetail{Status: model.QuestionStatusPublished}
	if verdict, _ := archivePrecondition(published, true); verdict != "affected" {
		t.Fatal("已发布的题应可归档")
	}
	if verdict, _ := archivePrecondition(published, false); verdict != "blocked" {
		t.Fatal("未归档的题不能取消归档")
	}

	archived := &schemaDetail{Status: model.QuestionStatusArchived}
	if verdict, _ := archivePrecondition(archived, true); verdict != "blocked" {
		t.Fatal("已归档的题不能重复归档")
	}
	if verdict, _ := archivePrecondition(archived, false); verdict != "affected" {
		t.Fatal("归档的题应可取消归档")
	}
}

// 预演读到的状态必须影响后续判断，而不是恒为 true。
func TestPlanQuestionBatchSeparatesAffectedFromBlocked(t *testing.T) {
	deps, user, subjectID, question := newWriteDeps(t)
	tool := writeTool(t, deps, "batch_set_source")

	res := runWriteTool(t, tool, user, subjectID, map[string]any{
		"question_ids": []any{float64(question.ID), float64(999999)},
		"source":       "期中考试",
		"dry_run":      true,
	})
	if !strings.Contains(res.Content, `"missing"`) {
		t.Fatalf("不存在的题目应单列出来，实际 %s", res.Content)
	}
	if !strings.Contains(res.Content, `"total":2`) {
		t.Fatalf("总数应含不存在的题目，实际 %s", res.Content)
	}
}

// 每个批量工具都必须在 schema 里声明 dry_run，
// 并在描述里说清"省略等同于预演"——否则模型无从知道要写什么。
func TestBatchToolsDeclareDryRun(t *testing.T) {
	deps, _, _, _ := newWriteDeps(t)
	names := []string{
		"batch_update_questions", "batch_review_questions", "batch_archive_questions",
		"batch_delete_questions", "batch_set_source",
	}
	reg := buildToolRegistry(deps)
	for _, name := range names {
		tool, ok := reg.Get(name)
		if !ok {
			t.Fatalf("工具 %s 未注册", name)
		}
		props, _ := tool.Parameters["properties"].(map[string]any)
		if _, ok := props["dry_run"]; !ok {
			t.Fatalf("%s 的 schema 应声明 dry_run", name)
		}
		if !strings.Contains(tool.Description, "dry_run") {
			t.Fatalf("%s 的描述应提到 dry_run", name)
		}
	}
}
