package service

import (
	"context"
	"testing"

	"github.com/NanamiKyoka/questionmind/internal/importing"
	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/repository"
	"github.com/NanamiKyoka/questionmind/internal/schema"
)

// 整卷导入要证明的不是"能建出一张卷"，而是"卷面和原件对得上"。
// 这里盯三件事：标题在、题在、题序对。

const paperImportFixture = `# 期中测试卷

考生注意：全卷 120 分钟。

一、选择题（每小题 5 分）
1.【题目】下列函数递增的是（  ）
A. $y=x^2$
B. $y=-x^2$
【答案】A
2.【题目】集合 $A=\{1,2\}$ 的真子集个数是（  ）
A. 2  B. 3  C. 4
【答案】C

二、填空题
3.【题目】$1+1=$（  ）
【答案】2
`

// 题号 → 题干纯文本，用来把节点和题目对上。
func nodeText(t *testing.T, svc *PaperService, user *model.User, paperID uint) []string {
	t.Helper()
	detail, err := svc.GetPaper(context.Background(), user, paperID)
	if err != nil {
		t.Fatalf("读取试卷失败: %v", err)
	}
	var out []string
	for _, n := range detail.Nodes {
		if n.NodeType != model.NodeTypeQuestion {
			continue
		}
		var ref struct {
			QuestionID uint `json:"question_id"`
		}
		if err := n.Content.Into(&ref); err != nil || ref.QuestionID == 0 {
			t.Fatalf("题目节点的内容不对: %+v", n.Content)
		}
		proj, ok := detail.Questions[ref.QuestionID]
		if !ok {
			t.Fatalf("题目 %d 没有内容投影", ref.QuestionID)
		}
		out = append(out, proj.StemText)
	}
	return out
}

func TestWholePaperImportRestoresHeadingsAndOrder(t *testing.T) {
	fx := newImportFixture(t, realisticExtraction)
	ctx := context.Background()
	// 整卷导入需要试卷服务；常规导入用不到它。
	fx.svc = fx.svc.WithPapers(NewPaperService(fx.repos))

	res, err := fx.svc.Extract(ctx, fx.admin, fx.subject.ID, &schema.ExtractRequest{
		Content:  paperImportFixture,
		Method:   string(importing.MethodTemplate),
		Filename: "期中测试卷.md",
	})
	if err != nil {
		t.Fatalf("抽取失败: %v", err)
	}
	if len(res.Drafts) != 3 {
		t.Fatalf("应解析出 3 道题，实际 %d", len(res.Drafts))
	}
	// 大纲必须带回来，否则整卷导入无从谈起。
	if len(res.Outline) == 0 {
		t.Fatal("抽取响应里必须带版面大纲")
	}

	commit, err := fx.svc.Commit(ctx, fx.admin, fx.subject.ID, &schema.CommitRequest{
		Drafts:      res.Drafts,
		SaveAsPaper: true,
		PaperTitle:  "期中测试卷",
		Outline:     res.Outline,
	})
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	if commit.CreatedCount != 3 {
		t.Fatalf("应入库 3 道题，实际 %d：%+v", commit.CreatedCount, commit.Failures)
	}
	if commit.PaperError != "" {
		t.Fatalf("建卷不应失败：%s", commit.PaperError)
	}
	if commit.PaperID == 0 {
		t.Fatal("应返回建出来的试卷 id")
	}

	// 题目确实入库了。
	if _, err := fx.repos.Questions.Get(ctx, commit.CreatedIDs[0]); err != nil {
		t.Fatalf("题目应已入库: %v", err)
	}

	stems := nodeText(t, NewPaperService(fx.repos), fx.admin, commit.PaperID)
	if len(stems) != 3 {
		t.Fatalf("卷上应有 3 道题，实际 %d", len(stems))
	}

	// 题序必须和原件一致：错序的卷子是没法发给学生的。
	if !contains(stems[0], "下列函数递增") {
		t.Fatalf("第 1 题错位: %v", stems)
	}
	if !contains(stems[1], "真子集个数") {
		t.Fatalf("第 2 题错位: %v", stems)
	}
	if !contains(stems[2], "1+1") {
		t.Fatalf("第 3 题错位: %v", stems)
	}

	// 标题必须落在卷上。
	detail, err := NewPaperService(fx.repos).GetPaper(ctx, fx.admin, commit.PaperID)
	if err != nil {
		t.Fatalf("读取试卷失败: %v", err)
	}
	var headingTexts []string
	for _, n := range detail.Nodes {
		if n.NodeType == model.NodeTypeHeading {
			headingTexts = append(headingTexts, n.Props.String())
		}
	}
	if len(headingTexts) == 0 {
		t.Fatal("卷上应当保留大题标题")
	}
	if detail.ImportTaskID == nil || *detail.ImportTaskID != commit.TaskID {
		t.Fatalf("试卷未回填来源导入任务: got=%v want=%d", detail.ImportTaskID, commit.TaskID)
	}
}

