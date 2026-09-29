package latex

import (
	"strings"
	"testing"

	"github.com/NanamiKyoka/questionmind/internal/export"
)

// 留白样式只按行高留出等高的垂直空白，不画任何横线。
func TestWriteAnswerSpaceBlankKeepsVerticalRoom(t *testing.T) {
	res, err := Write(&export.Document{Blocks: []export.Block{
		&export.AnswerSpace{Lines: 3},
	}})
	if err != nil {
		t.Fatalf("Write 失败: %v", err)
	}
	tex := string(res.TeX)

	if got := strings.Count(tex, "\\mbox{}"); got != 3 {
		t.Fatalf("应输出 3 行留白，实际 %d 行:\n%s", got, tex)
	}
	if strings.Contains(tex, "\\rule{") {
		t.Fatalf("留白样式不应画横线:\n%s", tex)
	}
}

// lined 样式逐行画满宽横线，与 DOCX 端"段落下边框"落在同一基线位置。
func TestWriteAnswerSpaceLinedDrawsRules(t *testing.T) {
	res, err := Write(&export.Document{Blocks: []export.Block{
		&export.AnswerSpace{Lines: 4, Lined: true},
	}})
	if err != nil {
		t.Fatalf("Write 失败: %v", err)
	}
	tex := string(res.TeX)

	if got := strings.Count(tex, `\rule{\linewidth}{0.4pt}`); got != 4 {
		t.Fatalf("4 行应画出 4 条满宽横线，实际 %d 条:\n%s", got, tex)
	}
	// 横线必须顶到左边界，否则会缩进成一列短横线。
	if !strings.Contains(tex, `\noindent\rule{\linewidth}`) {
		t.Fatalf("横线应带 \\noindent:\n%s", tex)
	}
}

// 行高常量与 DOCX 端 Normal 样式的 1.5 倍行距对齐，
// 两种格式的答题区才不会一高一矮。
func TestWriteAnswerSpaceUsesSharedLineHeight(t *testing.T) {
	res, err := Write(&export.Document{Blocks: []export.Block{
		&export.AnswerSpace{Lines: 2, Lined: true},
	}})
	if err != nil {
		t.Fatalf("Write 失败: %v", err)
	}
	tex := string(res.TeX)

	if got := strings.Count(tex, `\vspace{`+answerSpaceLineHeight+`}`); got != 2 {
		t.Fatalf("每行都应保留 %s 的行高，实际 %d 行:\n%s", answerSpaceLineHeight, got, tex)
	}
}

// 行数非法时按 1 行兜底：答题区在导出结果里凭空消失比行数差一点更难排查。
func TestWriteAnswerSpaceClampsLines(t *testing.T) {
	for _, lines := range []int{0, -5} {
		res, err := Write(&export.Document{Blocks: []export.Block{
			&export.AnswerSpace{Lines: lines, Lined: true},
		}})
		if err != nil {
			t.Fatalf("Write 失败: %v", err)
		}
		if got := strings.Count(string(res.TeX), `\rule{\linewidth}`); got != 1 {
			t.Fatalf("lines=%d 应兜底为 1 行，实际 %d 行", lines, got)
		}
	}
}
