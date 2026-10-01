// Package pdftext 从 PDF 里取出带版面信息的文本。
//
// 为什么自己写而不用现成库：本项目要求 CGO_ENABLED=0、单文件二进制、
// 离线可构建，而成熟的 PDF 文本抽取库几乎都带 cgo 依赖
// （go-fitz 走 MuPDF，pdfcore 之类也常拖进字体栈）。
// 引一个"只是读文本"的库进来，会让"零外部依赖"这个前提直接作废。
//
// 为什么只取文本、不渲染页面：录题要的是题面文字，而版面还原
// （把打散的公式重新拼成 LaTeX、还原表格线、还原图形）本质上是渲染器的活，
// 那是另一个数量级的工程。文本层加坐标已经足够让模型还原绝大部分题面，
// 而渲染失败时给一句"这份 PDF 无法处理"比渲染出一张糊掉的图有用得多。
//
// 明确不支持：加密文档（权限密码）、Type3 字体里内嵌的解释器脚本、
// 扫描件（没有文本层，只能得到空白——此时由调用方给出可执行的提示）。
package pdftext

import (
	"fmt"
	"strings"
)

// Object 是 PDF 里的任意一个对象。
//
// 用 `any` 而不是一棵专门的类型树：解析出来的中间产物立刻会被解释掉，
// 为它建一套带方法的类型只会让每个消费点都写一堆类型断言。
type Object any

// Name 是 PDF 的名字对象（写作 /Foo）。
type Name string

// Ref 是间接引用（写作 12 0 R）。
type Ref struct {
	Num int
	Gen int
}

// Dict 是 PDF 的字典对象。键是 Name，取值是 Object。
type Dict map[Name]Object

// Array 是 PDF 的数组对象。
type Array []Object

// Stream 是流对象：字典 + 原始字节。
//
// Raw 保留**未解压**的内容：一张 10MB 的插图可能一辈子都不会被读，
// 而 /Length 本身还可能是个间接引用，非得先解引用才知道边界在哪。
type Stream struct {
	Dict Dict
	Raw  []byte

	doc  *Document
	data []byte // 解压后的缓存，nil 表示还没解
}

// Bytes 返回解压后的流内容。
func (s *Stream) Bytes() ([]byte, error) {
	if s.data != nil {
		return s.data, nil
	}
	if s.doc == nil {
		return s.Raw, nil
	}
	out, err := s.doc.decodeStream(s)
	if err != nil {
		return nil, err
	}
	s.data = out
	return out, nil
}

// ---- Document 上的取值助手 ----
//
// 全部挂在 *Document 上而不是 Dict 上：解引用需要访问文档，
// 而 Dict 是纯数据。让 Dict 反向持有文档会逼出一个全局变量，
// 那是并发下的定时炸弹。

// get 取一个键的值并解引用；缺失或类型不对时返回 nil。
func (d *Document) get(dict Dict, key Name) Object {
	if dict == nil {
		return nil
	}
	return d.resolve(dict[key])
}

func (d *Document) nameOf(dict Dict, key Name) (Name, bool) {
	v, ok := d.get(dict, key).(Name)
	return v, ok
}

func (d *Document) dictOf(dict Dict, key Name) (Dict, bool) {
	v, ok := d.get(dict, key).(Dict)
	return v, ok
}

func (d *Document) arrayOf(dict Dict, key Name) (Array, bool) {
	v, ok := d.get(dict, key).(Array)
	return v, ok
}

func (d *Document) streamOf(dict Dict, key Name) (*Stream, bool) {
	v, ok := d.get(dict, key).(*Stream)
	return v, ok
}

func (d *Document) intOf(dict Dict, key Name) (int64, bool) {
	return toInt(d.get(dict, key))
}

func (d *Document) floatOf(dict Dict, key Name) (float64, bool) {
	return toFloat(d.get(dict, key))
}

func toInt(o Object) (int64, bool) {
	switch v := o.(type) {
	case int64:
		return v, true
	case float64:
		return int64(v), true
	}
	return 0, false
}

func toFloat(o Object) (float64, bool) {
	switch v := o.(type) {
	case int64:
		return float64(v), true
	case float64:
		return v, true
	}
	return 0, false
}

// floatSlice 把数组里的数值逐个取出，非数值的记 0。
func floatSlice(a Array) []float64 {
	out := make([]float64, len(a))
	for i, v := range a {
		out[i], _ = toFloat(v)
	}
	return out
}

// hasKey 判断键是否存在。它不解引用，因此不能用来判断"这个值是不是 null"——
// 要判断值必须用 get。
func hasKey(dict Dict, key Name) bool {
	if dict == nil {
		return false
	}
	_, ok := dict[key]
	return ok
}

func errUnsupported(format string, args ...any) error {
	return fmt.Errorf("pdftext: "+format, args...)
}

func errMalformed(format string, args ...any) error {
	return fmt.Errorf("pdftext: 文件结构损坏："+format, args...)
}

// isSpace 判断字节是不是 PDF 语法里的空白符。
//
// 刻意不用 unicode.IsSpace：PDF 的空白是**字节**级的白名单
// （0x00 0x09 0x0A 0x0C 0x0D 0x20），把高位字节也算进去会让
// 二进制流里的内容被误当成语法。
func isSpace(b byte) bool {
	switch b {
	case 0x00, 0x09, 0x0A, 0x0C, 0x0D, 0x20:
		return true
	}
	return false
}

// isDelim 判断字节是不是 PDF 的分隔符。
func isDelim(b byte) bool {
	switch b {
	case '(', ')', '<', '>', '[', ']', '{', '}', '/', '%':
		return true
	}
	return false
}

// cleanName 还原名字对象里的 #XX 转义。
func cleanName(s string) string {
	if !strings.Contains(s, "#") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '#' && i+2 < len(s) {
			hi, ok1 := hexVal(s[i+1])
			lo, ok2 := hexVal(s[i+2])
			if ok1 && ok2 {
				b.WriteByte(hi<<4 | lo)
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func hexVal(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}
