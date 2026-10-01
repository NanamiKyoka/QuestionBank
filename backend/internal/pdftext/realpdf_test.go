package pdftext

import (
	"os"
	"testing"
)

// 用真实试卷验证抽取质量。需要时用 QMB_PDF 环境变量指向文件。
func TestExtractRealPaper(t *testing.T) {
	path := os.Getenv("QMB_PDF")
	if path == "" {
		t.Skip("设置 QMB_PDF 指向真实 PDF 后运行")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	res, err := Extract(data)
	if err != nil {
		t.Fatalf("抽取失败: %v", err)
	}
	t.Logf("pages=%d chars=%d warnings=%v", res.Pages, res.Chars, res.Warnings)
	t.Logf("----\n%s\n----", res.Text)
}
