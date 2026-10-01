package pdftext

import (
	"strconv"
	"strings"
)

// lexer 是 PDF 的词法分析器。
//
// PDF 的词法是**上下文相关**的：字符串里的字节要不要转义、名字里的 #
// 要不要还原、遇到 << 是字典还是两个次级比较运算符，都取决于当前状态。
// 因此这里不做完整的词法分析，只按需往前扫；正则或一次性切词都会在
// 二进制流里翻车（流里几乎不会出现 '>'，却经常出现 "(" ）。
type lexer struct {
	data []byte
	pos  int
}

func newLexer(data []byte, pos int) *lexer { return &lexer{data: data, pos: pos} }

func (l *lexer) skipSpace() {
	for l.pos < len(l.data) && isSpace(l.data[l.pos]) {
		l.pos++
	}
}

// skipComment 跳过一行 % 注释。
func (l *lexer) skipComment() {
	for l.pos < len(l.data) && l.data[l.pos] != '\r' && l.data[l.pos] != '\n' {
		l.pos++
	}
}

type tokenKind int

const (
	tokEOF tokenKind = iota
	tokNumber
	tokName
	tokString
	tokArrayOpen
	tokArrayClose
	tokDictOpen
	tokDictClose
	tokBraceOpen // { （PostScript 函数体）
	tokBraceClose
	tokKeyword
)

type token struct {
	kind tokenKind
	num  float64
	// isInt 区分 3 与 3.0：间接引用与数组下标必须是整数，
	// 而 PDF 允许把整数写成 3.0，不区分就会把 "3 0 R" 里的 0.0 当下标。
	isInt   bool
	str     []byte
	name    string
	keyword string
}

// next 读出一个词法单元。
func (l *lexer) next() token {
	l.skipSpace()
	if l.pos >= len(l.data) {
		return token{kind: tokEOF}
	}
	c := l.data[l.pos]

	switch {
	case c == '%':
		l.skipComment()
		return l.next()

	case c == '/':
		return l.lexName()

	case c == '(':
		return l.lexLiteralString()

	case c == '[':
		l.pos++
		return token{kind: tokArrayOpen}

	case c == ']':
		l.pos++
		return token{kind: tokArrayClose}

	case c == ']':
		l.pos++
		return token{kind: tokArrayClose}

	case c == '{':
		l.pos++
		return token{kind: tokBraceOpen}

	case c == '}':
		l.pos++
		return token{kind: tokBraceClose}

	case c == '<':
		if l.pos+1 < len(l.data) && l.data[l.pos+1] == '<' {
			l.pos += 2
			return token{kind: tokDictOpen}
		}
		return l.lexHexString()

	case c == '>':
		if l.pos+1 < len(l.data) && l.data[l.pos+1] == '>' {
			l.pos += 2
			return token{kind: tokDictClose}
		}
		// 落单的 '>' 在损坏文件里出现过：当作分隔符跳过。
		l.pos++
		return l.next()

	case c == ')' || c == ']' || c == '}':
		l.pos++
		return l.next()

	case c == '+' || c == '-' || c == '.' || (c >= '0' && c <= '9'):
		return l.lexNumber()

	default:
		return l.lexKeyword()
	}
}

func (l *lexer) lexNumber() token {
	start := l.pos
	if c := l.data[l.pos]; c == '+' || c == '-' {
		l.pos++
	}
	dot := false
	for l.pos < len(l.data) {
		c := l.data[l.pos]
		switch {
		case c >= '0' && c <= '9':
			l.pos++
		case c == '.':
			// 一个数字里出现第二个小数点说明后面已经不是数字了。
			if dot {
				goto done
			}
			dot = true
			l.pos++
		case c == '-' || c == '+':
			// 允许 "1-2" 这种写法：PDF 允许省略分隔符。
			l.pos++
		default:
			goto done
		}
	}
done:
	raw := string(l.data[start:l.pos])
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return l.lexKeyword()
	}
	return token{kind: tokNumber, num: v, isInt: !dot}
}

func (l *lexer) lexKeyword() token {
	start := l.pos
	for l.pos < len(l.data) {
		c := l.data[l.pos]
		if isSpace(c) || isDelim(c) {
			break
		}
		l.pos++
	}
	if l.pos == start {
		// 一个分隔符落到了 default 分支：吃掉它再试一次。
		l.pos++
		return l.next()
	}
	return token{kind: tokKeyword, keyword: string(l.data[start:l.pos])}
}

