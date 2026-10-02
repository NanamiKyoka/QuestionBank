package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"strings"
	"sync"
	"testing"

	"github.com/NanamiKyoka/questionmind/internal/ai"
)

// 假调用：记录每次拿到了几页图，页数达到阈值就判定截断。
type recordingCall struct {
	mu    sync.Mutex
	sizes []int
	// failAt 达到这么多页就判定截断；0 表示永不截断。
	failAt int
}

func (r *recordingCall) call(text string, imgs []string, pageCount, answerCount int) ([]ai.ExtractedQuestion, error) {
	r.mu.Lock()
	r.sizes = append(r.sizes, pageCount)
	limit := r.failAt
	r.mu.Unlock()

	if limit > 0 && pageCount >= limit {
		return nil, fmt.Errorf("%w：输出被上限截断", ai.ErrTruncated)
	}
	out := make([]ai.ExtractedQuestion, 0, pageCount)
	for i := 0; i < pageCount; i++ {
		// 每批都从 1 编号：合并时必须靠 reidentify 改掉。
		out = append(out, ai.ExtractedQuestion{
			TempID: "1",
			Stem:   fmt.Sprintf("第%d页的题", pageCount),
		})
	}
	return out, nil
}

func (r *recordingCall) recorded() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int(nil), r.sizes...)
}

func pages(n int) []string {
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, fmt.Sprintf("data:image/png;base64,page%d", i))
	}
	return out
}

// 页图按页分批：防止输出被截断的第一道防线。
//
// 回归点在于旧实现"带图就不切块"——十页的 PDF 一次全发出去，
// 32K 上限装不下，而截断的 JSON 残缺到一道题都读不出来。
func TestPageBatchesSplitByPage(t *testing.T) {
	rec := &recordingCall{}
	svc := &ImportService{}

	got, warns, err := svc.extractPageBatches(
		context.Background(), rec.call, pages(5), nil, 2, 0, nil)
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	for _, n := range rec.recorded() {
		if n > 2 {
			t.Fatalf("单次调用送了 %d 页，超过每批上限 2", n)
		}
	}
	if len(got) != 5 {
		t.Fatalf("应得到 5 道题，实际 %d", len(got))
	}
	if len(warns) != 0 {
		t.Fatalf("不该有告警，实际 %v", warns)
	}
}

// 生产用的批大小必须真的是 1：实测每页约 1.7 万输出 token，
// 32K 上限下两页就贴边，放宽这个值等于把整份卷子押在"刚好装得下"上。
func TestPagesPerCallIsOne(t *testing.T) {
	if pagesPerCall != 1 {
		t.Fatalf("pagesPerCall 应为 1（保守优先于少一次往返），实际 %d", pagesPerCall)
	}
}

// 合并后临时编号必须全局唯一，否则入库时母子关系会错位。
func TestPageBatchesReidentifyAcrossBatches(t *testing.T) {
	rec := &recordingCall{}
	svc := &ImportService{}

	got, _, err := svc.extractPageBatches(
		context.Background(), rec.call, pages(4), nil, 2, 0, nil)
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	seen := map[string]bool{}
	for i, q := range got {
		if seen[q.TempID] {
			t.Fatalf("第 %d 题的 temp_id 重复: %q（每批都从 1 编号，必须重排）", i, q.TempID)
		}
		seen[q.TempID] = true
	}
}

// 答案页要对**每一批**都带上：它是对整份卷子的一次性说明。
func TestPageBatchesAttachAnswerPagesToEveryCall(t *testing.T) {
	var mu sync.Mutex
	var answerSeen []int

	svc := &ImportService{}
	call := func(text string, imgs []string, pageCount, answerCount int) ([]ai.ExtractedQuestion, error) {
		mu.Lock()
		answerSeen = append(answerSeen, answerCount)
		mu.Unlock()
		return []ai.ExtractedQuestion{{TempID: "1", Stem: "x"}}, nil
	}
	_, _, err := svc.extractPageBatches(context.Background(), call,
		pages(4), []string{"answer1", "answer2"}, 2, 2, nil)
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if len(answerSeen) != 2 {
		t.Fatalf("4 页按每批 2 页应是 2 次调用，实际 %d", len(answerSeen))
	}
	for i, n := range answerSeen {
		if n != 2 {
			t.Fatalf("第 %d 次调用只带了 %d 张答案页，应当每次都带 2 张", i, n)
		}
	}
}

