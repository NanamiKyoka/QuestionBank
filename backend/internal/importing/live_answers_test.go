package importing

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/NanamiKyoka/questionmind/internal/ai"
	"github.com/NanamiKyoka/questionmind/internal/visionprep"
)

// 真实场景：一份**没有答案**的试卷 PDF + 同一份试卷的答案页图片。
//
// 这是录题最常见也最痛的一条路：有卷子没答案时，模型只能把二十道
// 解答题一道道算出来；把答案页一并发过去，它就只需要转写。
// 这个用例就是量这两者差多少。
//
// 需要的��境变量见 live_import_test.go 的说明，另外：
//
//	QMB_ANSWER_DIR  答案页图片所在目录（*.jpg / *.png）
func TestLivePaperWithAnswerPages(t *testing.T) {
	chunks, provider, opts, ok := liveFixture(t)
	if !ok {
		return
	}
	_ = chunks

	answerDir := os.Getenv("QMB_ANSWER_DIR")
	if answerDir == "" {
		t.Skip("未配置 QMB_ANSWER_DIR，跳过带答案页的对照测试")
	}
	files, err := filepath.Glob(filepath.Join(answerDir, "*"))
	if err != nil {
		t.Fatalf("列出答案页失败: %v", err)
	}
	sort.Strings(files)

	var dataURLs []string
	var rawBytes int
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		url, err := visionprep.ToDataURL(raw)
		if err != nil {
			t.Errorf("%s 处理失败: %v", filepath.Base(f), err)
			continue
		}
		rawBytes += len(raw)
		dataURLs = append(dataURLs, url)
	}
	if len(dataURLs) == 0 {
		t.Fatalf("没有可用的答案页图片（%s）", answerDir)
	}
	payloadBytes := 0
	for _, u := range dataURLs {
		payloadBytes += len(u)
	}
	t.Logf("答案页 %d 张：原图合计 %d KB，内联后 %d KB",
		len(dataURLs), rawBytes>>10, payloadBytes>>10)

	opts.AnswerImages = len(dataURLs)
	start := time.Now()
	got, err := provider.ExtractQuestions(context.Background(), ai.ExtractRequest{
		Model:     os.Getenv("QMB_LIVE_MODEL"),
		Prompt:    BuildSystemPrompt(opts),
		Content:   BuildUserContent(paperText(t), opts),
		Images:    dataURLs,
		MaxTokens: 32768,
	})
	if err != nil {
		t.Fatalf("带答案页的抽取失败: %v", err)
	}
	t.Logf("带答案页：%v，抽出 %d 道题", time.Since(start).Round(time.Second), len(got))

	var withAnswer int
	for i, q := range got {
		if q.Answer != "" {
			withAnswer++
		}
		t.Logf("%2d. %s | 答案=%q | %s",
			i+1, q.QuestionType, truncateLive(q.Answer, 30), truncateLive(q.Stem, 70))
	}
	t.Logf("有答案的题目：%d / %d", withAnswer, len(got))
}

// paperText 重新摄取一次被测 PDF，取出正文。
func paperText(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(os.Getenv("QMB_PDF"))
	if err != nil {
		t.Fatalf("读取 PDF 失败: %v", err)
	}
	res, err := Ingest(context.Background(), raw, "paper.pdf", nil)
	if err != nil {
		t.Fatalf("摄取失败: %v", err)
	}
	return res.Docs[0].Text
}

func truncateLive(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
