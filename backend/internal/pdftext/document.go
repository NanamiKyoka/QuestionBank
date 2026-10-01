package pdftext

import (
	"bytes"
	"regexp"
	"sort"
	"strconv"
)

// Document 是一份打开的 PDF。
//
// 索引策略是**两段式**：先按 xref（表或流）建索引，再按需解析对象；
// 任何一步失败都退回"全文件扫描重建索引"。这看着像是保守，
// 但 PDF 的 xref 损坏在实际文件里非常常见——增量保存写坏偏移、
// 第三方工具写出非标准偏移量——而扫描重建几乎总能救回来。
// 只走 xref 的话，这类文件会直接变成"无法导入"。
type Document struct {
	data []byte

	// offsets 对象号 → 文件偏移（来自 xref 或扫描）。
	offsets map[int]int64
	// compressed 对象号 → (对象流编号, 在流内的序号)。
	compressed map[int][2]int

	trailer Dict
	catalog Dict

	// parsed 已解析的对象缓存。键含世代号，避免不同世代互相覆盖。
	parsed map[Ref]Object
	// objStmDecoded 已展开的对象流内容，避免重复解压。
	objStmDecoded map[int][]byte
	// objStmTables 对象流的头表：对象流号 → (对象号 → 字节偏移, /First)。
	objStmTables map[int]map[int]int

	// scanned 表示索引是扫描重建的：此时不信任任何声明的长度。
	scanned bool
}

// Open 打开一份 PDF。
func Open(data []byte) (*Document, error) {
	if len(data) < 32 || string(data[:5]) != "%PDF-" {
		return nil, errMalformed("这不是一个 PDF 文件")
	}
	d := &Document{
		data:          data,
		offsets:       map[int]int64{},
		compressed:    map[int][2]int{},
		parsed:        map[Ref]Object{},
		objStmDecoded: map[int][]byte{},
		objStmTables:  map[int]map[int]int{},
	}
	if err := d.loadXref(); err != nil {
		// xref 读不懂就重建。这是"能导入"与"打不开一份真卷子"的区别。
		d.rebuild()
	}
	if d.trailer == nil {
		d.rebuild()
	}
	if err := d.findCatalog(); err != nil {
		return nil, err
	}
	return d, nil
}

// ---- xref ----

// startxrefRe 找文件末尾的 startxref 指针。
var startxrefRe = regexp.MustCompile(`startxref\s+(\d+)`)

// loadXref 顺着 /Prev 链把交叉引用表读进来。
func (d *Document) loadXref() error {
	tail := d.data
	if len(tail) > 2048 {
		tail = tail[len(tail)-2048:]
	}
	m := startxrefRe.FindAllSubmatch(tail, -1)
	if len(m) == 0 {
		return errMalformed("找不到 startxref")
	}
	// 最后一个才是当前的起点。
	off, err := strconv.ParseInt(string(m[len(m)-1][1]), 10, 64)
	if err != nil {
		return errMalformed("startxref 偏移不可解析")
	}

	seen := map[int64]bool{}
	for cur := off; cur > 0 && cur < int64(len(d.data)); {
		if seen[cur] {
			return errMalformed("xref 链成环")
		}
		seen[cur] = true

		next, err := d.readXrefSection(cur)
		if err != nil {
			return err
		}
		cur = next
	}
	if len(d.offsets) == 0 && len(d.compressed) == 0 {
		return errMalformed("交叉引用表是空的")
	}
	return nil
}

// readXrefSection 读一节 xref，返回下一节的偏移（0 表示到头了）。
func (d *Document) readXrefSection(off int64) (int64, error) {
	if off < 0 || off >= int64(len(d.data)) {
		return 0, errMalformed("xref 偏移越界：%d", off)
	}
	lx := newLexer(d.data, int(off))
	t := lx.next()

	switch {
	case t.kind == tokKeyword && t.keyword == "xref":
		return d.readXrefTable(lx)

	case t.kind == tokNumber:
		// PDF 1.5 的交叉引用**流**：写成 "N G obj << /Type/XRef … >> stream"。
		return d.readXrefStreamAt(int(off))

	default:
		return 0, errMalformed("无法识别的 xref 段")
	}
}