func TestPreviewPaperImportCountsUsableReferences(t *testing.T) {
	fx := newImportFixture(t, realisticExtraction)
	drafts := []importing.Draft{
		{TempID: "q1", StemMarkdown: "甲", Stem: importing.MarkdownToRichDoc("甲")},
		{TempID: "q2", StemMarkdown: "乙", Stem: importing.MarkdownToRichDoc("乙")},
	}
	res, err := fx.svc.PreviewPaperImport(context.Background(), fx.admin, fx.subject.ID, &schema.PaperImportPreviewRequest{
		Drafts: drafts,
		Outline: []importing.Outline{
			{Kind: importing.OutlineHeading, Text: "一、选择题", Level: 1},
			{Kind: importing.OutlineQuestion, DraftIndex: 0},
			{Kind: importing.OutlineQuestion, DraftIndex: 0},
			{Kind: importing.OutlineQuestion, DraftIndex: 9},
		},
	})
	if err != nil {
		t.Fatalf("预检失败: %v", err)
	}
	if !res.CanCreate || res.QuestionNodes != 1 || res.DuplicateReferences != 1 || res.MissingReferences != 1 {
		t.Fatalf("预检计数不符: %+v", res)
	}
}

func TestCommitIdempotencyKeyReplaysResult(t *testing.T) {
	fx := newImportFixture(t, realisticExtraction)
	ctx := context.Background()
	fx.svc = fx.svc.WithPapers(NewPaperService(fx.repos))
	payload := &schema.CommitRequest{
		IdempotencyKey: "commit-once-1",
		Drafts: []importing.Draft{{
			StemMarkdown: "题干", Stem: importing.MarkdownToRichDoc("题干"),
			QuestionType: model.QuestionTypeTrueFalse,
			Answer:       model.AnswerSpec{Kind: "boolean", BoolAnswer: boolPtr(true)},
		}},
	}
	first, err := fx.svc.Commit(ctx, fx.admin, fx.subject.ID, payload)
	if err != nil {
		t.Fatalf("首次提交失败: %v", err)
	}
	second, err := fx.svc.Commit(ctx, fx.admin, fx.subject.ID, payload)
	if err != nil {
		t.Fatalf("重放提交失败: %v", err)
	}
	if first.TaskID != second.TaskID || first.CreatedCount != second.CreatedCount || len(second.CreatedIDs) != 1 {
		t.Fatalf("幂等键未回放首次结果: first=%+v second=%+v", first, second)
	}
	_, total, err := fx.repos.Questions.List(ctx, model.QuestionFilter{SubjectID: fx.subject.ID}, model.QuestionSort("created_desc"), 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("幂等重放不应重复建题，实际 %d", total)
	}
}

// 没开 SaveAsPaper 时绝不能凭空建卷：只想把题存进题库的用户
// 不该收到一份多余的试卷。
func TestCommitWithoutSaveAsPaperCreatesNoPaper(t *testing.T) {
	fx := newImportFixture(t, realisticExtraction)
	fx.svc = fx.svc.WithPapers(NewPaperService(fx.repos))
	ctx := context.Background()

	before, _, _ := fx.repos.Papers.List(ctx, repository.PaperFilter{SubjectID: fx.subject.ID}, 100, 0)
	commit, err := fx.svc.Commit(ctx, fx.admin, fx.subject.ID, &schema.CommitRequest{
		Drafts: []importing.Draft{{
			StemMarkdown: "题干", Stem: importing.MarkdownToRichDoc("题干"),
			QuestionType: model.QuestionTypeTrueFalse,
			Answer:       model.AnswerSpec{Kind: "boolean", BoolAnswer: boolPtr(true)},
		}},
	})
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	if commit.PaperID != 0 {
		t.Fatalf("没要求建卷却建了：%d", commit.PaperID)
	}
	after, _, _ := fx.repos.Papers.List(ctx, repository.PaperFilter{SubjectID: fx.subject.ID}, 100, 0)
	if len(after) != len(before) {
		t.Fatalf("试卷数量不该变化：%d → %d", len(before), len(after))
	}
}

