package docx

import (
	"strings"
	"testing"

	"github.com/NanamiKyoka/questionmind/internal/export"
)

// 答题区在 Word 里靠"每行一个空段落"实现——行高沿用 Normal 样式的 1.5 倍行距，
// 与 LaTeX 端的 1.5\baselineskip 是同一套版面参数。
func TestWriteAnswerSpaceEmitsOneParagraphPerLine(t *testing.T) {
	data, err := Write(&export.Document{Blocks: []export.Block{
		&export.AnswerSpace{Lines: 3},
	}})
	if err != nil {
		t.Fatalf("Write 失败: %v", err)
	}
	body := string(openParts(t, data)["word/document.xml"])

	// sectPr 是文档的收尾标签，不算段落。
	if got := strings.Count(body, "<w:p>"); got != 3 {
		t.Fatalf("应输出 3 个空段落，实际 %d 个: %s", got, truncate(body))
	}
	// 留白样式不该画出任何框线。
	if strings.Contains(body, "<w:pBdr>") {
		t.Fatalf("留白样式不应输出答题横线: %s", truncate(body))
	}
}

// lined 样式给每个段落加一条下边框，横线才真的出现在卷面上。
func TestWriteAnswerSpaceLinedAddsBottomBorder(t *testing.T) {
	data, err := Write(&export.Document{Blocks: []export.Block{
		&export.AnswerSpace{Lines: 4, Lined: true},
	}})
	if err != nil {
		t.Fatalf("Write 失败: %v", err)
	}
	body := string(openParts(t, data)["word/document.xml"])

	if got := strings.Count(body, "<w:pBdr>"); got != 4 {
		t.Fatalf("4 行应输出 4 条答题横线，实际 %d 条: %s", got, truncate(body))
	}
	if !strings.Contains(body, `<w:bottom w:val="single"`) {
		t.Fatalf("横线应为段落下边框: %s", truncate(body))
	}
	// 只画下边框：左右或上下都会多出线框，不是答题横线该有的样子。
	if strings.Contains(body, "<w:top w:val=") {
		t.Fatalf("答题横线只应有下边框: %s", truncate(body))
	}
}

// 行数非法时按 1 行兜底：答题区在导出结果里凭空消失比行数差一点更难排查。
func TestWriteAnswerSpaceClampsLines(t *testing.T) {
	for _, lines := range []int{0, -5} {
		data, err := Write(&export.Document{Blocks: []export.Block{
			&export.AnswerSpace{Lines: lines, Lined: true},
		}})
		if err != nil {
			t.Fatalf("Write 失败: %v", err)
		}
		body := string(openParts(t, data)["word/document.xml"])
		if got := strings.Count(body, "<w:pBdr>"); got != 1 {
			t.Fatalf("lines=%d 应兜底为 1 行，实际 %d 行", lines, got)
		}
	}
}