// readXrefTable 读经典的 "xref" 关键字形式。
func (d *Document) readXrefTable(lx *lexer) (int64, error) {
	for {
		t := lx.next()
		if t.kind == tokKeyword && t.keyword == "trailer" {
			break
		}
		if t.kind != tokNumber || !t.isInt {
			// 到了文件尾也没等到 trailer：交给上层重建索引。
			return 0, errMalformed("xref 表缺少 trailer")
		}
		start := int(t.num)
		countTok := lx.next()
		if countTok.kind != tokNumber {
			return 0, errMalformed("xref 子表头损坏")
		}
		count := int(countTok.num)
		if count < 0 || count > 1<<22 {
			return 0, errMalformed("xref 子表条目数异常：%d", count)
		}

		for i := 0; i < count; i++ {
			a := lx.next()
			b := lx.next()
			kind := lx.next()
			if a.kind != tokNumber || b.kind != tokNumber || kind.kind != tokKeyword {
				return 0, errMalformed("xref 条目损坏")
			}
			// 类型 1 = 普通对象。只有类型 1 有偏移；类型 0（空闲）
			// 与类型 2（在对象流里）由对象流自己登记。
			if kind.keyword == "n" {
				objNum := start + i
				if _, dup := d.offsets[objNum]; !dup {
					d.offsets[objNum] = int64(a.num)
				}
			}
		}
	}

	trailer := Dict{}
	for {
		tt := lx.next()
		if tt.kind == tokDictOpen {
			trailer = Dict{}
			for {
				k := lx.next()
				if k.kind == tokDictClose || k.kind == tokEOF {
					break
				}
				if k.kind != tokName {
					lx.parseFrom(d, k)
					continue
				}
				trailer[Name(k.name)] = lx.parseObject(d)
			}
			break
		}
		if tt.kind == tokEOF {
			break
		}
	}
	d.mergeTrailer(trailer)

	// 混合引用文件：xref 表之外还有一个 xref 流。
	var next int64
	if s, ok := d.intOf(trailer, "XRefStm"); ok && s > 0 {
		if next2, err := d.readXrefStreamAt(int(s)); err == nil && next2 > 0 {
			next = next2
		}
	}
	if p, ok := d.intOf(trailer, "Prev"); ok {
		return p, nil
	}
	return next, nil
}

// readXrefStreamAt 按文件偏移读一节 xref 流。
func (d *Document) readXrefStreamAt(off int) (int64, error) {
	obj, err := d.parseObjectAt(off)
	if err != nil {
		return 0, err
	}
	s, ok := obj.(*Stream)
	if !ok {
		return 0, errMalformed("xref 偏移处不是一个流对象")
	}
	dict := s.Dict

	if tp, ok := d.nameOf(dict, "Type"); !ok || tp != "XRef" {
		return 0, errMalformed("偏移处的流不是 /Type /XRef")
	}

	wArr, _ := d.arrayOf(dict, "W")
	widths := floatSlice(wArr)
	if len(widths) < 3 {
		return 0, errMalformed("xref 流的 /W 至少要有三段")
	}
	size, ok := d.intOf(dict, "Size")
	if !ok || size < 0 {
		return 0, errMalformed("xref 流缺少 /Size")
	}

	data, err := s.Bytes()
	if err != nil {
		return 0, err
	}

	// 每行开头的类型字段可能是"省略的类型"（拿上一行的类型顶上）。
	prevType := int64(1)
	pos := 0
	for i := int64(0); i < size; i++ {
		row := make([]int64, len(widths))
		for c, w := range widths {
			if w == 0 {
				row[c] = prevType // 宽度为 0 表示"沿用上一行"
				continue
			}
			if pos+int(w) > len(data) {
				return 0, errMalformed("xref 流数据不足")
			}
			var v int64
			for k := 0; k < int(w); k++ {
				v = v<<8 | int64(data[pos+k])
			}
			pos += int(w)
			row[c] = v
		}
		if widths[0] != 0 {
			prevType = row[0]
		}
		switch prevType {
		case 1:
			if _, dup := d.offsets[int(i)]; !dup {
				d.offsets[int(i)] = row[1]
			}
		case 2:
			if _, dup := d.compressed[int(i)]; !dup {
				d.compressed[int(i)] = [2]int{int(row[1]), int(row[2])}
			}
		}
	}

	d.mergeTrailer(dict)

	var next int64
	if p, ok := d.intOf(dict, "Prev"); ok {
		next = p
	}
	return next, nil
}

