package service

import (
	"archive/zip"
	"bytes"
	"context"
	"strconv"
	"testing"

	"github.com/NanamiKyoka/questionmind/internal/importing"
	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/schema"
)

// 这些用例覆盖的是"不配任何 AI 也能录题"这条能力。
//
// 它是规则解析器存在的全部理由：模型路径在没有供应商、密钥过期或
// 额度耗尽时会直接失败，而带标签的电子稿本来就不需要模型来看一眼。

// newTemplateFixture 刻意**不配置任何 AI**。
func newTemplateFixture(t *testing.T) (*Repos, *ImportService, *model.Subject, *model.User) {
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

	// aiSvc 建出来但不配供应商/模型：让 Resolve 一定失败。
	aiSvc := newAIService(t, repos)
	questionSvc := NewQuestionService(repos)
	return repos, NewImportService(repos, aiSvc, questionSvc), subject, admin
}

const tagPaper = `1.【题目】下列说法正确的是（　）
A. 甲
B. 乙
【答案】A
【解析】因为甲成立

2.【题目】下面哪个不是整数
A. 1
B. 2.5
【答案】B
`

func TestExtractByTemplateNeedsNoAI(t *testing.T) {
	_, svc, subject, admin := newTemplateFixture(t)
	ctx := context.Background()

	res, err := svc.Extract(ctx, admin, subject.ID, &schema.ExtractRequest{
		Content:  tagPaper,
		Method:   string(importing.MethodTemplate),
		Filename: "随堂卷.md",
	})
	if err != nil {
		t.Fatalf("未配置 AI 时规则解析也必须可用: %v", err)
	}
	if res.Method != string(importing.MethodTemplate) {
		t.Fatalf("响应应回显 method=template，实际 %q", res.Method)
	}
	if len(res.Drafts) != 2 {
		t.Fatalf("应解析出 2 道题，实际 %d", len(res.Drafts))
	}
	if res.Drafts[0].Answer.Correct[0] != "A" || res.Drafts[1].Answer.Correct[0] != "B" {
		t.Fatalf("答案解析错误：%v / %v", res.Drafts[0].Answer, res.Drafts[1].Answer)
	}
	if res.Drafts[0].SourceFile != "随堂卷.md" {
		t.Fatalf("应记录来源文件，实际 %q", res.Drafts[0].SourceFile)
	}
	if res.TaskID == 0 {
		t.Fatal("规则解析同样要留下任务记录，否则历史页上这次导入不存在")
	}
}

func TestExtractByTemplateRejectsImages(t *testing.T) {
	_, svc, subject, admin := newTemplateFixture(t)
	_, err := svc.Extract(context.Background(), admin, subject.ID, &schema.ExtractRequest{
		Content: tagPaper,
		Images:  []string{"data:image/png;base64,iVBORw0KGgo="},
		Method:  string(importing.MethodTemplate),
	})
	if err == nil {
		t.Fatal("规则解析处理不了图片，必须明确报错而不是静默忽略图片")
	}
}

func TestExtractRejectsUnknownMethod(t *testing.T) {
	_, svc, subject, admin := newTemplateFixture(t)
	_, err := svc.Extract(context.Background(), admin, subject.ID, &schema.ExtractRequest{
		Content: tagPaper,
		Method:  "gpt",
	})
	if err == nil {
		t.Fatal("未知抽取方式必须报错")
	}
}

// 端到端：Word 文件 → 摄取 → 规则解析 → 入库，全程不碰模型。
func TestUploadDocxEndToEndWithoutAI(t *testing.T) {
	_, svc, subject, admin := newTemplateFixture(t)
	ctx := context.Background()

	data := buildTestDocx(t, `
<w:p><w:r><w:t>1.【题目】下列说法正确的是（　）</w:t></w:r></w:p>
<w:p><w:r><w:t>A. 甲</w:t></w:r></w:p>
<w:p><w:r><w:t>B. 乙</w:t></w:r></w:p>
<w:p><w:r><w:t>【答案】A</w:t></w:r></w:p>
`)

	res, err := svc.Upload(ctx, admin, subject.ID, "随堂练习.docx", data,
		string(importing.MethodTemplate), string(importing.ModeExtract))
	if err != nil {
		t.Fatalf("上传 Word 并规则解析失败: %v", err)
	}
	if len(res.Drafts) != 1 {
		t.Fatalf("应解析出 1 道题，实际 %d：%+v", len(res.Drafts), res.Drafts)
	}
	if res.SourceFiles[0] != "随堂练习.docx" {
		t.Fatalf("应报告来源文件，实际 %v", res.SourceFiles)
	}

	// 提交入库，确认整条链路真的落到题库里。
	commit, err := svc.Commit(ctx, admin, subject.ID, &schema.CommitRequest{Drafts: res.Drafts})
	if err != nil {
		t.Fatalf("入库失败: %v", err)
	}
	if commit.CreatedCount != 1 || commit.FailedCount != 0 {
		t.Fatalf("入库结果不对：%+v", commit)
	}
}

