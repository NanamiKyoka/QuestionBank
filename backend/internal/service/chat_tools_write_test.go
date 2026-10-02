package service

import (
	"context"
	"strings"
	"testing"

	"github.com/NanamiKyoka/questionmind/internal/aiagent"
	"github.com/NanamiKyoka/questionmind/internal/model"
)

// 写工具的参数契约。
//
// 模型是通过 JSON Schema 认识这些工具的，参数名写错一个字母、
// 或者把"没给"和"给空"混为一谈，表现出来的都是一次莫名其妙的写入。
// 因此这里把最容易出错的几种边界钉死。

func TestBuildQuestionUpdateOnlyTouchesGivenFields(t *testing.T) {
	req, err := buildQuestionUpdate(map[string]any{
		"difficulty": float64(4),
		"source":     "  2024 全国甲卷  ",
	}, "改个难度")
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}

	if req.Stem != nil || req.Options != nil || req.Answer != nil || req.Analysis != nil {
		t.Fatal("没给的字段必须保持 nil，否则会覆盖掉题目原有内容")
	}
	if req.Difficulty == nil || *req.Difficulty != 4 {
		t.Fatalf("difficulty 应为 4，实际 %v", req.Difficulty)
	}
	if req.Source == nil || *req.Source != "2024 全国甲卷" {
		t.Fatalf("source 应去掉首尾空白，实际 %v", req.Source)
	}
	if req.ChangeNote != "改个难度" {
		t.Fatalf("change_note 应保留，实际 %q", req.ChangeNote)
	}
}

// 「把解析清空」和「不动解析」在 service 层是靠 nil 与空文档区分的。
// 模型给出空串时必须真的产生一个空文档，而不是被当成"没给"。
func TestBuildQuestionUpdateDistinguishesEmptyFromMissing(t *testing.T) {
	req, err := buildQuestionUpdate(map[string]any{"analysis_markdown": "  "}, "")
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if req.Analysis == nil {
		t.Fatal("给出空串应产生一个空文档，而不是 nil")
	}
	if got := markdownFromJSON(req.Analysis); strings.TrimSpace(got) != "" {
		t.Fatalf("空串解析后不该留下任何文字，实际 %q", got)
	}
}

func TestBuildQuestionUpdateRejectsUnusableInput(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
	}{
		{"空 changes", map[string]any{}},
		{"只有无关字段", map[string]any{"foo": "bar"}},
		{"难度越界", map[string]any{"difficulty": float64(9)}},
		{"题型不存在", map[string]any{"question_type": "填空"}},
		{"题干为空", map[string]any{"stem_markdown": "   "}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := buildQuestionUpdate(c.args, ""); err == nil {
				t.Fatalf("%s 应被拒绝", c.name)
			}
		})
	}
}

// 公式必须以富文本形式落库，而不是把 Markdown 原文塞进题干。
func TestBuildQuestionUpdateConvertsMarkdownToRichDoc(t *testing.T) {
	req, err := buildQuestionUpdate(map[string]any{
		"stem_markdown": "已知 $f(x)=x^2$，则 $f(2)=$（  ）",
	}, "")
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	got := markdownFromJSON(req.Stem)
	if !strings.Contains(got, "$f(x)=x^2$") {
		t.Fatalf("公式应保留 $ 定界符与 LaTeX，实际 %q", got)
	}
	if !strings.Contains(string(req.Stem), `"type":"doc"`) {
		t.Fatalf("题干应被转成富文本文档，实际 %s", req.Stem)
	}
}

func TestBuildAnswerCoversEveryKind(t *testing.T) {
	single, err := buildAnswer(map[string]any{"kind": "single", "correct": []any{"B"}})
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if single.Kind != "single" || len(single.Correct) != 1 || single.Correct[0] != "B" {
		t.Fatalf("选择题答案解析错误: %+v", single)
	}

	boolean, err := buildAnswer(map[string]any{"kind": "boolean", "bool_answer": true})
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if boolean.BoolAnswer == nil || !*boolean.BoolAnswer {
		t.Fatalf("判断题答案解析错误: %+v", boolean)
	}

	blanks, err := buildAnswer(map[string]any{
		"kind":   "blanks",
		"blanks": []any{map[string]any{"accepts": []any{[]any{"3", "三"}}}},
	})
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if len(blanks.Blanks) != 1 || len(blanks.Blanks[0].Accepts) != 1 || len(blanks.Blanks[0].Accepts[0]) != 2 {
		t.Fatalf("填空题答案解析错误: %+v", blanks)
	}
	if blanks.Blanks[0].ID != "blk_1" {
		t.Fatalf("空 id 应按序号生成，实际 %q", blanks.Blanks[0].ID)
	}

	reference, err := buildAnswer(map[string]any{
		"kind": "reference", "reference_markdown": "由 $x^2$ 求导得 $2x$",
	})
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if !strings.Contains(string(reference.Reference), `"type":"doc"`) {
		t.Fatalf("参考答案应转成富文本，实际 %s", reference.Reference)
	}
}