func (d *Document) mergeTrailer(dict Dict) {
	if dict == nil {
		return
	}
	if d.trailer == nil {
		d.trailer = Dict{}
	}
	for k, v := range dict {
		if k == "Prev" || k == "XRefStm" || k == "Length" {
			continue
		}
		// 先读到的（也就是更新的一节）优先。
		if _, exists := d.trailer[k]; !exists {
			d.trailer[k] = v
		}
	}
}

// objRe 匹配 "N G obj"，用于扫描重建索引。
var objRe = regexp.MustCompile(`(?s)(?:^|[\r\n\x20\t>])(\d{1,10})[\x20\t]+(\d{1,5})[\x20\t]+obj\b`)

// rebuild 扫描整份文件重建对象索引。
//
// 扫描会误命中二进制流里的字节序列，所以每个候选都要**真的解析一遍**
// 并确认能读到 endobj —— 只有通过这一关的才登记。
func (d *Document) rebuild() {
	d.offsets = map[int]int64{}
	d.scanned = true

	for _, loc := range objRe.FindAllSubmatchIndex(d.data, -1) {
		num, _ := strconv.Atoi(string(d.data[loc[2]:loc[3]]))
		start := loc[1] // "obj" 之后的位置
		obj, err := d.parseObjectAt(start)
		if err != nil {
			continue
		}
		d.offsets[num] = int64(start)
		// 对象流同样要登记：里面的对象没有自己的文件偏移。
		if s, ok := obj.(*Stream); ok {
			if tp, _ := d.nameOf(s.Dict, "Type"); tp == "ObjStm" {
				d.indexObjStm(num)
			}
		}
	}

	// 找到最后一个 trailer（增量保存会留下多个）。
	if d.trailer == nil {
		d.trailer = Dict{}
	}
	if last := bytes.LastIndex(d.data, []byte("trailer")); last >= 0 {
		lx := newLexer(d.data, last+len("trailer"))
		if lx.next().kind == tokDictOpen {
			for {
				k := lx.next()
				if k.kind == tokDictClose || k.kind == tokEOF {
					break
				}
				if k.kind != tokName {
					lx.parseFrom(d, k)
					continue
				}
				d.trailer[Name(k.name)] = lx.parseObject(d)
			}
		}
	}
	// PDF 1.5 起没有 trailer 关键字，trailer 藏在交叉引用**流**的字典里。
	// 扫描重建时这是拿到 /Root 的唯一途径。
	if !hasKey(d.trailer, "Root") {
		d.recoverTrailerFromXRefStream()
	}
}

// recoverTrailerFromXRefStream 从交叉引用流里取回 trailer 字段。
func (d *Document) recoverTrailerFromXRefStream() {
	nums := make([]int, 0, len(d.offsets))
	for num := range d.offsets {
		nums = append(nums, num)
	}
	sort.Ints(nums)
	// 从后往前找：增量保存时最新的 xref 流在文件末尾。
	for i := len(nums) - 1; i >= 0; i-- {
		obj, err := d.parseObjectAt(int(d.offsets[nums[i]]))
		if err != nil {
			continue
		}
		s, ok := obj.(*Stream)
		if !ok {
			continue
		}
		if tp, _ := d.nameOf(s.Dict, "Type"); tp != "XRef" {
			continue
		}
		for _, key := range []Name{"Root", "Encrypt", "ID", "Info"} {
			if v, ok := s.Dict[key]; ok && !hasKey(d.trailer, key) {
				d.trailer[key] = v
			}
		}
		return
	}
}

