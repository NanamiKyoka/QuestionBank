package pdftext

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
)

// font 是一个已解码的字体：把字节码翻成 Unicode，并给出字宽。
//
// 两个职责必须放在一起：光知道"这个码位是哪个字"却不知道它多宽，
// 就没法判断两段文字之间该不该补空格；而版面还原（尤其是数学公式）
// 恰恰是靠空格位置读出来的。
type font struct {
	// twoByte 复合字体（Type0）每个字符占两字节。
	twoByte bool
	// toUnicode 字节码 → Unicode。
	toUnicode map[int]rune
	// widths 字节码 → 字宽（1/1000 em）。
	widths map[int]float64
	// defaultWidth 查不到时的字宽。
	defaultWidth float64
	// cidDescendant 复合字体的后代字体已经解析过。
	// （占位：后代只需要 /W 与 /DW，不需要单独编码。）
}

// decode 把一段原始字符串按字节码切成 Unicode 字符。
//
// 返回值是"字符 + 该字符的推进宽度（1/1000 em）"的序列。
// 宽度必须一起给：内容流解释器要靠它推进文本矩阵，
// 而文本矩阵的位置决定了这段文字最终落在页面的哪里。
type glyph struct {
	r     rune
	code  int
	width float64
}

func (f *font) decode(raw []byte) []glyph {
	if f == nil {
		return nil
	}
	out := make([]glyph, 0, len(raw))
	if f.twoByte {
		for i := 0; i+1 < len(raw); i += 2 {
			code := int(raw[i])<<8 | int(raw[i+1])
			out = append(out, f.glyphOf(code))
		}
		// 落单的一个字节：坏文件里出现过，按单字节编码试一次。
		if len(raw)%2 == 1 {
			out = append(out, f.glyphOf(int(raw[len(raw)-1])))
		}
		return out
	}
	for _, b := range raw {
		out = append(out, f.glyphOf(int(b)))
	}
	return out
}

func (f *font) glyphOf(code int) glyph {
	g := glyph{code: code}
	if r, ok := f.toUnicode[code]; ok && r != 0 {
		g.r = r
	} else {
		// 映射缺失时宁可输出替换符，也不要臆造：伪造出的字符会被
		// 当成真题内容送给模型，而模型不会知道自己看到的是假的。
		g.r = unicode.ReplacementChar
	}
	if w, ok := f.widths[code]; ok {
		g.width = w
	} else {
		g.width = f.defaultWidth
	}
	return g
}

// spaceCode 该字体里空格的字节码，用来识别 TJ 数组里的大幅负间距。
func (f *font) spaceCode() int {
	if !f.twoByte {
		return 32
	}
	for code, r := range f.toUnicode {
		if r == ' ' {
			return code
		}
	}
	return 32
}

// ---- 字体构造 ----

// loadFont 从字体字典构造解码器。
func (d *Document) loadFont(dict Dict) *font {
	f := &font{defaultWidth: 500}

	subtype, _ := d.nameOf(dict, "Subtype")
	if subtype == "Type0" {
		f.twoByte = true
		// 复合字体的编码几乎总是 Identity-H/V：两字节直接当 CID 用。
		if encName, ok := d.nameOf(dict, "Encoding"); ok {
			switch encName {
			case "Identity-H", "Identity-V":
			default:
				// 其它 CMap 编码（如 UniGB-UCS2-H）本可以查内置表，
				// 但那是一张几百项的表，而带 /ToUnicode 的文件
				// 本来就不需要它：ToUnicode 已经是权威映射。
			}
		}
		// 复合字体的字宽表挂在后代字体上。
		if arr, ok := d.arrayOf(dict, "DescendantFonts"); ok && len(arr) > 0 {
			if df, ok := d.resolve(arr[0]).(Dict); ok {
				d.loadCIDWidths(f, df)
			}
		}
		f.defaultWidth = 1000
	} else {
		if w, ok := d.arrayOf(dict, "Widths"); ok && len(w) > 0 {
			f.ensureWidths()
			first, hasFirst := d.intOf(dict, "FirstChar")
			for i, item := range w {
				width, _ := toFloat(item)
				code := i
				if hasFirst {
					code = int(first) + i
				}
				f.widths[code] = width
			}
		}
		if dw, ok := d.floatOf(dict, "MissingWidth"); ok && dw > 0 {
			f.defaultWidth = dw
		}
		// Type3 字体的宽度用 FontMatrix 折算：矩阵里 x 是 0.001 em 单位。
		if subtype == "Type3" {
			if fm, ok := d.arrayOf(dict, "FontMatrix"); ok {
				v := floatSlice(fm)
				if len(v) == 6 && v[0] != 0 {
					f.scaleWidths(v[0])
				}
			}
		}
	}

	d.loadToUnicode(f, dict)
	if len(f.toUnicode) == 0 {
		d.loadEncodingNames(f, dict, subtype)
	}
	return f
}