// 大纲里引用了入库失败的题时，跳过它并继续建卷——
// 宁可卷上少一道题，也不要留一个引用失效的空块。
func TestWholePaperImportSkipsFailedQuestions(t *testing.T) {
	fx := newImportFixture(t, realisticExtraction)
	fx.svc = fx.svc.WithPapers(NewPaperService(fx.repos))
	ctx := context.Background()

	// 第 2 道草稿是坏题：答案引用了不存在的选项，领域校验必然拒绝。
	bad := importing.Draft{
		StemMarkdown: "坏题", Stem: importing.MarkdownToRichDoc("坏题"),
		QuestionType: model.QuestionTypeSingleChoice,
		Options: []model.Option{{
			ID: "opt_a", Label: "A",
			Content: importing.MarkdownToRichDoc("甲"),
		}},
		Answer: model.AnswerSpec{Kind: "single", Correct: []string{"opt_missing"}},
	}
	good := importing.Draft{
		StemMarkdown: "好题", Stem: importing.MarkdownToRichDoc("好题"),
		QuestionType: model.QuestionTypeTrueFalse,
		Answer:       model.AnswerSpec{Kind: "boolean", BoolAnswer: boolPtr(true)},
	}

	commit, err := fx.svc.Commit(ctx, fx.admin, fx.subject.ID, &schema.CommitRequest{
		Drafts:      []importing.Draft{bad, good},
		SaveAsPaper: true,
		PaperTitle:  "混合卷",
		Outline: []importing.Outline{
			importing.Heading(1, "一、题目"),
			importing.QuestionRef(0),
			importing.QuestionRef(1),
		},
	})
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	if commit.FailedCount != 1 {
		t.Fatalf("应恰好有 1 道题入库失败，实际 %d", commit.FailedCount)
	}
	if commit.PaperID == 0 {
		t.Fatalf("部分失败时也该把能建的卷建出来（原因：%s）", commit.PaperError)
	}

	stems := nodeText(t, NewPaperService(fx.repos), fx.admin, commit.PaperID)
	if len(stems) != 1 {
		t.Fatalf("卷上只该有 1 道可用的题，实际 %d：%v", len(stems), stems)
	}
	if !contains(stems[0], "好题") {
		t.Fatalf("卷上应是那道入库成功的题，实际 %v", stems)
	}
}

// 题目入库失败**不能**让整次提交报错：题已经进库了，
// 报 error 会让用户以为整批没进去从而重复导入。
func TestWholePaperFailureNeverFailsTheCommit(t *testing.T) {
	fx := newImportFixture(t, realisticExtraction)
	ctx := context.Background()
	// 故意不注入试卷服务：建卷必然失败。
	res, err := fx.svc.Extract(ctx, fx.admin, fx.subject.ID, &schema.ExtractRequest{
		Content: paperImportFixture, Method: string(importing.MethodTemplate),
	})
	if err != nil {
		t.Fatalf("抽取失败: %v", err)
	}
	commit, err := fx.svc.Commit(ctx, fx.admin, fx.subject.ID, &schema.CommitRequest{
		Drafts: res.Drafts, SaveAsPaper: true, Outline: res.Outline,
	})
	if err != nil {
		t.Fatalf("建卷失败不得让提交失败: %v", err)
	}
	if commit.CreatedCount != 3 {
		t.Fatalf("题目应照常入库，实际 %d", commit.CreatedCount)
	}
	if commit.PaperError == "" {
		t.Fatal("建卷失败必须给出可读原因")
	}
}

func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