// indexObjStm 登记一个对象流里的全部对象。
func (d *Document) indexObjStm(stmNum int) {
	idx, _, err := d.objStmIndex(stmNum)
	if err != nil {
		return
	}
	for objNum := range idx {
		if objNum == firstKey {
			continue
		}
		if _, dup := d.compressed[objNum]; !dup {
			d.compressed[objNum] = [2]int{stmNum, idx[objNum]}
		}
	}
}

// findCatalog 找到文档目录（页树的根）。
func (d *Document) findCatalog() error {
	if root, ok := d.dictOf(d.trailer, "Root"); ok {
		d.catalog = root
		if _, ok := d.dictOf(root, "Pages"); ok {
			return nil
		}
	}
	// 没有可用的 /Root（扫描重建、trailer 损坏时常见）：按 /Type /Catalog 找。
	// 两类索引都要查——分页表与字体字典往往被塞在对象流里，
	// 只扫顶层对象的话会漏掉整棵页树。
	for _, num := range d.allObjectNums() {
		obj, err := d.object(num)
		if err != nil {
			continue
		}
		dict, ok := obj.(Dict)
		if !ok {
			continue
		}
		if tp, _ := d.nameOf(dict, "Type"); tp == "Catalog" {
			d.catalog = dict
			return nil
		}
	}
	return errUnsupported("这份 PDF 缺少文档目录，可能已损坏")
}

// allObjectNums 按对象号排序返回全部对象编号。
func (d *Document) allObjectNums() []int {
	out := make([]int, 0, len(d.offsets)+len(d.compressed))
	for num := range d.offsets {
		out = append(out, num)
	}
	for num := range d.compressed {
		out = append(out, num)
	}
	sort.Ints(out)
	return out
}

// ---- 对象解析与解析引用 ----

// parseObjectAt 从某个偏移解析出一个对象（用于 xref 定位与扫描重建）。
//
// 偏移指向的是 "N G obj" 这一行的**开头**，而真正要解析的对象在 obj 之后。
// 两种位置都见过：xref 里存的是前者，扫描重建时我用的是后者。
// 因此这里先看有没有对象头，有就跳过它——让同一个函数同时吃下两种偏移。
func (d *Document) parseObjectAt(off int) (Object, error) {
	if off < 0 || off >= len(d.data) {
		return nil, errMalformed("对象偏移越界：%d", off)
	}
	lx := newLexer(d.data, off)
	skipObjHeader(lx)
	obj := lx.parseObject(d)
	if obj == nil {
		return nil, errMalformed("偏移 %d 处不是一个对象", off)
	}
	return obj, nil
}

// skipObjHeader 若当前位置是 "N G obj"，跳过它。
func skipObjHeader(lx *lexer) bool {
	start := lx.pos

	t := lx.next()
	if t.kind != tokNumber || !t.isInt {
		lx.pos = start
		return false
	}
	g := lx.next()
	if g.kind != tokNumber {
		lx.pos = start
		return false
	}
	k := lx.next()
	if k.kind != tokKeyword || k.keyword != "obj" {
		lx.pos = start
		return false
	}
	return true
}

// object 按对象号取对象（走索引，必要时展开对象流）。
func (d *Document) object(num int) (Object, error) {
	if off, ok := d.offsets[num]; ok {
		return d.parseObjectAt(int(off))
	}
	if loc, ok := d.compressed[num]; ok {
		return d.objectFromObjStm(loc[0], num, loc[1])
	}
	return nil, errMalformed("对象 %d 不存在", num)
}