// loadCIDWidths 读后代 CID 字体的 /W 与 /DW。
func (d *Document) loadCIDWidths(f *font, df Dict) {
	if dw, ok := d.floatOf(df, "DW"); ok && dw > 0 {
		f.defaultWidth = dw
	}
	arr, ok := d.arrayOf(df, "W")
	if !ok {
		return
	}
	i := 0
	for i < len(arr) {
		first, ok1 := toInt(arr[i])
		i++
		if !ok1 || i >= len(arr) {
			break
		}
		switch v := arr[i].(type) {
		case Array:
			// [cFirst [w1 w2 …]] 形式。
			i++
			f.ensureWidths()
			for j, item := range v {
				w, _ := toFloat(item)
				f.widths[int(first)+j] = w
			}
		default:
			// [cFirst cLast w] 形式。
			last, ok2 := toInt(v)
			if !ok2 || i+1 >= len(arr) {
				i++
				continue
			}
			w, _ := toFloat(arr[i+1])
			i += 2
			if last < first {
				continue
			}
			// 区间可能横跨整段 CJK，逐个展开会让字宽表膨胀到几万个条目。
			// 因此单段区间只保留首尾的显式值，其余交给 defaultWidth。
			f.ensureWidths()
			f.widths[int(first)] = w
			f.widths[int(last)] = w
		}
	}
}

func (f *font) scaleWidths(k float64) {
	for c, w := range f.widths {
		f.widths[c] = w * k
	}
	f.defaultWidth *= k
}

// ensureWidths 懒建字宽表。
//
// 字宽表只在字体真的带 /W 或 /Widths 时才有内容，而 Type3 字体
// 与内置数学字体常常两者都没有。没有这层懒建，第一处写入就会 panic。
func (f *font) ensureWidths() {
	if f.widths == nil {
		f.widths = map[int]float64{}
	}
}

// loadToUnicode 解析 /ToUnicode CMap。
//
// 这是 PDF 里**唯一**可靠的编码→字符映射：字体子集可以任意重排码位，
// 编码名字也可能完全对不上，但导出器写 ToUnicode 时一定按实际字形写。
func (d *Document) loadToUnicode(f *font, dict Dict) {
	s, ok := d.streamOf(dict, "ToUnicode")
	if !ok {
		return
	}
	data, err := s.Bytes()
	if err != nil {
		return
	}
	parseCMap(data, func(code int, r rune) {
		if f.toUnicode == nil {
			f.toUnicode = map[int]rune{}
		}
		f.toUnicode[code] = r
	})
}

