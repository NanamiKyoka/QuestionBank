package pdfium

import (
	"bytes"
	"context"
	"os"
	"testing"
)

// 一份自造的最小 PDF：不依赖仓库外的样本文件。
//
// 为什么不用现成的试卷 PDF：那个文件不在后端模块里，CI 上没有；
// 而"能不能渲染"这件事用一页 A4 就足够验证，不必引入二进制样本。
// 真正的"渲染出来够不够清晰给模型看"由人工与端到端验证负责，
// 那不是单元测试该管的。
const miniPDF = `%PDF-1.4
1 0 obj
<< /Type /Catalog /Pages 2 0 R >>
endobj
2 0 obj
<< /Type /Pages /Kids [3 0 R] /Count 1 >>
endobj
3 0 obj
<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 200]
   /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>
endobj
4 0 obj
<< /Length 62 >>
stream
BT /F1 18 Tf 20 100 Td (Question 1: 2+2=? and x^2) Tj ET
endstream
endobj
5 0 obj
<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>
endobj
trailer
<< /Size 6 /Root 1 0 R >>
`

func TestRenderProducesPNG(t *testing.T) {
	pages, err := Render(context.Background(), []byte(miniPDF))
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if len(pages) != 1 {
		t.Fatalf("应渲染出 1 页，实际 %d", len(pages))
	}

	p := pages[0]
	if len(p.PNG) < 8 || string(p.PNG[1:4]) != "PNG" {
		t.Fatalf("拿到��不是合法 PNG：前 8 字节 %q", p.PNG[:min(8, len(p.PNG))])
	}
	if p.Width <= 0 || p.Height <= 0 {
		t.Fatalf("页面尺寸异常：%dx%d", p.Width, p.Height)
	}
	if p.Index != 0 {
		t.Fatalf("页码应为 0，实际 %d", p.Index)
	}
	t.Logf("渲染出 %dx%d，%.1f KB", p.Width, p.Height, float64(len(p.PNG))/1024)
}

func TestRenderRejectsEmptyInput(t *testing.T) {
	if _, err := Render(context.Background(), nil); err == nil {
		t.Fatal("空内容应当报错，而不是返回空结果")
	}
}

func TestRenderRejectsGarbage(t *testing.T) {
	// 垃圾字节：必须给出明确错误，而不是 panic 或返回空页。
	if _, err := Render(context.Background(), bytes.Repeat([]byte("x"), 4096)); err == nil {
		t.Fatal("非 PDF 内容应当报错")
	}
}

// 真实试卷样本：仓库根下的 reference/ 里放着用户自己的卷子。
//
// 用不上就跳过——它是加分项，不是这道测试成立的前提。
func TestRenderRealPaper(t *testing.T) {
	path := "../../../reference/2026全国1(山东,广东,湖南,湖北,河北,江苏,福建,浙江,河南,江西,安徽).pdf"
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("没有参考 PDF 样本（%v），跳过", err)
	}

	pages, err := Render(context.Background(), data)
	if err != nil {
		t.Fatalf("渲染真实试卷失败: %v", err)
	}
	var total int
	for i, p := range pages {
		total += len(p.PNG)
		if len(p.PNG) < 8 || string(p.PNG[1:4]) != "PNG" {
			t.Fatalf("第 %d 页不是合法 PNG", i)
		}
		t.Logf("p%d %dx%d %.0f KB", i+1, p.Width, p.Height, float64(len(p.PNG))/1024)
	}
	if total == 0 {
		t.Fatal("渲染出的页面全是空的")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