// object 从对象流里取指定**对象号**的对象。
//
// 为什么按对象号查表而不是直接用交叉引用里的第三段：
// 规范说它是"在对象流中的索引"，可实际生产的文件里两种写法都有
// ——LaTeX 写的是序号，别的工具写的是相对 /First 的字节偏移。
// 直接采信任何一种都会在另一种上错位。改成在对象流的头表里
// 按对象号查，两种写法都得到同一个答案，也就都不必猜了。
func (d *Document) objectFromObjStm(stmNum, objNum, hint int) (Object, error) {
	index, first, err := d.objStmIndex(stmNum)
	if err != nil {
		return nil, err
	}
	data, err := d.objStmData(stmNum)
	if err != nil {
		return nil, err
	}

	at, ok := index[objNum]
	if !ok {
		// 头表里没有这个对象号：只能退回到提示值。
		at = hint
	}
	// 越界检查要拿数据段的长度，不是 /First：偏移是**相对 /First** 的，
	// 拿 /First 本身当上界会把绝大多数正常对象误判成越界。
	if at < 0 || first+int64(at) >= int64(len(data)) {
		return nil, errMalformed("对象流 %d 里对象 %d 的偏移越界", stmNum, objNum)
	}
	return newLexer(data, int(first)+at).parseObject(d), nil
}

// objStmIndex 解析并缓存一个对象流的头表：对象号 → 相对 /First 的字节偏移。
func (d *Document) objStmIndex(stmNum int) (map[int]int, int64, error) {
	if idx, ok := d.objStmTables[stmNum]; ok {
		return idx, int64(idx[firstKey]), nil
	}
	s := d.parsedObj(Ref{Num: stmNum})
	st, ok := s.(*Stream)
	if !ok {
		return nil, 0, errMalformed("对象流 %d 不可读", stmNum)
	}
	data, err := d.objStmData(stmNum)
	if err != nil {
		return nil, 0, err
	}
	n, ok1 := d.intOf(st.Dict, "N")
	first, ok2 := d.intOf(st.Dict, "First")
	if !ok1 || !ok2 || n < 0 || n > 1<<20 || first < 0 || int(first) > len(data) {
		return nil, 0, errMalformed("对象流 %d 的 /N 或 /First 不合法", stmNum)
	}

	lx := newLexer(data, 0)
	idx := make(map[int]int, n)
	for i := int64(0); i < n; i++ {
		objNum, ok1 := toInt(lx.parseObject(nil))
		off, ok2 := toInt(lx.parseObject(nil))
		if !ok1 || !ok2 {
			break
		}
		if _, dup := idx[int(objNum)]; !dup {
			idx[int(objNum)] = int(off)
		}
	}
	// 把 /First 挂在表里，省得每个调用点再解一次字典。
	idx[firstKey] = int(first)

	if d.objStmTables == nil {
		d.objStmTables = map[int]map[int]int{}
	}
	d.objStmTables[stmNum] = idx
	return idx, first, nil
}

// firstKey 是头表里借用的一项，用来携带 /First。
//
// 借键而不是再加一个结构体字段：头表本身是 map[int]int，
// 用一个绝不可能与真实对象号冲突的负键存 /First，既不改变它的语义，
// 也不必让所有调用点多带一个返回值。
const firstKey = -1

// objStmData 取并缓存一个对象流的解压内容。
func (d *Document) objStmData(stmNum int) ([]byte, error) {
	if data, ok := d.objStmDecoded[stmNum]; ok {
		return data, nil
	}
	s := d.parsedObj(Ref{Num: stmNum})
	st, ok := s.(*Stream)
	if !ok {
		return nil, errMalformed("对象流 %d 不可读", stmNum)
	}
	data, err := st.Bytes()
	if err != nil {
		return nil, err
	}
	d.objStmDecoded[stmNum] = data
	return data, nil
}

// parsedObj 解析并缓存指定引用的对象。
func (d *Document) parsedObj(ref Ref) Object {
	if v, ok := d.parsed[ref]; ok {
		return v
	}
	// 先占位再填充：对象之间可能互相引用，占位可以挡住无限递归。
	d.parsed[ref] = nil
	var obj Object
	if off, ok := d.offsets[ref.Num]; ok {
		obj, _ = d.parseObjectAt(int(off))
	} else if loc, ok := d.compressed[ref.Num]; ok {
		obj, _ = d.objectFromObjStm(loc[0], ref.Num, loc[1])
	}
	d.parsed[ref] = obj
	return obj
}

// resolve 解引用：Ref 变成它指向的对象，其它值原样返回。
func (d *Document) resolve(o Object) Object {
	for i := 0; i < 32; i++ { // 深度上限：坏文件里的 Ref 环
		ref, ok := o.(Ref)
		if !ok {
			return o
		}
		o = d.parsedObj(ref)
	}
	return nil
}