// parseCMap 遍历 CMap，取出 bfchar 与 bfrange 段的映射。
//
// 刻意只认这两个段：嵌入字体文件（bfchar 之外的 beginbf…t 文件段）
// 对取文字没有帮助，而解析它需要的 CID→GID 与字形度量映射表
// 是另一个数量级的活。
func parseCMap(data []byte, put func(code int, r rune)) {
	lx := newLexer(data, 0)

	// 保留最近一次读到的操作数，按操作符配对。
	//
	// 段与段之间的边界由 endbfchar / endbfrange 划出来，所以这里
	// **不能**在攒够几个就把旧的丢掉：一段 bfchar 动辄几百对
	// （子集化的中文字体轻松上千），提前截断会静默丢掉绝大部分映射。
	// 上限只用来挡住"没有 end 标记"的坏文件。
	var ops []Object
	const maxCMapOps = 1 << 16

	for {
		t := lx.next()
		if t.kind == tokEOF {
			return
		}
		if t.kind != tokKeyword {
			ops = append(ops, lx.parseFrom(nil, t))
			if len(ops) > maxCMapOps {
				ops = ops[len(ops)-3:]
			}
			continue
		}
		switch t.keyword {
		case "endbfchar":
			for i := 0; i+1 < len(ops); i += 2 {
				if code, ok1 := cmapCode(ops[i]); ok1 {
					if r, ok2 := cmapRune(ops[i+1]); ok2 {
						put(code, r)
					}
				}
			}
			ops = ops[:0]

		case "endbfrange":
			for i := 0; i+2 < len(ops); i += 3 {
				lo, ok1 := cmapCode(ops[i])
				hi, ok2 := cmapCode(ops[i+1])
				if !ok1 || !ok2 || hi < lo {
					continue
				}
				// 第三项可以是数组（逐个列出），也可以是一个起始值（等差）。
				if arr, ok := ops[i+2].(Array); ok {
					for j, item := range arr {
						if r, ok3 := cmapRune(item); ok3 {
							put(lo+j, r)
						}
					}
					continue
				}
				base, ok3 := cmapRune(ops[i+2])
				if !ok3 {
					continue
				}
				// 上限保护：坏文件里 hi 可能是个天文数字。
				if hi-lo > 1<<16 {
					hi = lo + 1<<16
				}
				for c := lo; c <= hi; c++ {
					put(c, base+rune(c-lo))
				}
			}
			ops = ops[:0]

		default:
			// 不认识的操作符同样要清空操作数：CMap 头部堆着一长串
			// def / findresource / begin，留着会污染下一段的操作数，
			// 于是第一段 bfchar 之前的所有值都被算进了它。
			ops = ops[:0]
		}
	}
}

// cmapCode 把 CMap 里的源码位读成整数。
//
// 关键在于源码位写的是 <0033> 这种**十六进制串**，解出来是字节
// 0x00 0x33 —— 它表示的是码值 0x0033，而不是数字 33。当成普通数值
// 处理的话整张映射表会静默地一条都填不进去，最后每个字符都变成
// 替换符，而且没有任何报错。
func cmapCode(o Object) (int, bool) {
	switch v := o.(type) {
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	case string:
		if v == "" {
			return 0, false
		}
		code := 0
		for _, b := range []byte(v) {
			code = code<<8 | int(b)
		}
		return code, true
	}
	return 0, false
}

// cmapRune 把 CMap 里的目标值读成字符。
//
// 目标写的是 <4E00> / <0020>，按 UTF-16BE 解释：绝大多数是单码元，
// 但代理对也会出现，因此真的按 UTF-16 解一次。
func cmapRune(o Object) (rune, bool) {
	switch v := o.(type) {
	case string:
		u := utf16BE(v)
		if len(u) == 0 {
			return 0, false
		}
		return u[0], true
	case int64:
		return rune(v), true
	case float64:
		return rune(v), true
	case Name:
		// bfrange 里允许用 /glyphname 指代起点字符。
		if r, ok := glyphNameToRune(string(v)); ok {
			return r, true
		}
		return 0, false
	}
	return 0, false
}

// utf16BE 把字节串按 UTF-16BE 解成码元序列。
func utf16BE(s string) []rune {
	b := []byte(s)
	if len(b)%2 == 1 {
		// 奇数长度只可能来自损坏文件：丢掉末尾那半个。
		b = b[:len(b)-1]
	}
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		u = append(u, uint16(b[i])<<8|uint16(b[i+1]))
	}
	return utf16.Decode(u)
}