// 撞上输出上限就拆半重试——这才是每批大小的真正保险。
func TestTruncatedBatchSplitsInHalf(t *testing.T) {
	// 每批 3 页必然超限（3>=3）；拆成 1+2 之后都能过。
	rec := &recordingCall{failAt: 3}
	svc := &ImportService{}

	got, _, err := svc.extractPageBatches(
		context.Background(), rec.call, pages(3), nil, 3, 0, nil)
	if err != nil {
		t.Fatalf("拆半后应当成功: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("拆半后应拿到 3 道题，实际 %d", len(got))
	}
	if !containsInt(rec.recorded(), 3) || !containsInt(rec.recorded(), 1) {
		t.Fatalf("应当先按 3 页试一次、再拆成更小的批次，实际 %v", rec.recorded())
	}
}

// 单页也装不下时，把这一页的**图**横向切开再试——按页拆已经无能为力。
//
// 这是真实故障的形态：实测 2026 全国 1 卷第 1 页只有 11 道题，
// 带解析就要 3 万多 token，一整页塞不进 32K。
//
// 假调用只在**第一次**截断，之后就放行：真实场景里是"这一页太密"，
// 拆成两半就装得下了。若每次都截断，那测到的会是"切到上限仍失败"，
// 那是另一个用例（TestTruncationAtSinglePageIsReported）该管的事。
func TestSinglePageTruncationSlicesTheImage(t *testing.T) {
	svc := &ImportService{}
	var mu sync.Mutex
	calls := 0
	call := func(text string, imgs []string, pageCount, answerCount int) ([]ai.ExtractedQuestion, error) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			return nil, fmt.Errorf("%w：输出被上限截断", ai.ErrTruncated)
		}
		return []ai.ExtractedQuestion{{TempID: "1", Stem: "切开后抽到的题"}}, nil
	}

	got, _, err := svc.extractPageBatches(
		context.Background(), call, []string{tallPageDataURL(t)}, nil, 1, 0, nil)
	if err != nil {
		t.Fatalf("切图后应当成功: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("切成两半后应当得到 2 道题，实际 %d", len(got))
	}
	if calls < 3 {
		t.Fatalf("应当是「整页失败一次 + 两半各一次」，实际调用 %d 次", calls)
	}
}

// 切图也救不回来时如实报错：每段都截断说明内容密度已经超上限。
func TestTruncationAtSinglePageIsReported(t *testing.T) {
	// 用一张切不动的假图（不是有效 data URL），切图会失败并原样报出截断。
	rec := &recordingCall{failAt: 1}
	svc := &ImportService{}

	_, _, err := svc.extractPageBatches(
		context.Background(), rec.call, []string{"data:image/png;base64,!!not-base64!!"}, nil, 1, 0, nil)
	if err == nil {
		t.Fatal("切图也失败时应当报错，而不是静默返回空结果")
	}
	if !strings.Contains(err.Error(), "一道") {
		t.Fatalf("错误信息应当说清是整份都没识别出来，实际 %v", err)
	}
}

// 一批失败不影响其余：部分成功远好过整份重来。
func TestFailedBatchDoesNotDiscardOthers(t *testing.T) {
	svc := &ImportService{}
	var mu sync.Mutex
	calls := 0
	call := func(text string, imgs []string, pageCount, answerCount int) ([]ai.ExtractedQuestion, error) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		// 只有第一次调用失败，模拟"某一批上游报错"。
		if n == 1 {
			return nil, fmt.Errorf("上游 500")
		}
		return []ai.ExtractedQuestion{{TempID: "1", Stem: "好页"}}, nil
	}

	got, warns, err := svc.extractPageBatches(
		context.Background(), call, pages(4), nil, 2, 0, nil)
	if err != nil {
		t.Fatalf("部分失败不该整体报错: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("应当保留成功那一批的题，实际 %d", len(got))
	}
	if !strings.Contains(strings.Join(warns, ""), "识别失败") {
		t.Fatalf("必须如实报告哪部分缺失，实际告警 %v", warns)
	}
}

func TestChunkStringsKeepsOrder(t *testing.T) {
	got := chunkStrings([]string{"a", "b", "c", "d", "e"}, 2)
	if len(got) != 3 {
		t.Fatalf("5 个元素按 2 个一批应是 3 批，实际 %d", len(got))
	}
	var flat []string
	for _, g := range got {
		flat = append(flat, g...)
	}
	if strings.Join(flat, "") != "abcde" {
		t.Fatalf("分批必须保序且不丢元素，实际 %v", flat)
	}
}

func TestChunkStringsSingleBatchWhenSmall(t *testing.T) {
	got := chunkStrings([]string{"a"}, pagesPerCall)
	if len(got) != 1 || len(got[0]) != 1 {
		t.Fatalf("单页应当只有一批，实际 %v", got)
	}
}

func containsInt(list []int, want int) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// tallPageDataURL 造一张足够高的真图，让切图这条路真的能走通。
//
// 尺寸取得比 8×parts 的下限大得多：切得太窄时 SplitVertical 会判定
// "切不动"并原样返回，那样这个用例测到的就不是切图，而是"切图失败"了。
func tallPageDataURL(t *testing.T) string {
	t.Helper()
	const w, h = 120, 600
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			// 上半黑、下半白，中间留一条空白，正好给 findBlankNear 挑。
			ink := y < h/2
			v := uint8(250)
			if ink {
				v = 20
			}
			img.Set(x, y, color.RGBA{R: v, G: v, B: v, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("编码失败: %v", err)
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}