func (d *Document) dict(ref Ref) (Dict, error) {
	v, ok := d.resolve(ref).(Dict)
	if !ok {
		return nil, errMalformed("对象 %d 不是字典", ref.Num)
	}
	return v, nil
}

// ---- 页树 ----

// page 是解析好的一页。
type page struct {
	// resources 字体等资源，已按页树继承补全。
	resources Dict
	// contents 拼好的内容流。
	content []byte
	// mediaBox 页面尺寸，用来判断旋转。
	mediaBox [4]float64
	rotate   int
}

// pages 按顺序返回全部页面。
func (d *Document) pages() ([]*page, error) {
	root, ok := d.dictOf(d.catalog, "Pages")
	if !ok {
		return nil, errMalformed("文档目录里没有页树")
	}
	var (
		out []*page
		// 继承来的属性：Pages 节点上的设置对整棵子树生效，
		// 真实的 PDF 经常只在根节点上放一次 /Resources。
		res      Dict
		box      = [4]float64{0, 0, 612, 792}
		rotate   int
		visiting = map[int]bool{}
	)
	var walk func(node Dict, depth int)
	walk = func(node Dict, depth int) {
		if node == nil || depth > 64 || len(out) > maxPages {
			return
		}
		if r, ok := d.dictOf(node, "Resources"); ok {
			res = r
		}
		if b, ok := d.arrayOf(node, "MediaBox"); ok && len(b) == 4 {
			v := floatSlice(b)
			box = [4]float64{v[0], v[1], v[2], v[3]}
		}
		if r, ok := d.intOf(node, "Rotate"); ok {
			rotate = int(r) % 360
			if rotate < 0 {
				rotate += 360
			}
		}

		if tp, _ := d.nameOf(node, "Type"); tp == "Page" {
			out = append(out, &page{
				resources: res,
				content:   d.pageContent(node),
				mediaBox:  box,
				rotate:    rotate,
			})
			return
		}

		kids, ok := d.arrayOf(node, "Kids")
		if !ok {
			return
		}
		for _, kid := range kids {
			ref, isRef := kid.(Ref)
			if isRef {
				if visiting[ref.Num] {
					continue // 页树成环：跳过而不是栈溢出
				}
				visiting[ref.Num] = true
			}
			child, ok := d.resolve(kid).(Dict)
			if ok {
				walk(child, depth+1)
			}
			if isRef {
				delete(visiting, ref.Num)
			}
		}
	}
	walk(root, 0)
	if len(out) == 0 {
		return nil, errUnsupported("这份 PDF 里没有可读的页面")
	}
	return out, nil
}

// maxPages 页数上限。一份"卷子"超过这个数就不是卷子了。
const maxPages = 2000

// pageContent 拼出一页的内容流。
//
// /Contents 可以是单个流，也可以是数组（同一页被分多次写入）。
// 数组之间必须补一个换行，否则前一个流末尾没写完的 token
// 会和下一个流的开头粘成一个词。
func (d *Document) pageContent(node Dict) []byte {
	switch v := d.get(node, "Contents").(type) {
	case *Stream:
		data, err := v.Bytes()
		if err != nil {
			return nil
		}
		return data
	case Array:
		var out []byte
		for _, item := range v {
			s, ok := d.resolve(item).(*Stream)
			if !ok {
				continue
			}
			data, err := s.Bytes()
			if err != nil {
				continue
			}
			if len(out) > 0 {
				out = append(out, '\n')
			}
			out = append(out, data...)
		}
		return out
	}
	return nil
}

// PageCount 返回页数。
func (d *Document) PageCount() int {
	ps, err := d.pages()
	if err != nil {
		return 0
	}
	return len(ps)
}

// sortedOffsets 便于测试与调试：按对象号排序的全部偏移。
func (d *Document) sortedOffsets() []int {
	out := make([]int, 0, len(d.offsets))
	for num := range d.offsets {
		out = append(out, num)
	}
	sort.Ints(out)
	return out
}