// loadEncodingNames 在没有 ToUnicode 时退回到编码名字表。
//
// 只覆盖简单字体：复合字体的码位是 CID，没有 /ToUnicode 时
// 根本没有可靠的还原方式（那正是子集化字体的常态）。
func (d *Document) loadEncodingNames(f *font, dict Dict, subtype Name) {
	if subtype == "Type0" || subtype == "CIDFontType0" || subtype == "CIDFontType2" {
		return
	}
	base := Name("StandardEncoding")
	diffs := map[int]Name{}

	if encVal := d.get(dict, "Encoding"); encVal != nil {
		switch v := encVal.(type) {
		case Name:
			base = v
		case Dict:
			if b, ok := d.nameOf(v, "BaseEncoding"); ok {
				base = b
			}
			if arr, ok := d.arrayOf(v, "Differences"); ok {
				code := 0
				for _, item := range arr {
					switch n := item.(type) {
					case int64:
						code = int(n)
					case float64:
						code = int(n)
					case Name:
						diffs[code] = n
						code++
					}
				}
			}
		}
	}

	table := encodingTable(base)
	for code, name := range diffs {
		if r, ok := glyphNameToRune(string(name)); ok {
			if f.toUnicode == nil {
				f.toUnicode = map[int]rune{}
			}
			f.toUnicode[code] = r
			continue
		}
		// /Differences 里也可能直接写 /uniXXXX 或 /uXXXX[XX]。
		if r, ok := unicodeFromGlyphName(string(name)); ok {
			if f.toUnicode == nil {
				f.toUnicode = map[int]rune{}
			}
			f.toUnicode[code] = r
		}
	}
	// 没有 /Differences 的码位按基础编码表补齐。
	if f.toUnicode == nil {
		f.toUnicode = map[int]rune{}
	}
	for code, r := range table {
		if _, exists := f.toUnicode[code]; !exists {
			f.toUnicode[code] = r
		}
	}
}

// glyphNameToRune 从字形名（AGL 子集）取 Unicode。
func glyphNameToRune(name string) (rune, bool) {
	if len(name) == 1 {
		r := rune(name[0])
		if r < 128 {
			return r, true
		}
	}
	if r, ok := glyphNames[name]; ok {
		return r, true
	}
	return unicodeFromGlyphName(name)
}

// unicodeFromGlyphName 处理 uniXXXX / uXXXX 与 gNN/cidNN 之外的
// 少见写法：单字符名、"uniXXXXXX" 六位形式。
func unicodeFromGlyphName(name string) (rune, bool) {
	switch {
	case strings.HasPrefix(name, "uni") && len(name) >= 7:
		if v, err := strconv.ParseUint(name[3:7], 16, 32); err == nil {
			return rune(v), true
		}
	case strings.HasPrefix(name, "u") && len(name) >= 5 && len(name) <= 7:
		if v, err := strconv.ParseUint(name[1:], 16, 32); err == nil {
			return rune(v), true
		}
	}
	return 0, false
}

