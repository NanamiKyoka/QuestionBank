package pdftext

import (
	"bytes"
	"compress/flate"
	"strings"
	"testing"
)

// CMap 的操作数是**十六进制串**而不是数字：<0033> 表示码值 0x0033。
// 当成数值处理的话整张映射表会静默地一条都填不进去，
// 表现是每个字符都变成替换符——没有任何报错，只能靠这个用例钉住。
func TestParseCMapDecodesHexOperands(t *testing.T) {
	cmap := strings.Join([]string{
		"begincmap",
		"1 begincodespacerange",
		"<0000> <FFFF>",
		"endcodespacerange",
		"2 beginbfchar",
		"<0033> <4E00>",
		"<006B> <0020>",
		"endbfchar",
		"endcmap",
	}, "\n")

	got := map[int]rune{}
	parseCMap([]byte(cmap), func(code int, r rune) { got[code] = r })

	if r, ok := got[0x33]; !ok || r != '一' {
		t.Fatalf("0x33 应映射到「一」，实际 %q (ok=%v)", string(r), ok)
	}
	if r, ok := got[0x6B]; !ok || r != ' ' {
		t.Fatalf("0x6B 应映射到空格，实际 %q (ok=%v)", string(r), ok)
	}
	if _, ok := got[0x0000]; ok {
		t.Fatal("codespacerange 不该进映射表")
	}
}

// bfrange 的第三项是起始字符（等差），也可以是一个数组（逐个列出）。
func TestParseCMapHandlesBothRangeForms(t *testing.T) {
	// 标量形式按**码位等差**展开——这是规范规定的行为。
	// 注意汉字并不连续，拿中文当例子会得出看似荒谬却正确的结果；
	// 这里用拉丁字母，等差才是可预期的。
	scalar := map[int]rune{}
	parseCMap([]byte("begincmap\n1 beginbfrange\n<0041> <0043> <0061>\nendbfrange\nendcmap\n"),
		func(code int, r rune) { scalar[code] = r })
	for i, want := range []rune{'a', 'b', 'c'} {
		if scalar[0x41+i] != want {
			t.Fatalf("等差区间第 %d 项应为 %q，实际 %q", i, want, scalar[0x41+i])
		}
	}

	// 数组形式逐个列出，不做等差。
	listed := map[int]rune{}
	parseCMap([]byte("begincmap\n1 beginbfrange\n<0041> <0043> [<0058> <0059> <005A>]\nendbfrange\nendcmap\n"),
		func(code int, r rune) { listed[code] = r })
	for i, want := range []rune{'X', 'Y', 'Z'} {
		if listed[0x41+i] != want {
			t.Fatalf("数组区间第 %d 项应为 %q，实际 %q", i, want, listed[0x41+i])
		}
	}
}

// CMap 头部堆着一长串 def / findresource / begin。它们不该被当成操作数，
// 否则第一段 bfchar 之前的所有值都会被算进它那一段。
func TestParseCMapIgnoresPrologue(t *testing.T) {
	cmap := strings.Join([]string{
		"/CIDInit /ProcSet findresource begin",
		"12 dict begin",
		"begincmap",
		"/CMapName /Test def",
		"/CMapType 2 def",
		"/CIDSystemInfo << /Registry (Adobe) /Ordering (UCS) /Supplement 0 >> def",
		"1 beginbfchar",
		"<0041> <0042>",
		"endbfchar",
		"endcmap",
	}, "\n")

	got := map[int]rune{}
	parseCMap([]byte(cmap), func(code int, r rune) { got[code] = r })

	if len(got) != 1 || got[0x41] != 'B' {
		t.Fatalf("头部不应污染映射表，实际 %v", got)
	}
}

// 一段 bfchar 可能有上千对（子集化的中文字体很常见）。
// 早期实现把操作数栈截到 3 个，于是只剩最后一对——静默丢数据。
func TestParseCMapKeepsEveryPairOfALongSection(t *testing.T) {
	var b strings.Builder
	b.WriteString("begincmap\n1000 beginbfchar\n")
	for i := 0; i < 1000; i++ {
		code := i + 1
		b.WriteString("<")
		b.WriteString(pad4(code))
		b.WriteString("> <")
		b.WriteString(pad4(code))
		b.WriteString(">\n")
	}
	b.WriteString("endbfchar\nendcmap\n")

	n := 0
	parseCMap([]byte(b.String()), func(code int, r rune) {
		if r != rune(code) {
			t.Fatalf("码值 %d 映到了 %q", code, r)
		}
		n++
	})
	if n != 1000 {
		t.Fatalf("1000 对应当全部收下，实际 %d", n)
	}
}

func pad4(v int) string {
	s := itoa(v)
	for len(s) < 4 {
		s = "0" + s
	}
	return s
}

func TestPredictorRestoresPNGRows(t *testing.T) {
	// 过滤器类型 2（Up）：每行等于上一行加上自己。
	data := []byte{
		2, 1, 2, 3,
		2, 1, 1, 1,
	}
	got, err := applyPredictor(data, Dict{"Predictor": int64(12), "Colors": int64(1), "Columns": int64(3), "BitsPerComponent": int64(8)})
	if err != nil {
		t.Fatalf("还原失败: %v", err)
	}
	want := []byte{1, 2, 3, 2, 3, 4}
	if len(got) != len(want) {
		t.Fatalf("长度应为 %d，实际 %d (%v)", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 字节应为 %d，实际 %d（整段 %v）", i, want[i], got[i], got)
		}
	}
}

// PDF 里有两种"少了 zlib 头"的流，都得能读。
func TestFlateDecodeAcceptsBothHeaders(t *testing.T) {
	zlibbed := deflate(t, "hello")
	if got, err := flateDecode(zlibbed, nil); err != nil || string(got) != "hello" {
		t.Fatalf("zlib 头解不开: %q err=%v", got, err)
	}
	// 情况一：头没了、末尾的 Adler-32 还在。
	if got, err := flateDecode(zlibbed[2:], nil); err != nil || string(got) != "hello" {
		t.Fatalf("丢了 zlib 头应当仍能解: %q err=%v", got, err)
	}
	// 情况二：真正的裸 deflate，头与校验都没有。
	var raw bytes.Buffer
	w, _ := flate.NewWriter(&raw, flate.DefaultCompression)
	w.Write([]byte("hello"))
	w.Close()
	if got, err := flateDecode(raw.Bytes(), nil); err != nil || string(got) != "hello" {
		t.Fatalf("裸 deflate 解不开: %q err=%v", got, err)
	}
}

func TestASCIIHexDecode(t *testing.T) {
	got, err := asciiHexDecode([]byte("48656C6C6F>"))
	if err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	if string(got) != "Hello" {
		t.Fatalf("解码结果不对: %q", got)
	}
	// 奇数个十六进制位：末尾补 0。
	got, _ = asciiHexDecode([]byte("4>"))
	if len(got) != 1 || got[0] != 0x40 {
		t.Fatalf("奇数位应补 0，实际 %v", got)
	}
}

// 解不出来的流要明确说不支持，而不是悄悄返回乱码让模型去猜。
func TestUnsupportedFilterIsReported(t *testing.T) {
	if _, err := applyFilter("LZWDecode", []byte("x"), nil); err == nil {
		t.Fatal("LZW 必须明确报不支持")
	}
	// 图像编码当成"这段是空的"，不算错误。
	got, err := applyFilter("DCTDecode", []byte("x"), nil)
	if err != nil || got != nil {
		t.Fatalf("图像编码应当返回空而非错误: %v %v", got, err)
	}
}