func (l *lexer) lexName() token {
	l.pos++ // 跳过 '/'
	start := l.pos
	for l.pos < len(l.data) {
		c := l.data[l.pos]
		if isSpace(c) || isDelim(c) {
			break
		}
		l.pos++
	}
	return token{kind: tokName, name: cleanName(string(l.data[start:l.pos]))}
}

// lexLiteralString 读 ( ... ) 字符串，处理嵌套括号与转义。
func (l *lexer) lexLiteralString() token {
	l.pos++ // 跳过 '('
	var out []byte
	depth := 1
	for l.pos < len(l.data) {
		c := l.data[l.pos]
		l.pos++
		switch c {
		case '\\':
			if l.pos >= len(l.data) {
				break
			}
			e := l.data[l.pos]
			l.pos++
			switch e {
			case 'n':
				out = append(out, '\n')
			case 'r':
				out = append(out, '\r')
			case 't':
				out = append(out, '\t')
			case 'b':
				out = append(out, '\b')
			case 'f':
				out = append(out, 0x0C)
			case '(', ')', '\\':
				out = append(out, e)
			case '\r':
				// 续行：\ 后跟回车，回车本身要吃掉，换行保留。
				if l.pos < len(l.data) && l.data[l.pos] == '\n' {
					l.pos++
				}
			case '\n':
				// 续行：直接吞掉换行。
			default:
				if e >= '0' && e <= '7' {
					v := int(e - '0')
					for k := 0; k < 2 && l.pos < len(l.data); k++ {
						d := l.data[l.pos]
						if d < '0' || d > '7' {
							break
						}
						v = v*8 + int(d-'0')
						l.pos++
					}
					out = append(out, byte(v))
				} else {
					// \ 后面不是已知转义时，PDF 规定丢掉反斜杠保留字符本身。
					out = append(out, e)
				}
			}
		case '(':
			depth++
			out = append(out, c)
		case ')':
			depth--
			if depth == 0 {
				return token{kind: tokString, str: out}
			}
			out = append(out, c)
		default:
			out = append(out, c)
		}
	}
	// 未闭合：把已经读到的当作内容返回，交给上层去报错或容忍。
	return token{kind: tokString, str: out}
}

// lexHexString 读 < ... > 十六进制串。
func (l *lexer) lexHexString() token {
	l.pos++ // 跳过 '<'
	var out []byte
	var hi byte
	have := false
	for l.pos < len(l.data) {
		c := l.data[l.pos]
		l.pos++
		if c == '>' {
			break
		}
		v, ok := hexVal(c)
		if !ok {
			continue
		}
		if have {
			out = append(out, hi<<4|v)
			have = false
		} else {
			hi = v
			have = true
		}
	}
	if have {
		// 奇数个十六进制位：末尾补 0。
		out = append(out, hi<<4)
	}
	return token{kind: tokString, str: out}
}

// ---- 对象解析 ----

// parseObject 解析一个对象。
//
// doc 传 nil 表示"这是顶层对象"，此时不追踪流的长度边界。
func (l *lexer) parseObject(doc *Document) Object {
	return l.parseFrom(doc, l.next())
}

func (l *lexer) parseFrom(doc *Document, t token) Object {
	switch t.kind {
	case tokEOF:
		return nil

	case tokNumber:
		// "12 0 R" 是间接引用：需要回看两个 token。
		if t.isInt && l.peekIsRefTail() {
			return l.readRefTail(int64(t.num))
		}
		if t.isInt {
			return int64(t.num)
		}
		return t.num

	case tokName:
		return Name(t.name)

	case tokString:
		return string(t.str)

	case tokArrayOpen:
		arr := Array{}
		for {
			n := l.next()
			if n.kind == tokArrayClose || n.kind == tokEOF {
				return arr
			}
			arr = append(arr, l.parseFrom(doc, n))
		}

	case tokDictOpen:
		d := Dict{}
		for {
			n := l.next()
			if n.kind == tokDictClose || n.kind == tokEOF {
				break
			}
			if n.kind != tokName {
				// 键必须是名字；不是就跳过这个值继续，
				// 损坏文件里这一层比报错更常见。
				l.parseFrom(doc, n)
				continue
			}
			key := Name(n.name)
			d[key] = l.parseObject(doc)
		}
		// 字典后面可能紧跟 stream。
		if doc != nil {
			return l.maybeStream(doc, d)
		}
		return d

	case tokKeyword:
		switch t.keyword {
		case "true":
			return true
		case "false":
			return false
		case "null":
			return nil
		}
		// 裸关键字：当作操作符，由内容流解释器处理。这里返回它的原文。
		return Operator(t.keyword)

	case tokBraceOpen:
		// PostScript 函数体：整体收成一段原始字节。
		depth := 1
		start := l.pos
		for l.pos < len(l.data) && depth > 0 {
			switch l.data[l.pos] {
			case '{':
				depth++
			case '}':
				depth--
			}
			l.pos++
		}
		return string(l.data[start : l.pos-1])

	case tokArrayClose, tokDictClose, tokBraceClose:
		// 多余的闭合符：忽略并继续。
		return l.parseObject(doc)
	}
	return nil
}

