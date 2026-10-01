package pdftext

import (
	"math"
	"strings"
)

// textRun 是一段已经定位好的文字。
//
// 坐标存的是**页面坐标系**（已经乘过页面自身的平移与旋转），
// 而且 y 仍然是向上的。版面还原那一层负责把它翻成"从上往下读"，
// 但这一层不该改坐标：一旦在这里翻转，后面每次比较都要记得翻回来，
// 迟早有一处忘掉。
type textRun struct {
	text string
	// x 左边界，y 基线。
	x, y float64
	// w 这一段的推进宽度（沿文字方向的长度），h 字号的近似高度。
	// 两者都取变换后的模长，因此页面被缩放或旋转时依然成立。
	w, h float64
}

// graphicsState 是内容流里 q/Q 保存与恢复的那份状态。
type graphicsState struct {
	ctm [6]float64

	// 文本相关的部分。
	font       *font
	fontSize   float64
	charSpace  float64 // Tc
	wordSpace  float64 // Tw
	horizScale float64 // Tz
	leading    float64 // TL
	rise       float64 // Ts
	renderMode int

	textMatrix [6]float64
	lineMatrix [6]float64
	inText     bool
}

// interpreter 逐个操作符地走内容流。
type interpreter struct {
	doc  *Document
	base [6]float64 // 页面自身变换（平移 + /Rotate）

	runs  []textRun
	state graphicsState
	stack []graphicsState

	// fonts 页面资源里的字体，惰性加载：同一个字体在一页里会被选中
	// 上百次，每次重载都要重新解压一遍 ToUnicode。
	fonts map[Name]*font

	// depth 限制表单 XObject 的递归层数。
	depth int
}

// maxFormDepth 表单嵌套上限。正常卷子不超过两层。
const maxFormDepth = 8

// runPage 走完一页的内容流，返回取到的文字片段。
func runPage(d *Document, p *page) []textRun {
	in := &interpreter{
		doc:   d,
		base:  pageBase(p),
		fonts: map[Name]*font{},
	}
	in.state = graphicsState{
		ctm:        in.base,
		horizScale: 1,
		fontSize:   12,
	}
	in.exec(p.content, p.resources)
	return in.runs
}

// pageBase 算出页面自身的变换矩阵。
//
// 两件事：把 (0,0) 从 MediaBox 的左下角挪到原点，
// 再套上 /Rotate。扫描件和从手机拍来的卷子常常带 /Rotate 90/270，
// 不处理的话整页文字会横着排、读出来是一串换行错乱的碎片。
func pageBase(p *page) [6]float64 {
	ox, oy := -p.mediaBox[0], -p.mediaBox[1]
	// 旋转是绕 MediaBox 左下角进行的，所以平移量要跟着旋转一起算。
	var rot [6]float64
	switch p.rotate {
	case 90:
		rot = [6]float64{0, 1, 1, 0, 0, 0}
	case 180:
		rot = [6]float64{-1, 0, 0, -1, 0, 0}
	case 270:
		rot = [6]float64{0, -1, -1, 0, 0, 0}
	default:
		rot = [6]float64{1, 0, 0, 1, 0, 0}
	}
	// 先绕原点转，再把结果挪回 MediaBox 左下角。
	return mul(rot, translation(ox, oy))
}

func identityMatrix() [6]float64 { return [6]float64{1, 0, 0, 1, 0, 0} }

func translation(x, y float64) [6]float64 { return [6]float64{1, 0, 0, 1, x, y} }

// mul 按 PDF 约定返回 a×b：先施加 b，再施加 a。
func mul(a, b [6]float64) [6]float64 {
	return [6]float64{
		a[0]*b[0] + a[1]*b[2],
		a[0]*b[1] + a[1]*b[3],
		a[2]*b[0] + a[3]*b[2],
		a[2]*b[1] + a[3]*b[3],
		a[4]*b[0] + a[5]*b[2] + b[4],
		a[4]*b[1] + a[5]*b[3] + b[5],
	}
}