// kind 与字段对不上时必须在这里就拒掉：让它落到 domain 层，
// 模型只会收到一句更难自我纠正的领域错误。
func TestBuildAnswerRejectsIncompleteInput(t *testing.T) {
	cases := []any{
		map[string]any{"kind": "single"},
		map[string]any{"kind": "boolean"},
		map[string]any{"kind": "blanks", "blanks": []any{}},
		map[string]any{"kind": "reference", "reference_markdown": "  "},
		map[string]any{"kind": "whatever"},
		"不是对象",
	}
	for i, raw := range cases {
		if _, err := buildAnswer(raw); err == nil {
			t.Fatalf("第 %d 个应被拒绝", i+1)
		}
	}
	// 没给答案不等于给了个坏答案。
	spec, err := buildAnswer(nil)
	if err != nil || spec != nil {
		t.Fatalf("缺省答案应返回 (nil, nil)，实际 (%v, %v)", spec, err)
	}
}

// 「清空关联」是独立于「不动关联」的能力，模型必须能表达。
func TestExplicitIDListSeparatesClearFromMissing(t *testing.T) {
	cleared, err := explicitIDList(map[string]any{"tag_ids": []any{}}, "tag_ids", "tag_ids")
	if err != nil {
		t.Fatalf("空数组应表示清空: %v", err)
	}
	if len(cleared) != 0 {
		t.Fatalf("空数组应得到空切片，实际 %v", cleared)
	}
	if cleared == nil {
		t.Fatal("空数组必须得到非 nil 的空切片：service 靠它与 nil 的区别表达清空")
	}

	if _, err := explicitIDList(map[string]any{}, "tag_ids", "tag_ids"); err == nil {
		t.Fatal("缺参数应被拒绝，否则模型会以为已经清空了")
	}
	if _, err := explicitIDList(map[string]any{"tag_ids": []any{float64(0)}}, "tag_ids", "tag_ids"); err == nil {
		t.Fatal("id 0 应被拒绝")
	}
	if _, err := explicitIDList(map[string]any{"tag_ids": "1,2"}, "tag_ids", "tag_ids"); err == nil {
		t.Fatal("非数组应被拒绝")
	}
}

// ---- 端到端 ----

func newWriteDeps(t *testing.T) (toolDeps, *model.User, uint, *model.Question) {
	t.Helper()
	repos := newTestRepos(t)
	ctx := context.Background()

	admin := adminUser()
	subject := &model.Subject{Name: "数学", ReviewThreshold: 1, CreatedBy: admin.ID}
	if err := repos.Subjects.Create(ctx, subject); err != nil {
		t.Fatalf("建学科失败: %v", err)
	}
	if err := repos.Subjects.AddMember(ctx, subject.ID, admin.ID, model.SubjectRoleManager); err != nil {
		t.Fatalf("加成员失败: %v", err)
	}
	kp := &model.KnowledgePoint{SubjectID: subject.ID, Name: "导数", CreatedBy: admin.ID}
	if err := repos.KnowledgePoints.Create(ctx, kp); err != nil {
		t.Fatalf("建知识点失败: %v", err)
	}

	question := &model.Question{
		SubjectID:    subject.ID,
		QuestionType: model.QuestionTypeSingleChoice,
		Difficulty:   2,
		Status:       model.QuestionStatusPublished,
		Visibility:   model.VisibilityPublic,
		Stem:         mustJSONT(t, map[string]any{"type": "doc"}),
		// 选择题没有合法选项就过不了 domain 校验：写工具的每次 Update
		// 都会重新校验一遍整道题，所以夹具必须是**完整**的题目。
		Options: mustJSONT(t, []model.Option{
			{ID: "opt_1", Label: "A", Content: mustJSONT(t, map[string]any{"type": "doc"})},
			{ID: "opt_2", Label: "B", Content: mustJSONT(t, map[string]any{"type": "doc"})},
		}),
		Answer:          mustJSONT(t, model.AnswerSpec{Kind: "single", Correct: []string{"A"}}),
		StemText:        "原题干",
		ContentVersion:  1,
		ContentRevision: 1,
		CreatedBy:       admin.ID,
		UpdatedBy:       admin.ID,
	}
	if err := repos.Questions.Create(ctx, question); err != nil {
		t.Fatalf("建题失败: %v", err)
	}
	return toolDeps{questions: NewQuestionService(repos)}, admin, subject.ID, question
}

func writeTool(t *testing.T, deps toolDeps, name string) *aiagent.Tool {
	t.Helper()
	tool, ok := buildToolRegistry(deps).Get(name)
	if !ok {
		t.Fatalf("工具 %s 未注册", name)
	}
	return tool
}

func runWriteTool(t *testing.T, tool *aiagent.Tool, user *model.User, subjectID uint, args map[string]any) aiagent.ToolResult {
	t.Helper()
	res, err := tool.Handler(context.Background(), aiagent.ToolCall{
		User:      user,
		SubjectID: subjectID,
		Arguments: args,
	})
	if err != nil {
		t.Fatalf("工具不应返回硬错误: %v", err)
	}
	return res
}

