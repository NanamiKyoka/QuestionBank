package omml

import (
	"strings"
	"unicode"
)

// parser 递归下降解析器。
//
// 不做完整的 TeX 语法分析（那需要一个真正的 catcode 引擎），
// 只覆盖数学模式的常见构造。设计取态是**宽进**：
// 不认识的命令退化为字面文本，不合法的地方尽量向前恢复，
// 保证一份卷子里个别公式写错不会导致整份导出失败。
type parser struct {
	src []rune
	pos int
}

// Parse 把 LaTeX 数学公式解析为表达式树。
// 空字符串返回空 Row，不返回错误——调用方无需为"没有公式"做特判。
func Parse(src string) Expr {
	p := &parser{src: []rune(src)}
	row := p.parseList(func() bool { return false })
	p.unwrap()
	return row
}

// unwrap 跳过尾部空白。
func (p *parser) unwrap() {
	for p.pos < len(p.src) && unicode.IsSpace(p.src[p.pos]) {
		p.pos++
	}
}

func (p *parser) eof() bool { return p.pos >= len(p.src) }

func (p *parser) peek() rune {
	if p.eof() {
		return 0
	}
	return p.src[p.pos]
}

// peekCommand 判断当前位置是否是名为 name 的命令。
// 必须校验命令名后紧跟非字母字符，否则 \sum 会被误认成 \summation 的前缀。
func (p *parser) peekCommand(name string) bool {
	if p.eof() || p.src[p.pos] != '\\' {
		return false
	}
	rest := p.src[p.pos+1:]
	want := []rune(name)
	if len(rest) < len(want) {
		return false
	}
	for i, c := range want {
		if rest[i] != c {
			return false
		}
	}
	if len(rest) == len(want) {
		return true
	}
	return !unicode.IsLetter(rest[len(want)])
}

// skipSpaces 跳过空白。
//
// TeX 里空白在数学模式中不产生输出，因此 \alpha \beta 应解析成两个相邻符号。
func (p *parser) skipSpaces() {
	for !p.eof() && unicode.IsSpace(p.src[p.pos]) {
		p.pos++
	}
}

// parseList 解析一个表达式序列，stop 返回 true 时停止（不消费当前位置）。
func (p *parser) parseList(stop func() bool) *Row {
	row := &Row{}
	for !p.eof() {
		if stop != nil && stop() {
			break
		}
		c := p.peek()

		switch {
		case unicode.IsSpace(c):
			p.pos++
		case c == '}':
			// 多余的右花括号：向前恢复，跳过而不是报错。
			p.pos++
		case c == '^' || c == '_':
			// 没有基底的上标/下标：造一个空基底，避免丢弃内容。
			row.Items = append(row.Items, p.parseScripts(&Row{}))
		case c == '&':
			// 对齐标记在单行公式里无意义，跳过。
			p.pos++
		default:
			atom := p.parseAtom()
			if atom == nil {
				continue
			}
			row.Items = append(row.Items, p.parseScripts(atom))
		}
	}
	return row
}

// parseScripts 处理紧跟基底的 ^ 与 _ 。
func (p *parser) parseScripts(base Expr) Expr {
	var sub, sup Expr
	for {
		p.skipSpaces()
		if p.eof() {
			break
		}
		switch p.peek() {
		case '^':
			p.pos++
			if sup != nil {
				// 重复上标（如 x^2^3）：后一个作为前一上标的基底继续解析。
				sup = p.parseScripts(p.parseAtomOrEmpty(sup))
				continue
			}
			sup = p.parseAtomOrEmpty(nil)
		case '_':
			p.pos++
			if sub != nil {
				sub = p.parseScripts(p.parseAtomOrEmpty(sub))
				continue
			}
			sub = p.parseAtomOrEmpty(nil)
		default:
			if sub == nil && sup == nil {
				return base
			}
			return &Script{Base: base, Sub: sub, Sup: sup}
		}
	}
	if sub == nil && sup == nil {
		return base
	}
	return &Script{Base: base, Sub: sub, Sup: sup}
}

