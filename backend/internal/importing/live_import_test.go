// 本文件是**打真实模型**的开发工具，用来回答两个凭猜猜不出来的问题：
//
//  1. 一次调用到底要吐多少输出 token、耗时多少；
//  2. 切成 N 块之后，每块能不能在供应商的超时上限内跑完。
//
// 这两点决定了分块参数（extractionMaxQuestionsPerChunk、ExtractionConcurrency）
// 取什么值，而它们在不同模型上差着数量级——所以只能量，不能猜。
//
// 需要同时设置这些环境变量才运行：
//
//	QMB_LIVE_BASE_URL  例如 https://api.example.com/v1
//	QMB_LIVE_MODEL    模型名
//	QMB_LIVE_API_KEY  密钥（只从环境变量读，绝不写进仓库）
//	QMB_PDF           被导入的 PDF 路径
//	QMB_CHUNK_MODE    skip = 只打印分块结果，不调模型
//
// 它不属于提交前检查：没有网络、没有密钥，也就没有结果。
package importing

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/NanamiKyoka/questionmind/internal/ai"
)

// TestLiveSplitOnly 只跑切分，不调模型。
//
// 调模型一次要好几分钟，而"这份卷子被切成了什么样"是零成本就能看到的。
// 改完分块参数先跑这个，别拿几分钟去换一个一眼可见的答案。
func TestLiveSplitOnly(t *testing.T) {
	if _, _, _, ok := liveFixture(t); !ok {
		return
	}
}

// TestLiveChunksSequentially 逐块串行跑。
//
// 串行而不是并发，是为了把两件事分开：串行能看出"单块本身能不能跑完"
// （即供应商的上限在哪里），并发只告诉你总耗时。
func TestLiveChunksSequentially(t *testing.T) {
	chunks, provider, opts, ok := liveFixture(t)
	if !ok {
		return
	}
	if os.Getenv("QMB_CHUNK_MODE") == "skip" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	for i, c := range chunks {
		start := time.Now()
		got, err := provider.ExtractQuestions(ctx, ai.ExtractRequest{
			Model:     os.Getenv("QMB_LIVE_MODEL"),
			Prompt:    BuildSystemPrompt(opts),
			Content:   BuildUserContent(c.Text, opts),
			MaxTokens: 32768,
		})
		if err != nil {
			t.Errorf("块[%d] %v 后失败: %v", i, time.Since(start).Round(time.Second), err)
			continue
		}
		t.Logf("块[%d] %d 字节 -> %v，%d 道题",
			i, len(c.Text), time.Since(start).Round(time.Second), len(got))
		for _, q := range got {
			t.Logf("   · %s | %s", q.QuestionType, liveFirstLine(q.Stem))
		}
	}
}

// liveFixture 准备好真实模型与分块结果，并打印分块概览。
func liveFixture(t *testing.T) ([]Chunk, *ai.OpenAIProvider, PromptOptions, bool) {
	t.Helper()

	baseURL := os.Getenv("QMB_LIVE_BASE_URL")
	modelName := os.Getenv("QMB_LIVE_MODEL")
	apiKey := os.Getenv("QMB_LIVE_API_KEY")
	pdfPath := os.Getenv("QMB_PDF")
	if baseURL == "" || modelName == "" || apiKey == "" || pdfPath == "" {
		t.Skip("未配置 QMB_LIVE_BASE_URL / QMB_LIVE_MODEL / QMB_LIVE_API_KEY / QMB_PDF")
	}

	// 这个用例的地址来自开发者显式设置的环境变量，而且常常指向本机网关
	// （Ollama、vLLM），因此放开 ai 包对内网地址的限制。
	ai.SetAllowPrivateURL(true)

	raw, err := os.ReadFile(pdfPath)
	if err != nil {
		t.Fatalf("读取 PDF 失败: %v", err)
	}
	start := time.Now()
	res, err := Ingest(context.Background(), raw, "paper.pdf", nil)
	if err != nil {
		t.Fatalf("摄取失败: %v", err)
	}
	t.Logf("摄取耗时 %v，正文 %d 字节", time.Since(start).Round(time.Millisecond), len(res.Docs[0].Text))

	chunks := SplitForExtraction(res.Docs[0].Text, 0)
	t.Logf("切成 %d 块：", len(chunks))
	for i, c := range chunks {
		t.Logf("  [%d] %d 字节 · %s", i, len(c.Text), c.Label)
	}

	provider := ai.NewOpenAI(ai.Config{
		Name: "live", InterfaceType: ai.TypeOpenAI,
		BaseURL: baseURL, APIKey: apiKey,
		Timeout: 6 * time.Minute, MaxRetries: 0,
	})
	return chunks, provider, PromptOptions{Mode: ModeExtract, SubjectName: "数学"}, true
}

func liveFirstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