func TestUpdateQuestionRewritesStemAndBumpsRevision(t *testing.T) {
	deps, user, subjectID, question := newWriteDeps(t)
	tool := writeTool(t, deps, "update_question")

	res := runWriteTool(t, tool, user, subjectID, map[string]any{
		"question_id": float64(question.ID),
		"change_note": "改题干",
		"changes":     map[string]any{"stem_markdown": "已知 $f(x)=x^2$"},
	})
	if !strings.Contains(res.Content, `"content_revision":2`) {
		t.Fatalf("内容改动应递增 content_revision，实际 %s", res.Content)
	}

	detail, err := deps.questions.Get(context.Background(), user, question.ID)
	if err != nil {
		t.Fatalf("读回失败: %v", err)
	}
	if got := markdownFromJSON(detail.Stem); !strings.Contains(got, "$f(x)=x^2$") {
		t.Fatalf("题干没被改写，实际 %q", got)
	}
	if detail.Difficulty != 2 {
		t.Fatalf("没给的字段不该被动到，难度实际 %d", detail.Difficulty)
	}
}

func TestSetKnowledgePointsReplacesTheWholeSet(t *testing.T) {
	deps, user, subjectID, question := newWriteDeps(t)
	tool := writeTool(t, deps, "set_question_knowledge_points")

	res := runWriteTool(t, tool, user, subjectID, map[string]any{
		"question_id":         float64(question.ID),
		"knowledge_point_ids": []any{},
	})
	if !strings.Contains(res.Content, `"knowledge_points":[]`) {
		t.Fatalf("空数组应清空关联，实际 %s", res.Content)
	}
}

func TestWriteToolsRefuseMissingQuestionID(t *testing.T) {
	deps, user, subjectID, _ := newWriteDeps(t)
	for _, name := range []string{
		"update_question", "set_question_knowledge_points", "set_question_tags",
		"submit_question", "archive_question", "delete_question", "restore_question",
	} {
		t.Run(name, func(t *testing.T) {
			tool := writeTool(t, deps, name)
			args := map[string]any{}
			if name == "archive_question" {
				args["archived"] = true
			}
			if name == "update_question" {
				args["changes"] = map[string]any{"difficulty": float64(3)}
			}
			res := runWriteTool(t, tool, user, subjectID, args)
			if !strings.Contains(res.Content, "缺少 question_id") {
				t.Fatalf("应提示缺少 question_id，实际 %s", res.Content)
			}
		})
	}
}

// 每个写工具都必须声明 Mutating 与 Capability。
// Mutating 决定前端把这次调用渲染成什么，Capability 是事后审计的唯一线索。
func TestWriteToolsDeclareMutationMetadata(t *testing.T) {
	deps, _, _, _ := newWriteDeps(t)
	for _, tool := range buildToolRegistry(deps).All() {
		if !tool.Mutating {
			continue
		}
		if tool.Capability == "" {
			t.Fatalf("写工具 %s 没有声明 Capability", tool.Name)
		}
	}
}

// 空描述的工具等于没注册：模型无从判断什么时候该用它，
// 只能靠名字猜，而名字通常比描述短得多。
func TestEveryToolHasADescription(t *testing.T) {
	deps, _, _, _ := newWriteDeps(t)
	for _, tool := range buildToolRegistry(deps).All() {
		if strings.TrimSpace(tool.Description) == "" {
			t.Fatalf("工具 %s 没有描述", tool.Name)
		}
	}
}

// 工具 schema 全部要进提示词，因此单个场景下的工具数量必须有上限。
// 这个数只防"某天随手加了一个工具"造成上下文膨胀，不是质量指标。
func TestToolSetStaysWithinContextBudget(t *testing.T) {
	const maxPerScene = 30

	deps, _, _, _ := newWriteDeps(t)
	for _, scene := range []aiagent.Scene{
		aiagent.SceneUnscoped, aiagent.SceneQuestionLibrary,
		aiagent.SceneCompositionEditor, aiagent.SceneImportReview,
	} {
		got := len(buildToolRegistry(deps).Schemas(scene, true))
		if got > maxPerScene {
			t.Fatalf("场景 %s 暴露了 %d 个工具，超过上限 %d", scene, got, maxPerScene)
		}
	}
}

// 题库页面的写工具不该出现在组卷场景：用户在那里改的是试卷。
func TestQuestionWriteToolsStayOutOfTheEditorScene(t *testing.T) {
	deps, _, _, _ := newWriteDeps(t)
	reg := buildToolRegistry(deps)
	for _, name := range []string{
		"update_question", "set_question_knowledge_points", "set_question_tags",
		"create_question", "submit_question", "archive_question",
		"delete_question", "restore_question",
	} {
		tool, _ := reg.Get(name)
		if tool.AvailableIn(aiagent.SceneCompositionEditor) {
			t.Fatalf("%s 不该在组卷场景可用", name)
		}
		if !tool.AvailableIn(aiagent.SceneQuestionLibrary) {
			t.Fatalf("%s 应在题库场景可用", name)
		}
	}
}