// apply 把点 (x,y) 送进矩阵。
func apply(m [6]float64, x, y float64) (float64, float64) {
	return m[0]*x + m[2]*y + m[4], m[1]*x + m[3]*y + m[5]
}

// exec 执行一段内容流。
func (in *interpreter) exec(content []byte, resources Dict) {
	lx := newLexer(content, 0)

	// 内容流是后缀记法：先读值，再读操作符，因此需要一个操作数栈。
	var ops []Object
	const maxOps = 64

	for {
		t := lx.next()
		if t.kind == tokEOF {
			return
		}
		if t.kind != tokKeyword {
			// t 已经被 next() 读出来了，直接就地解析。
			// 这里再调一次 next() 会多吃掉一个记号，整个内容流随之错位，
			// 表现是"一句文字都抽不出来"。
			ops = append(ops, lx.parseFrom(nil, t))
			if len(ops) > maxOps {
				ops = ops[1:]
			}
			continue
		}

		switch t.keyword {
		case "true":
			ops = append(ops, true)
			continue
		case "false":
			ops = append(ops, false)
			continue
		case "null":
			ops = append(ops, nil)
			continue
		case "BI":
			// 内联图像：BI … ID <像素> EI。中间的像素数据不是内容流语法，
			// 让词法分析去读只会读出一堆垃圾再报一堆看不懂的错。
			lx.pos = skipInlineImage(content, lx.pos)
			ops = ops[:0]
			continue
		}

		in.apply(t.keyword, ops, resources)
		ops = ops[:0]
	}
}

// apply 执行一个操作符。
//
// 不认识的操作符直接忽略：内容流里除了本文列的这些，还有几十种
// 与取文字无关的算子（ri、gs、sh、d0…），遇到它们必须能安然跳过。
func (in *interpreter) apply(op string, ops []Object, resources Dict) {
	st := &in.state

	switch op {
	// ---- 图形状态 ----
	case "q":
		in.stack = append(in.stack, *st)
		if len(in.stack) > 64 {
			in.stack = in.stack[1:]
		}
	case "Q":
		if n := len(in.stack); n > 0 {
			*st = in.stack[n-1]
			in.stack = in.stack[:n-1]
		}
	case "cm":
		if m, ok := matrixOfN(ops, 6); ok {
			st.ctm = mul(m, st.ctm)
		}

	// ---- 文本对象 ----
	case "BT":
		st.inText = true
		st.textMatrix = identityMatrix()
		st.lineMatrix = identityMatrix()
	case "ET":
		st.inText = false

	case "Tf":
		if len(ops) >= 2 {
			if name, ok := ops[len(ops)-2].(Name); ok {
				st.font = in.loadFont(resources, name)
			}
			if size, ok := toFloat(ops[len(ops)-1]); ok {
				st.fontSize = size
			}
		}
	case "Td":
		if len(ops) >= 2 {
			tx, _ := toFloat(ops[len(ops)-2])
			ty, _ := toFloat(ops[len(ops)-1])
			st.lineMatrix = mul(translation(tx, ty), st.lineMatrix)
			st.textMatrix = st.lineMatrix
		}
	case "TD":
		// TD 等价于"先设行距，再 Td"。
		if len(ops) >= 2 {
			tx, _ := toFloat(ops[len(ops)-2])
			ty, _ := toFloat(ops[len(ops)-1])
			st.leading = -ty
			st.lineMatrix = mul(translation(tx, ty), st.lineMatrix)
			st.textMatrix = st.lineMatrix
		}
	case "Tm":
		if m, ok := matrixOfN(ops, 6); ok {
			st.lineMatrix = m
			st.textMatrix = m
		}
	case "T*":
		in.nextLine()

	// ---- 文本参数 ----
	case "Tc":
		if len(ops) >= 1 {
			st.charSpace, _ = toFloat(ops[len(ops)-1])
		}
	case "Tw":
		if len(ops) >= 1 {
			st.wordSpace, _ = toFloat(ops[len(ops)-1])
		}
	case "Tz":
		if len(ops) >= 1 {
			if v, ok := toFloat(ops[len(ops)-1]); ok && v > 0 {
				st.horizScale = v / 100
			}
		}
	case "TL":
		if len(ops) >= 1 {
			st.leading, _ = toFloat(ops[len(ops)-1])
		}
	case "Ts":
		if len(ops) >= 1 {
			st.rise, _ = toFloat(ops[len(ops)-1])
		}
	case "Tr":
		if len(ops) >= 1 {
			if v, ok := toInt(ops[len(ops)-1]); ok {
				st.renderMode = int(v)
			}
		}

	// ---- 显示文字 ----
	case "Tj":
		if s, ok := lastString(ops); ok {
			in.show([]byte(s))
		}
	case "'":
		in.nextLine()
		if s, ok := lastString(ops); ok {
			in.show([]byte(s))
		}
	case "\"":
		if len(ops) >= 3 {
			if v, ok := toFloat(ops[len(ops)-3]); ok {
				st.wordSpace = v
			}
			if v, ok := toFloat(ops[len(ops)-2]); ok {
				st.charSpace = v
			}
			in.nextLine()
			if s, ok := lastString(ops); ok {
				in.show([]byte(s))
			}
		}
	case "TJ":
		if arr, ok := lastArray(ops); ok {
			in.showArray(arr)
		}

	// ---- XObject ----
	case "Do":
		if name, ok := lastName(ops); ok {
			in.doXObject(resources, name)
		}
	}
}

