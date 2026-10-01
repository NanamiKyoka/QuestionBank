package importing

import (
	"bytes"
	"compress/zlib"
	"strings"
	"testing"
)

// minimalPDF 造一份只有结构、没有文字层的 PDF（等价于扫描件）。
func minimalPDF(t *testing.T) []byte {
	t.Helper()
	return buildPDF(
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 612 792] >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R >>",
		streamObj(t, "0 0 1 RG 10 10 100 100 re f"),
	)
}

// textPDF 造一份带文字层的 PDF。
func textPDF(t *testing.T, content string) []byte {
	t.Helper()
	cmap := streamObj(t, strings.Join([]string{
		"/CIDInit /ProcSet findresource begin",
		"12 dict begin",
		"begincmap",
		"1 begincodespacerange <00> <FF> endcodespacerange",
		"1 beginbfrange <0020> <007E> <0020> endbfrange",
		"endcmap",
		"CMapName currentdict /CMap defineresource pop",
		"end",
		"end",
	}, "\n"))
	return buildPDF(
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 612 792] >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] "+
			"/Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		streamObj(t, content),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /FirstChar 32 /LastChar 126 "+
			"/ToUnicode 6 0 R >>",
		cmap,
	)
}

func streamObj(t *testing.T, body string) string {
	t.Helper()
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	if _, err := w.Write([]byte(body)); err != nil {
		t.Fatalf("压缩失败: %v", err)
	}
	w.Close()
	return "<< /Length " + itoa(buf.Len()) + " /Filter /FlateDecode >>\nstream\n" +
		buf.String() + "\nendstream"
}

func buildPDF(objects ...string) []byte {
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.7\n")

	offsets := make([]int, len(objects)+1)
	for i, body := range objects {
		offsets[i+1] = buf.Len()
		buf.WriteString(itoa(i + 1))
		buf.WriteString(" 0 obj\n")
		buf.WriteString(body)
		buf.WriteString("\nendobj\n")
	}

	xref := buf.Len()
	buf.WriteString("xref\n0 ")
	buf.WriteString(itoa(len(objects) + 1))
	buf.WriteString("\n0000000000 65535 f \n")
	for i := 1; i <= len(objects); i++ {
		s := itoa(offsets[i])
		for len(s) < 10 {
			s = "0" + s
		}
		buf.WriteString(s)
		buf.WriteString(" 00000 n \n")
	}
	buf.WriteString("trailer\n<< /Size ")
	buf.WriteString(itoa(len(objects) + 1))
	buf.WriteString(" /Root 1 0 R >>\nstartxref\n")
	buf.WriteString(itoa(xref))
	buf.WriteString("\n%%EOF\n")
	return buf.Bytes()
}