// glyphNames 是 AGL 里跟录题真正相关的那部分字形名。
//
// 刻意只收数学与拉丁常用项：完整 AGL 有 4000 多条，收全是几百行
// 常量，而这些之外的字形名在试卷里几乎不出现。
var glyphNames = map[string]rune{
	// 希腊字母（数学题主力）
	"alpha": 'α', "beta": 'β', "gamma": 'γ', "delta": 'δ',
	"epsilon": 'ε', "zeta": 'ζ', "eta": 'η', "theta": 'θ',
	"iota": 'ι', "kappa": 'κ', "lambda": 'λ', "mu": 'μ',
	"nu": 'ν', "xi": 'ξ', "omicron": 'ο', "pi": 'π',
	"rho": 'ρ', "sigma": 'σ', "tau": 'τ', "upsilon": 'υ',
	"phi": 'φ', "chi": 'χ', "psi": 'ψ', "omega": 'ω',
	"Alpha": 'Α', "Beta": 'Β', "Gamma": 'Γ', "Delta": 'Δ',
	"Theta": 'Θ', "Lambda": 'Λ', "Xi": 'Ξ', "Pi": 'Π',
	"Sigma": 'Σ', "Phi": 'Φ', "Psi": 'Ψ', "Omega": 'Ω',

	// 常用符号
	"space": ' ', "exclam": '!', "quotedbl": '"', "numbersign": '#',
	"dollar": '$', "percent": '%', "ampersand": '&', "quotesingle": '\'',
	"parenleft": '(', "parenright": ')', "asterisk": '*', "plus": '+',
	"comma": ',', "hyphen": '-', "period": '.', "slash": '/',
	"zero": '0', "one": '1', "two": '2', "three": '3', "four": '4',
	"five": '5', "six": '6', "seven": '7', "eight": '8', "nine": '9',
	"colon": ':', "semicolon": ';', "less": '<', "equal": '=',
	"greater": '>', "question": '?', "at": '@',
	"bracketleft": '[', "backslash": '\\', "bracketright": ']',
	"asciicircum": '^', "underscore": '_', "grave": '`',
	"braceleft": '{', "bar": '|', "braceright": '}', "asciitilde": '~',

	// 数学运算符（数学试卷出现频率最高的非字母符号）
	"minus": '−', "periodcentered": '·', "multiply": '×',
	"divide": '÷', "plusminus": '±', "minusplus": '∓',
	"degree": '°', "minute": '′', "second": '″',
	"radical": '√', "infinity": '∞', "summation": '∑',
	"product": '∏', "integral": '∫', "partialdiff": '∂',
	"element": '∈', "notelement": '∉', "universal": '∀',
	"existential": '∃', "arrowright": '→', "arrowleft": '←',
	"arrowup": '↑', "arrowdown": '↓', "arrowboth": '↔',
	"lessequal": '≤', "greaterequal": '≥', "notequal": '≠',
	"approxequal": '≈', "equivalence": '≡', "proportional": '∝',
	"similar": '∼', "angle": '∠', "perpendicular": '⊥',
	"parallel": '∥', "logicaland": '∧', "logicalor": '∨',
	"intersection": '∩', "union": '∪', "emptyset": '∅',
	"propersubset": '⊂', "propersuperset": '⊃', "reflexsubset": '⊆',
	"reflexsuperset": '⊇', "circleplus": '⊕', "circlemultiply": '⊗',
	"therefore": '∴', "congruent": '≅', "bullet": '•',
	"emdash": '—', "endash": '–', "quotedblleft": '“',
	"quotedblright": '”', "quoteleft": '‘', "quoteright": '’',
	"dotmath": '⋅', "fraction": '⁄', "lozenge": '◊',
	"dagger": '†', "daggerdbl": '‡', "section": '§',
	"paragraph": '¶', "florin": 'ƒ', "germandbls": 'ß',
}

// encodingTable 返回基础编码表（码位 → 字符）。
func encodingTable(base Name) map[int]rune {
	table := map[int]rune{}
	// 标准编码里 32..126 与 ASCII 一致，这一段直接给出。
	for c := 32; c <= 126; c++ {
		table[c] = rune(c)
	}
	switch base {
	case "MacRomanEncoding":
		// MacRoman 与 ASCII 相同的地方已经填好；补几个常见的重音与符号。
		table[0xA5] = '•'
		table[0xA7] = 'ß'
		table[0xB5] = 'µ'
		table[0xD0] = '—'
		table[0xD1] = '“'
		table[0xD2] = '”'
	case "WinAnsiEncoding":
		table[0x80] = '€'
		table[0x82] = '‚'
		table[0x83] = 'ƒ'
		table[0x84] = '„'
		table[0x85] = '…'
		table[0x86] = '†'
		table[0x87] = '‡'
		table[0x88] = 'ˆ'
		table[0x89] = '‰'
		table[0x8A] = 'Š'
		table[0x8B] = '‹'
		table[0x8C] = 'Œ'
		table[0x91] = '‘'
		table[0x92] = '’'
		table[0x93] = '“'
		table[0x94] = '”'
		table[0x95] = '•'
		table[0x96] = '–'
		table[0x97] = '—'
		table[0x98] = '˜'
		table[0x99] = '™'
		table[0x9A] = 'š'
		table[0x9B] = '›'
		table[0x9C] = 'œ'
		table[0x9F] = 'Ÿ'
	}
	// 标准编码里的排版符号与制表符。
	table[0x09] = '\t'
	return table
}