// nextLine 移到下一行开头。
func (in *interpreter) nextLine() {
	st := &in.state
	st.lineMatrix = mul(translation(0, -st.leading), st.lineMatrix)
	st.textMatrix = st.lineMatrix
}

// showArray 处理 TJ 数组：里面既有字符串，也有表示间距的负数。
//
// 这些负数是**排版微调**，但当它大到超过一个空格宽度时，
// 它的真实含义就是"这里原本有一个空格"——导出器常用一个明显更大的
// 跳回来表示词间空格，因为 CJK 与西文字宽不同。不做这一步，
// 英文单词会被粘成一坨。
func (in *interpreter) showArray(arr Array) {
	for _, item := range arr {
		switch v := item.(type) {
		case string:
			in.show([]byte(v))
		case int64:
			in.adjust(-float64(v))
		case float64:
			in.adjust(-v)
		}
	}
}

// adjust 按 TJ 数组里的间距值平移文本矩阵。
//
// 单位是千分之一文本空间单位，因此要除以 1000 再乘字号；
// 还要乘上 Tz 的水平缩放，否则非 100% 的缩放会让位置慢慢漂走。
func (in *interpreter) adjust(delta float64) {
	st := &in.state
	tx := delta / 1000 * st.fontSize * st.horizScale
	st.textMatrix = mul(translation(tx, 0), st.textMatrix)
}

// show 显示一段文字并推进文本矩阵。
func (in *interpreter) show(raw []byte) {
	st := &in.state
	if st.font == nil || len(raw) == 0 {
		return
	}
	// 渲染模式 3 与 7 是不可见文字（扫描件 OCR 的底图、剪贴路径）。
	// 把它们当正文抽出来，用户会凭空多出几段重复内容。
	if st.renderMode == 3 || st.renderMode == 7 {
		return
	}

	// 变换矩阵：文本矩阵给出位置，CTM 给出页面上的朝向。
	tm := mul(st.textMatrix, st.ctm)
	scale := st.horizScale
	if scale <= 0 {
		scale = 1
	}

	glyphs := st.font.decode(raw)
	spaceCode := st.font.spaceCode()

	var text strings.Builder
	advance := 0.0
	for _, g := range glyphs {
		text.WriteRune(g.r)
		w := g.width/1000*st.fontSize + st.charSpace
		if !st.font.twoByte && g.code == spaceCode {
			w += st.wordSpace
		}
		advance += w
	}

	// 起点与终点：宽度取两者的距离，这样页面被旋转时宽度依然正确。
	x0, y0 := apply(tm, 0, st.rise)
	x1, y1 := apply(tm, advance*scale, st.rise)

	in.runs = append(in.runs, textRun{
		text: text.String(),
		x:    x0,
		y:    y0,
		w:    math.Hypot(x1-x0, y1-y0),
		h:    math.Hypot(tm[0], tm[1]) * math.Max(1, math.Abs(st.fontSize)),
	})

	st.textMatrix = mul(translation(advance*scale, 0), st.textMatrix)
}

