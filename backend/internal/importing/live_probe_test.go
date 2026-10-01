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

// 对照实验：同一段题目文本，一次不带图、一次带 N 张答案页。
//
// 要回答的是"慢在图片还是慢在输出"：
//   - 若是图片慢，带图会明显变慢而输出 token 数变化不大；
//   - 若是输出慢，带图应当让**输出 token 明显下降**（转写而不是推导）。
//
// QMB_PROBE_IMAGES 指定带几张答案页。
func TestLiveImageCostProbe(t *testing.T) {
	baseURL := os.Getenv("QMB_LIVE_BASE_URL")
	modelName := os.Getenv("QMB_LIVE_MODEL")
	apiKey := os.Getenv("QMB_LIVE_API_KEY")
	pdfPath := os.Getenv("QMB_PDF")
	answerDir := os.Getenv("QMB_ANSWER_DIR")
	if baseURL == "" || modelName == "" || apiKey == "" || pdfPath == "" || answerDir == "" {
		t.Skip("未配置 QMB_LIVE_* / QMB_PDF / QMB_ANSWER_DIR")
	}

	// 只取第 0 块（全是选择题），把变量压到最小。
	raw, err := os.ReadFile(pdfPath)
	if err != nil {
		t.Fatalf("读取 PDF 失败: %v", err)
	}
	res, err := Ingest(raw, "paper.pdf", nil)
	if err != nil {
		t.Fatalf("摄取失败: %v", err)
	}
	chunks := SplitForExtraction(res.Docs[0].Text, 0)
	if len(chunks) == 0 {
		t.Fatal("没有切出任何块")
	}
	subject := chunks[0].Text
	t.Logf("受测文本：%d 字节，%s", len(subject), chunks[0].Label)

	files, _ := filepath.Glob(filepath.Join(answerDir, "*.jpg"))
	sort.Strings(files)

	provider := ai.NewOpenAI(ai.Config{
		Name: "live", InterfaceType: ai.TypeOpenAI,
		BaseURL: baseURL, APIKey: apiKey,
		Timeout: 8 * time.Minute, MaxRetries: 0,
	})

	run := func(label string, images []string, withImages int) {
		opts := PromptOptions{Mode: ModeExtract, SubjectName: "数学", AnswerImages: withImages}
		ctx, cancel := context.WithTimeout(context.Background(), 9*time.Minute)
		defer cancel()

		start := time.Now()
		got, err := provider.ExtractQuestions(ctx, ai.ExtractRequest{
			Model:     modelName,
			Prompt:    BuildSystemPrompt(opts),
			Content:   BuildUserContent(subject, opts),
			Images:    images,
			MaxTokens: 32768,
		})
		took := time.Since(start).Round(time.Second)
		if err != nil {
			t.Errorf("[%s] %v 后失败: %v", label, took, err)
			return
		}
		answered := 0
		for _, q := range got {
			if q.Answer != "" {
				answered++
			}
		}
		t.Logf("[%s] %v，%d 道题，其中 %d 道有答案；约 %.0f 输出 token/秒",
			label, took, len(got), answered, float64(len(got))*1400/took.Seconds())
	}

	run("纯文本", nil, 0)

	n := 0
	if v := os.Getenv("QMB_PROBE_IMAGES"); v != "" {
		fmtSscan(v, &n)
	}
	if n <= 0 {
		n = 2
	}
	if n > len(files) {
		n = len(files)
	}
	var urls []string
	for _, f := range files[:n] {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		u, err := visionprep.ToDataURL(b)
		if err != nil {
			continue
		}
		urls = append(urls, u)
	}
	payload := 0
	for _, u := range urls {
		payload += len(u)
	}
	t.Logf("答案页 %d 张，内联后 %d KB", len(urls), payload>>10)
	run("带答案页", urls, len(urls))
}

func fmtSscan(s string, n *int) {
	v := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			break
		}
		v = v*10 + int(s[i]-'0')
	}
	*n = v
}