// parseAtomOrEmpty 解析一个原子；解析不出来时返回 fallback（可能为 nil）。
func (p *parser) parseAtomOrEmpty(fallback Expr) Expr {
	if p.eof() {
		return fallback
	}
	atom := p.parseAtom()
	if atom == nil {
		return fallback
	}
	return atom
}

// parseAtom 解析一个原子：分组、命令或一段普通字符。
func (p *parser) parseAtom() Expr {
	p.skipSpaces()
	if p.eof() {
		return nil
	}

	switch c := p.peek(); {
	case c == '{':
		p.pos++
		inner := p.parseList(func() bool { return p.peek() == '}' })
		if p.peek() == '}' {
			p.pos++
		}
		return inner
	case c == '\\':
		return p.parseCommand()
	default:
		return p.parseOrdinary()
	}
}

// isOrdinary 判断字符是否属于"普通字符"——即除 TeX 控制字符之外的一切。
func isOrdinary(c rune) bool {
	switch c {
	case '{', '}', '\\', '^', '_', '&', '$', '~':
		return false
	}
	return !unicode.IsSpace(c)
}

// parseOrdinary 读取一段普通字符，并按"数字正体 / 其他斜体"切分为多个 Run。
func (p *parser) parseOrdinary() Expr {
	start := p.pos
	for !p.eof() && isOrdinary(p.peek()) {
		p.pos++
	}
	text := string(p.src[start:p.pos])
	if text == "" {
		p.pos++ // 防御：确保游标前进，避免死循环
		return nil
	}
	return splitRuns(text)
}

// splitRuns 把一段文本切成若干 Run，数字与标点用正体、字母用斜体。
func splitRuns(text string) Expr {
	runes := []rune(text)
	var items []Expr

	i := 0
	for i < len(runes) {
		upright := isUprightRune(runes[i])
		j := i
		for j < len(runes) && isUprightRune(runes[j]) == upright {
			j++
		}
		items = append(items, &Run{Text: string(runes[i:j]), Upright: upright})
		i = j
	}

	if len(items) == 1 {
		return items[0]
	}
	return &Row{Items: items}
}

// isUprightRune 数字、标点、运算符与 CJK 用正体；拉丁/希腊字母用斜体。
func isUprightRune(c rune) bool {
	if unicode.IsDigit(c) {
		return true
	}
	if c > unicode.MaxASCII {
		// 非 ASCII：CJK 及各语种文字都用正体，运算符字符也用正体。
		return true
	}
	return !unicode.IsLetter(c)
}

// parseCommand 解析反斜杠命令。
func (p *parser) parseCommand() Expr {
	p.pos++ // 消费 '\'
	if p.eof() {
		return &Run{Text: "\\", Upright: true}
	}

	// 非字母命令：\, \; \{ \} \\ 等。
	if !unicode.IsLetter(p.peek()) {
		c := p.peek()
		p.pos++
		name := string(c)
		switch name {
		case "{":
			return &Run{Text: "{", Upright: true}
		case "}":
			return &Run{Text: "}", Upright: true}
		case "\\":
			return &Space{Width: 0} // 换行在行内公式中无意义，忽略
		}
		if w, ok := spacing[name]; ok {
			return &Space{Width: len(w)}
		}
		return &Run{Text: name, Upright: true}
	}

	start := p.pos
	for !p.eof() && unicode.IsLetter(p.peek()) {
		p.pos++
	}
	name := string(p.src[start:p.pos])
	return p.buildCommand(name)
}