// Operator 是内容流里的裸关键字（q、cm、Tj …）。
type Operator string

// peekIsRefTail 判断当前位置起是否是 "<int> R"。
func (l *lexer) peekIsRefTail() bool {
	save := l.pos
	defer func() { l.pos = save }()

	t := l.next()
	if t.kind != tokNumber || !t.isInt {
		return false
	}
	r := l.next()
	return r.kind == tokKeyword && r.keyword == "R"
}

func (l *lexer) readRefTail(num int64) Ref {
	t := l.next() // 世代号
	gen, _ := toInt(t)
	l.next() // 吃掉 R
	return Ref{Num: int(num), Gen: int(gen)}
}

// maybeStream 在字典之后检查是不是 `stream` 关键字。
func (l *lexer) maybeStream(doc *Document, d Dict) Object {
	save := l.pos
	l.skipSpace()
	// stream 后面必须紧跟 CRLF 或 LF。
	if !l.hasWord("stream") {
		l.pos = save
		return d
	}
	l.pos += len("stream")
	// 跳过 EOL。
	if l.pos < len(l.data) && l.data[l.pos] == '\r' {
		l.pos++
	}
	if l.pos < len(l.data) && l.data[l.pos] == '\n' {
		l.pos++
	}
	start := l.pos

	end := -1
	if n, ok := doc.intOf(d, "Length"); ok && n >= 0 && start+int(n) <= len(l.data) {
		// 声明的长度只是**建议**。先按它切，再确认后面真的跟着 endstream；
		// 确认不了就退回扫描。
		if l.endstreamFollows(start + int(n)) {
			end = start + int(n)
		}
	}
	if end < 0 {
		idx := indexFrom(l.data, []byte("endstream"), start)
		if idx < 0 {
			// 没有 endstream：这份文件坏得没法靠标记定位。
			return Stream{Dict: d, Raw: l.data[start:], doc: doc}
		}
		end = idx
		// endstream 前通常有一个 EOL，把它排除在内容之外。
		if end > start && l.data[end-1] == '\n' {
			end--
		}
		if end > start && l.data[end-1] == '\r' {
			end--
		}
	}
	raw := l.data[start:end]
	// 跳过 endstream 与 endobj 之前的内容，交还给调用方继续解析。
	idx := indexFrom(l.data, []byte("endstream"), end)
	if idx < 0 {
		idx = len(l.data)
	}
	l.pos = idx + len("endstream")
	return &Stream{Dict: d, Raw: raw, doc: doc}
}

// hasWord 判断当前位置起是否是给定的关键字（其后必须是分隔符）。
func (l *lexer) hasWord(word string) bool {
	if l.pos+len(word) > len(l.data) {
		return false
	}
	if string(l.data[l.pos:l.pos+len(word)]) != word {
		return false
	}
	next := l.pos + len(word)
	if next >= len(l.data) {
		return true
	}
	c := l.data[next]
	return isSpace(c) || isDelim(c)
}

// endstreamFollows 判断给定偏移处是不是 endstream（允许中间有空白）。
func (l *lexer) endstreamFollows(at int) bool {
	save := l.pos
	defer func() { l.pos = save }()
	l.pos = at
	l.skipSpace()
	return l.hasWord("endstream")
}

// indexFrom 从 start 起查找子串，返回首个下标或 -1。
func indexFrom(data, sub []byte, start int) int {
	if start < 0 {
		start = 0
	}
	if start > len(data) {
		return -1
	}
	idx := strings.Index(string(data[start:]), string(sub))
	if idx < 0 {
		return -1
	}
	return start + idx
}