// doXObject 执行一个 XObject。表单会被递归展开，图像忽略。
func (in *interpreter) doXObject(resources Dict, name Name) {
	if in.depth > maxFormDepth {
		return
	}
	xdict, ok := in.doc.dictOf(resources, "XObject")
	if !ok {
		return
	}
	obj, ok := in.doc.get(xdict, name).(*Stream)
	if !ok {
		return
	}
	if kind, _ := in.doc.nameOf(obj.Dict, "Subtype"); kind != "Form" {
		return // 图像：里面没有文字可取
	}

	// 表单自带资源时用它自己的，外层资源补缺：真实文件两种写法都有，
	// 而且外层往往才是父页面那份共享资源。
	sub := resources
	if own, ok := in.doc.dictOf(obj.Dict, "Resources"); ok {
		sub = own
	}

	saved := in.state
	savedStack := len(in.stack)

	if m, ok := matrixOf6(in.doc, obj.Dict, "Matrix"); ok {
		in.state.ctm = mul(m, in.state.ctm)
	}
	data, err := obj.Bytes()
	if err != nil {
		return
	}
	in.depth++
	in.exec(data, sub)
	in.depth--

	in.state = saved
	if len(in.stack) > savedStack {
		in.stack = in.stack[:savedStack]
	}
}

func matrixOfN(ops []Object, n int) ([6]float64, bool) {
	if len(ops) < n {
		return [6]float64{}, false
	}
	tail := ops[len(ops)-n:]
	var m [6]float64
	for i := 0; i < n; i++ {
		v, ok := toFloat(tail[i])
		if !ok {
			return m, false
		}
		m[i] = v
	}
	return m, true
}

func matrixOf6(d *Document, dict Dict, key Name) ([6]float64, bool) {
	arr, ok := d.arrayOf(dict, key)
	if !ok || len(arr) < 6 {
		return [6]float64{}, false
	}
	var m [6]float64
	for i := 0; i < 6; i++ {
		v, ok := toFloat(arr[i])
		if !ok {
			return m, false
		}
		m[i] = v
	}
	return m, true
}

func lastString(ops []Object) (string, bool) {
	if len(ops) == 0 {
		return "", false
	}
	s, ok := ops[len(ops)-1].(string)
	return s, ok
}

func lastName(ops []Object) (Name, bool) {
	if len(ops) == 0 {
		return "", false
	}
	n, ok := ops[len(ops)-1].(Name)
	return n, ok
}

func lastArray(ops []Object) (Array, bool) {
	if len(ops) == 0 {
		return nil, false
	}
	arr, ok := ops[len(ops)-1].(Array)
	return arr, ok
}

// loadFont 惰性加载并缓存页面字体。
func (in *interpreter) loadFont(resources Dict, name Name) *font {
	if f, ok := in.fonts[name]; ok {
		return f
	}
	entry, ok := in.doc.dictOf(resources, "Font")
	if !ok {
		in.fonts[name] = nil
		return nil
	}
	fontDict, ok := in.doc.get(entry, name).(Dict)
	if !ok {
		in.fonts[name] = nil
		return nil
	}
	f := in.doc.loadFont(fontDict)
	in.fonts[name] = f
	return f
}

// skipInlineImage 跳过内联图像的数据段（从 BI 之后到 EI）。
//
// 数据里几乎必然含有会被误认成 endstream 的字节序列，
// 因此不能靠"找 EI 关键字"了事——必须再确认 EI 后面
// 跟的是分隔符或文件结束，否则就会在图像里提前截断。
func skipInlineImage(data []byte, from int) int {
	pos := from
	for {
		idx := indexFrom(data, []byte("EI"), pos)
		if idx < 0 {
			return len(data)
		}
		end := idx + 2
		if end >= len(data) {
			return end
		}
		if isSpace(data[end]) || isDelim(data[end]) {
			return end
		}
		pos = end
	}
}