// buildCommand 按命令名构造 AST 节点。
func (p *parser) buildCommand(name string) Expr {
	// 文本类命令：花括号内按原样文本处理。
	if textCommands[name] {
		return &Run{Text: p.readRawGroup(), Upright: true}
	}
	// 字体切换命令：解析内容但忽略样式。
	if mathFonts[name] {
		return p.parseAtomOrEmpty(&Row{})
	}

	switch name {
	case "frac", "dfrac", "tfrac":
		num := p.parseAtomOrEmpty(&Row{})
		den := p.parseAtomOrEmpty(&Row{})
		return &Frac{Num: num, Den: den}

	case "sqrt":
		var deg Expr
		p.skipSpaces()
		if p.peek() == '[' {
			// 这里不能用 parseList 配 stop 断言：普通字符扫描是贪婪的，
			// 会把 "3]" 整体吃掉，导致根指数变成 "3]"。改为先原样取出
			// 配对方括号之间的内容，再单独解析。
			raw := p.readBracketRaw()
			deg = Parse(raw)
		}
		base := p.parseAtomOrEmpty(&Row{})
		return &Sqrt{Deg: deg, Base: base}

	case "left":
		return p.parseDelimited()

	case "right":
		// 孤立的 \right：吞掉定界符，避免它被当成内容输出。
		p.readDelimiter()
		return &Row{}

	case "begin", "end":
		// 环境（如 cases、matrix）暂不支持，退化为字面文本，
		// 保证公式其余部分仍然正常渲染。
		env := p.readRawGroup()
		return &Run{Text: "\\" + name + "{" + env + "}", Upright: true}

	case "limits", "nolimits", "displaystyle", "textstyle":
		return &Row{}
	}

	if op, ok := naryOps[name]; ok {
		return p.parseNary(op)
	}
	if name == "lim" || name == "limsup" || name == "liminf" {
		return p.parseLimLow(funcs[name])
	}
	if fn, ok := funcs[name]; ok {
		return p.parseFunc(fn)
	}
	if ac, ok := accents[name]; ok {
		base := p.parseAtomOrEmpty(&Row{})
		return &Accent{Char: ac, Base: base}
	}
	if sym, ok := greek[name]; ok {
		return &Run{Text: sym.ch, Upright: sym.upright}
	}
	if sym, ok := operators[name]; ok {
		return &Run{Text: sym.ch, Upright: sym.upright}
	}

	// 未知命令：原样输出命令名，用户至少能看到自己写了什么。
	return &Run{Text: name, Upright: true}
}

// readRawGroup 原样读取一个 {...} 分组的内容（不做数学解析）。
func (p *parser) readRawGroup() string {
	p.skipSpaces()
	if p.peek() != '{' {
		// 没有花括号时只取一个原子作为内容，如 \text a
		atom := p.parseAtom()
		if run, ok := atom.(*Run); ok {
			return run.Text
		}
		return ""
	}
	p.pos++
	depth := 1
	start := p.pos
	for !p.eof() && depth > 0 {
		switch p.peek() {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				text := string(p.src[start:p.pos])
				p.pos++
				return text
			}
		}
		p.pos++
	}
	return string(p.src[start:p.pos])
}

// readBracketRaw 读取 [ ... ] 之间配对的内容（花括号内的 ] 不算结束）。
func (p *parser) readBracketRaw() string {
	if p.peek() != '[' {
		return ""
	}
	p.pos++
	depth := 0
	start := p.pos
	for !p.eof() {
		switch p.peek() {
		case '{':
			depth++
		case '}':
			if depth > 0 {
				depth--
			}
		case ']':
			if depth == 0 {
				out := string(p.src[start:p.pos])
				p.pos++
				return out
			}
		}
		p.pos++
	}
	return string(p.src[start:p.pos])
}

// readDelimiter 读取 \left/\right 之后的一个定界符。
func (p *parser) readDelimiter() string {
	p.skipSpaces()
	if p.eof() {
		return ""
	}
	if p.peek() == '\\' {
		p.pos++
		start := p.pos
		for !p.eof() && unicode.IsLetter(p.peek()) {
			p.pos++
		}
		name := string(p.src[start:p.pos])
		if d, ok := delimiters[name]; ok {
			return d
		}
		return ""
	}
	c := string(p.peek())
	p.pos++
	if d, ok := delimiters[c]; ok {
		return d
	}
	return c
}

