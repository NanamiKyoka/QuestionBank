package latex

import (
	"strings"
	"testing"

	"github.com/NanamiKyoka/questionmind/internal/export"
)

func TestWriteProducesCompilableSkeleton(t *testing.T) {
	res, err := Write(&export.Document{
		Title: "高一数学月考卷",
		Meta:  map[string]string{"subject": "数学"},
		Blocks: []export.Block{
			&export.Heading{Level: 1, Runs: []export.Inline{&export.Text{Value: "一、选择题"}}},
			&export.Paragraph{Runs: []export.Inline{&export.Text{Value: "正文"}}},
			&export.PageBreak{},
		},
	})
	if err != nil {
		t.Fatalf("Write 失败: %v", err)
	}
	tex := string(res.TeX)

	for _, want := range []string{
		`\documentclass[11pt,a4paper]{ctexart}`,
		`\usepackage{amsmath,amssymb}`,
		`\usepackage{graphicx}`,
		`\begin{document}`,
		`\end{document}`,
		`\section*{一、选择题}`,
		`\newpage`,
		`\maketitle`,
	} {
		if !strings.Contains(tex, want) {
			t.Fatalf("应包含 %q，实际:\n%s", want, tex)
		}
	}
}

// LaTeX 原生支持数学模式，公式应原样输出而不是做格式转换。
func TestWriteKeepsMathAsIs(t *testing.T) {
	res, err := Write(&export.Document{
		Blocks: []export.Block{
			&export.Paragraph{Runs: []export.Inline{
				&export.Text{Value: "已知 "},
				&export.Math{LaTeX: `f(x)=\frac{1}{2}x^2+\sqrt{x}`},
				&export.Text{Value: " 求导"},
			}},
		},
	})
	if err != nil {
		t.Fatalf("Write 失败: %v", err)
	}
	tex := string(res.TeX)
	if !strings.Contains(tex, `$f(x)=\frac{1}{2}x^2+\sqrt{x}$`) {
		t.Fatalf("公式应原样输出: %s", tex)
	}
	// 公式里不能出现转义后的 \frac，否则公式会被破坏。
	if strings.Contains(tex, `\textbackslash{}frac`) {
		t.Fatalf("公式不应被转义: %s", tex)
	}
}

// 已经带 $ 定界的公式不应出现 $$$$。
func TestWriteStripsMathDelimiters(t *testing.T) {
	res, _ := Write(&export.Document{
		Blocks: []export.Block{
			&export.Paragraph{Runs: []export.Inline{&export.Math{LaTeX: `$x^2$`}}},
		},
	})
	tex := string(res.TeX)
	if strings.Contains(tex, "$$x^2$$") {
		t.Fatalf("定界符重复: %s", tex)
	}
	if !strings.Contains(tex, "$x^2$") {
		t.Fatalf("应输出单层定界: %s", tex)
	}
}

// LaTeX 保留字符必须转义，否则编译会失败。
func TestWriteEscapesSpecialCharacters(t *testing.T) {
	res, _ := Write(&export.Document{
		Blocks: []export.Block{
			&export.Paragraph{Runs: []export.Inline{
				&export.Text{Value: `100% #1 a_b & c {d} ~ e^f \ g`},
			}},
		},
	})
	tex := string(res.TeX)
	for _, want := range []string{`\%`, `\#`, `\_`, `\&`, `\{`, `\}`, `\textasciitilde{}`, `\textasciicircum{}`, `\textbackslash{}`} {
		if !strings.Contains(tex, want) {
			t.Fatalf("缺少转义 %q: %s", want, tex)
		}
	}
}

func TestWriteEscapesBackslashFirst(t *testing.T) {
	// 反斜杠若未优先替换，后续插入的反斜杠会被二次转义成 \textbackslash{}。
	got := Escape(`\`)
	if got != `\textbackslash{}` {
		t.Fatalf("反斜杠应转义为 \\textbackslash{}，实际 %q", got)
	}
	if strings.Contains(got, `\textbackslash{}\textbackslash{}`) {
		t.Fatalf("发生了二次转义: %q", got)
	}
}

func TestWriteImageCollectsFiles(t *testing.T) {
	res, err := Write(&export.Document{
		Blocks: []export.Block{
			&export.Paragraph{Runs: []export.Inline{
				&export.Image{Data: []byte("fake-png"), Ext: "png", WidthPt: 120},
			}},
		},
	})
	if err != nil {
		t.Fatalf("Write 失败: %v", err)
	}
	if len(res.Images) != 1 {
		t.Fatalf("应收集 1 张图片，实际 %d", len(res.Images))
	}
	for name, data := range res.Images {
		if !strings.HasPrefix(name, "images/") {
			t.Fatalf("图片应放在 images/ 下，实际 %s", name)
		}
		if string(data) != "fake-png" {
			t.Fatal("图片内容应原样保留")
		}
	}
	tex := string(res.TeX)
	if !strings.Contains(tex, `\includegraphics[width=120pt]{images/img1.png}`) {
		t.Fatalf("应引用图片并指定宽度: %s", tex)
	}
}

// jpg 在 LaTeX 里要写成 jpeg。
func TestWriteNormalisesJPEGExtension(t *testing.T) {
	res, _ := Write(&export.Document{
		Blocks: []export.Block{
			&export.Paragraph{Runs: []export.Inline{&export.Image{Data: []byte("x"), Ext: "jpg"}}},
		},
	})
	for name := range res.Images {
		if !strings.HasSuffix(name, ".jpeg") {
			t.Fatalf("jpg 应规范化为 jpeg，实际 %s", name)
		}
	}
}

func TestWriteTable(t *testing.T) {
	res, _ := Write(&export.Document{
		Blocks: []export.Block{
			&export.Table{Rows: [][]export.Cell{
				{{Blocks: []export.Block{&export.Paragraph{Runs: []export.Inline{&export.Text{Value: "题号"}}}}},
					{Blocks: []export.Block{&export.Paragraph{Runs: []export.Inline{&export.Text{Value: "答案"}}}}}},
				{{Blocks: []export.Block{&export.Paragraph{Runs: []export.Inline{&export.Text{Value: "1"}}}}},
					{Blocks: []export.Block{&export.Paragraph{Runs: []export.Inline{&export.Text{Value: "B"}}}}}},
			}},
		},
	})
	tex := string(res.TeX)
	if !strings.Contains(tex, `\begin{tabular}`) || !strings.Contains(tex, `\end{tabular}`) {
		t.Fatalf("应输出表格环境: %s", tex)
	}
	if !strings.Contains(tex, "题号 & 答案") {
		t.Fatalf("单元格应以 & 分隔: %s", tex)
	}
}

func TestWriteNilDocument(t *testing.T) {
	if _, err := Write(nil); err == nil {
		t.Fatal("nil 文档应返回错误")
	}
}

func TestWriteEmptyDocumentStillCompiles(t *testing.T) {
	res, err := Write(&export.Document{})
	if err != nil {
		t.Fatalf("空文档不应报错: %v", err)
	}
	tex := string(res.TeX)
	if !strings.Contains(tex, `\begin{document}`) || !strings.Contains(tex, `\end{document}`) {
		t.Fatalf("空文档也应是完整的 LaTeX 文档: %s", tex)
	}
}
