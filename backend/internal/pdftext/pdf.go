package pdftext

import (
	"strings"
)

// Result 一次抽取的结果。
type Result struct {
	// Text 按页拼接的正文，页与页之间空一行。
	Text string
	// Pages 页数。
	Pages int
	// Chars 有实际内容的字符数（去掉空白）。
	Chars int
	// Warnings 抽取过程中的提示。
	Warnings []string
}

// HasText 是否真的取到了文字。
//
// 区分"取到了"与"什么都没取到"很重要：扫描件同样是"成功解析"，
// 但它没有文本层，用户拿到的是一份空草稿。调用方需要据此
// 给出"这份 PDF 是扫描件"而不是"识别出 0 道题"。
func (r Result) HasText() bool { return r.Chars > 0 }

// Extract 从 PDF 字节流里取出正文。
func Extract(data []byte) (Result, error) {
	doc, err := Open(data)
	if err != nil {
		return Result{}, err
	}

	if encrypted, ok := doc.intOf(doc.trailer, "Encrypt"); ok && encrypted != 0 {
		return Result{}, errUnsupported("这份 PDF 已加密，无法读取文字")
	}

	pages, err := doc.pages()
	if err != nil {
		return Result{}, err
	}

	var (
		parts    []string
		warnings []string
		empty    int
	)
	for _, p := range pages {
		text := pageText(runPage(doc, p))
		text = strings.TrimSpace(text)
		if text == "" {
			empty++
		}
		parts = append(parts, text)
	}

	if empty > 0 && empty == len(pages) {
		warnings = append(warnings, "这份 PDF 没有可读取的文字层（多半是扫描件或纯图片）")
	} else if empty > 0 {
		warnings = append(warnings, "其中 "+itoa(empty)+" 页没有文字，可能是插图页")
	}

	joined := strings.Join(parts, "\n\n")
	return Result{
		Text:     joined,
		Pages:    len(pages),
		Chars:    countRunes(joined),
		Warnings: warnings,
	}, nil
}

func countRunes(s string) int {
	n := 0
	for _, r := range s {
		if !isSpaceRune(r) {
			n++
		}
	}
	return n
}

func isSpaceRune(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\r', 0x0B, 0x0C, 0xA0, 0x3000, 0xFEFF:
		return true
	}
	return false
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