// parseDelimited 解析 \left( ... \right) 。
func (p *parser) parseDelimited() Expr {
	beg := p.readDelimiter()
	body := p.parseList(func() bool { return p.peekCommand("right") })

	end := ""
	if p.peekCommand("right") {
		p.pos++ // 消费 '\'
		// 跳过 "right"
		for !p.eof() && unicode.IsLetter(p.peek()) {
			p.pos++
		}
		end = p.readDelimiter()
	}
	return &Delim{Beg: beg, End: end, Body: body}
}

// parseNary 解析大运算符及其上下限与主体。
func (p *parser) parseNary(op string) Expr {
	n := &Nary{Op: op}
	// 上下限在 TeX 里通常写作 \sum_{i=1}^{n}，顺序可能颠倒。
	for {
		save := p.pos
		p.skipSpaces()
		if p.eof() {
			break
		}
		switch p.peek() {
		case '_':
			if n.Sub != nil {
				p.pos = save
				goto body
			}
			p.pos++
			n.Sub = p.parseAtomOrEmpty(&Row{})
		case '^':
			if n.Sup != nil {
				p.pos = save
				goto body
			}
			p.pos++
			n.Sup = p.parseAtomOrEmpty(&Row{})
		default:
			p.pos = save
			goto body
		}
	}

body:
	// 主体：取紧随其后的一个原子（通常是 {...} 分组）。
	// 必须让它自己吸收上下标，否则 \sum_{i=1}^{n} a_i 里的 _i
	// 会被外层的 parseScripts 挂到求和符号上。
	n.Body = p.parseScripts(p.parseAtomOrEmpty(&Row{}))
	return n
}

// parseLimLow 解析 \lim_{x \to 0} 这类下标排在正下方的运算符。
func (p *parser) parseLimLow(name string) Expr {
	l := &LimLow{Name: name}
	for {
		save := p.pos
		p.skipSpaces()
		if p.eof() || p.peek() != '_' || l.Sub != nil {
			p.pos = save
			break
		}
		p.pos++
		l.Sub = p.parseAtomOrEmpty(&Row{})
	}
	l.Body = p.parseScripts(p.parseAtomOrEmpty(&Row{}))
	return l
}

// parseFunc 解析函数名及其参数。
func (p *parser) parseFunc(name string) Expr {
	f := &Func{Name: name}
	save := p.pos
	p.skipSpaces()
	// 函数参数只取一个原子：\sin x 的参数是 x，\sin(x+1) 的参数是 (...) 。
	if p.eof() {
		return f
	}
	switch p.peek() {
	case '^', '_', '}', ')', ']', ',', '+', '-', '=', '<', '>':
		// 这些符号表示函数没有紧跟参数，恢复游标。
		p.pos = save
		return f
	}
	// \sin x^2 的参数应当是 x^2 而不是 x，所以这里也要吸收上下标。
	f.Arg = p.parseScripts(p.parseAtomOrEmpty(nil))
	if emptyArg(f.Arg) {
		p.pos = save
		f.Arg = nil
	}
	return f
}

// emptyArg 判断函数参数是否为空（用于区分"有参数"与"只是函数名"）。
func emptyArg(e Expr) bool {
	switch n := e.(type) {
	case nil:
		return true
	case *Row:
		return n.IsEmpty()
	}
	return false
}

// StripMathDelimiters 去掉公式外面的 $ ... $ 或 \[ ... \] 或 \( ... \)。
func StripMathDelimiters(s string) string {
	s = strings.TrimSpace(s)
	for _, pair := range [][2]string{
		{"$$", "$$"}, {"\\[", "\\]"}, {"\\(", "\\)"}, {"$", "$"},
	} {
		if strings.HasPrefix(s, pair[0]) && strings.HasSuffix(s, pair[1]) && len(s) >= len(pair[0])+len(pair[1]) {
			return strings.TrimSpace(s[len(pair[0]) : len(s)-len(pair[1])])
		}
	}
	return s
}