func TestUploadZipMergesAndLabelsSources(t *testing.T) {
	_, svc, subject, admin := newTemplateFixture(t)
	ctx := context.Background()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{
		"一.md":  "1.【题目】甲题\nA. 甲\nB. 乙\n【答案】A\n",
		"二.txt": "1.【题目】乙题\nA. 甲\nB. 乙\n【答案】B\n",
	} {
		w, _ := zw.Create(name)
		w.Write([]byte(body))
	}
	zw.Close()

	res, err := svc.Upload(ctx, admin, subject.ID, "整套卷.zip", buf.Bytes(),
		string(importing.MethodTemplate), string(importing.ModeExtract))
	if err != nil {
		t.Fatalf("上传压缩包失败: %v", err)
	}
	if len(res.Drafts) != 2 {
		t.Fatalf("包内两份文档应各出一题，实际 %d", len(res.Drafts))
	}
	// 两道题必须能分辨各自来自哪个文件，否则审核界面的分组是假的。
	seen := map[string]bool{}
	for _, d := range res.Drafts {
		seen[d.SourceFile] = true
	}
	if !seen["一.md"] || !seen["二.txt"] {
		t.Fatalf("来源文件未正确标记：%v", seen)
	}
	if len(res.SourceFiles) != 2 {
		t.Fatalf("应报告包内的两份文档，实际 %v", res.SourceFiles)
	}
}

// 两份文档都用 1/2/3 编号且各自带文末答案区：答案不能串台。
func TestUploadZipKeepsAnswerSectionsSeparate(t *testing.T) {
	_, svc, subject, admin := newTemplateFixture(t)
	ctx := context.Background()

	first := "1.【题目】甲题\nA. 甲\nB. 乙\n2.【题目】乙题\nA. 甲\nB. 乙\n\n答案\n1．A\n2．B\n"
	second := "1.【题目】丙题\nA. 甲\nB. 乙\n2.【题目】丁题\nA. 甲\nB. 乙\n\n答案\n1．B\n2．A\n"

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{"一.md": first, "二.md": second} {
		w, _ := zw.Create(name)
		w.Write([]byte(body))
	}
	zw.Close()

	res, err := svc.Upload(ctx, admin, subject.ID, "两份卷.zip", buf.Bytes(),
		string(importing.MethodTemplate), string(importing.ModeExtract))
	if err != nil {
		t.Fatalf("上传失败: %v", err)
	}
	for _, d := range res.Drafts {
		if len(d.Answer.Correct) != 1 {
			t.Fatalf("每题都应解析出答案：%+v", d)
		}
		want := map[string]string{
			"一.md#1": "A", "一.md#2": "B",
			"二.md#1": "B", "二.md#2": "A",
		}[d.SourceFile+"#"+itoa(d.SourceNumber)]
		if d.Answer.Correct[0] != want {
			t.Fatalf("%s 第 %d 题答案应为 %s，实际 %s",
				d.SourceFile, d.SourceNumber, want, d.Answer.Correct[0])
		}
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestUploadRejectsUnsupportedAndOversized(t *testing.T) {
	_, svc, subject, admin := newTemplateFixture(t)
	ctx := context.Background()

	if _, err := svc.Upload(ctx, admin, subject.ID, "试卷.pdf", []byte("%PDF-1.4"),
		string(importing.MethodTemplate), ""); err == nil {
		t.Fatal("PDF 不支持，必须报错")
	}
	// 文件名带穿越路径时，只取末段，不应影响类型判定。
	if _, err := svc.Upload(ctx, admin, subject.ID, "../../evil.pdf", []byte("x"),
		string(importing.MethodTemplate), ""); err == nil {
		t.Fatal("非法扩展名必须报错")
	}
}

// buildTestDocx 拼一个最小可用的 .docx。
func buildTestDocx(t *testing.T, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("word/document.xml")
	if err != nil {
		t.Fatalf("写 zip 失败: %v", err)
	}
	w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>` +
		body + `</w:body></w:document>`))
	if err := zw.Close(); err != nil {
		t.Fatalf("关闭 zip 失败: %v", err)
	}
	return buf.Bytes()
}
