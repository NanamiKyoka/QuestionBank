package importing

import (
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
)

// Word 自动编号（列表编号）的还原。
//
// 真实卷子的题号绝大多数是**自动编号**：段落上只有一个 w:numPr，
// 序号由 numbering.xml 描述的模板在 Word 里现算，文档里根本不存在。
// 不还原它，段落就只剩纯文本，template.go 里按题号切题的整条路径失效，
// 整份卷子会被当成一道大题。

// ---- XML 形状 -----------------------------------------------------------
//
// numbering.xml 与 styles.xml 的层级是固定的，用结构体解码即可；
// document.xml 之所以逐 token 扫，是因为段落可以任意嵌套。

type numberingXML struct {
	Abstracts []abstractNumXML `xml:"abstractNum"`
	Nums      []numXML         `xml:"num"`
}

type abstractNumXML struct {
	ID     string     `xml:"abstractNumId,attr"`
	Levels []levelXML `xml:"lvl"`
}

type levelXML struct {
	Ilvl   string  `xml:"ilvl,attr"`
	Start  *valXML `xml:"start"`
	Format *valXML `xml:"numFmt"`
	Text   *valXML `xml:"lvlText"`
	Suffix *valXML `xml:"suff"`
	Legal  *valXML `xml:"isLgl"`
}

type numXML struct {
	NumID     string        `xml:"numId,attr"`
	Abstract  *valXML       `xml:"abstractNumId"`
	Overrides []overrideXML `xml:"lvlOverride"`
}

type overrideXML struct {
	Ilvl  string    `xml:"ilvl,attr"`
	Start *valXML   `xml:"startOverride"`
	Level *levelXML `xml:"lvl"`
}

type stylesXML struct {
	Styles []styleXML `xml:"style"`
}

type styleXML struct {
	Type string  `xml:"type,attr"`
	ID   string  `xml:"styleId,attr"`
	PPr  *pPrXML `xml:"pPr"`
}

type pPrXML struct {
	NumPr *numPrXML `xml:"numPr"`
}

type numPrXML struct {
	Ilvl  *valXML `xml:"ilvl"`
	NumID *valXML `xml:"numId"`
}

type valXML struct {
	Val string `xml:"val,attr"`
}

func (v *valXML) str() string {
	if v == nil {
		return ""
	}
	return v.Val
}

// ---- 编号定义 -----------------------------------------------------------

// levelDef 一级的编号规则。
type levelDef struct {
	start  int
	format string
	text   string
	suffix string
	legal  bool
}

// numDef 一个 numId：指向哪个抽象编号，以及它对某些级的覆盖。
type numDef struct {
	abstract string
	startOf  map[string]int      // startOverride
	levelOf  map[string]levelDef // 整级替换
}

// numberingPart 解析出来的编号定义。
type numberingPart struct {
	abstracts map[string]map[string]levelDef
	nums      map[string]numDef
	// styles 段落样式带来的默认编号：Word 里最常见的
	// "ListParagraph" 就是这样间接挂上一个 numId 的。
	styles map[string]numPrRef
}

type numPrRef struct {
	numID string
	ilvl  int
	hasIL int
}

// parseNumbering 读 numbering.xml 与 styles.xml。
//
// 任何一个缺失或解析失败都只返回空定义：编号还原是锦上添花，
// 不该因为一个格式怪异的附件就让整份卷子导入失败。
func parseNumbering(numberingData, stylesData []byte) *numberingPart {
	p := &numberingPart{
		abstracts: map[string]map[string]levelDef{},
		nums:      map[string]numDef{},
		styles:    map[string]numPrRef{},
	}
	if len(numberingData) > 0 {
		var doc numberingXML
		if err := xml.Unmarshal(numberingData, &doc); err == nil {
			for _, ab := range doc.Abstracts {
				levels := make(map[string]levelDef, len(ab.Levels))
				for _, lv := range ab.Levels {
					levels[lv.Ilvl] = levelDefOf(lv)
				}
				p.abstracts[ab.ID] = levels
			}
			for _, n := range doc.Nums {
				def := numDef{
					abstract: n.Abstract.str(),
					startOf:  map[string]int{},
					levelOf:  map[string]levelDef{},
				}
				for _, ov := range n.Overrides {
					if s := ov.Start.str(); s != "" {
						def.startOf[ov.Ilvl], _ = strconv.Atoi(s)
					}
					if ov.Level != nil {
						def.levelOf[ov.Ilvl] = levelDefOf(*ov.Level)
					}
				}
				p.nums[n.NumID] = def
			}
		}
	}
	if len(stylesData) > 0 {
		var doc stylesXML
		if err := xml.Unmarshal(stylesData, &doc); err == nil {
			for _, st := range doc.Styles {
				if st.PPr == nil || st.PPr.NumPr == nil {
					continue
				}
				ref := numPrRef{ilvl: 0}
				if s := st.PPr.NumPr.NumID.str(); s != "" {
					ref.numID = s
				}
				if s := st.PPr.NumPr.Ilvl.str(); s != "" {
					ref.ilvl, _ = strconv.Atoi(s)
					ref.hasIL = 1
				}
				if ref.numID != "" {
					p.styles[st.ID] = ref
				}
			}
		}
	}
	return p
}

func levelDefOf(lv levelXML) levelDef {
	d := levelDef{
		start:  1,
		format: "decimal",
		text:   lv.Text.str(),
		suffix: lv.Suffix.str(),
		legal:  lv.Legal != nil && lv.Legal.Val == "true",
	}
	if s := lv.Start.str(); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			d.start = n
		}
	}
	if s := lv.Format.str(); s != "" {
		d.format = s
	}
	return d
}

// level 取出 numId 在 ilvl 这一级的规则。
func (p *numberingPart) level(numID string, ilvl int) (levelDef, bool) {
	key := strconv.Itoa(ilvl)
	def, ok := p.nums[numID]
	if !ok {
		return levelDef{}, false
	}
	if lv, ok := def.levelOf[key]; ok {
		if s, ok := def.startOf[key]; ok {
			lv.start = s
		}
		return lv, true
	}
	lv, ok := p.abstracts[def.abstract][key]
	if !ok {
		return levelDef{}, false
	}
	if s, ok := def.startOf[key]; ok {
		lv.start = s
	}
	return lv, true
}

// ---- 运行时计数 ---------------------------------------------------------

// numberingState 按段落推进计数器。同一份文档里每个 numId 各有一组。
type numberingState struct {
	part    *numberingPart
	counter map[string]map[int]int // numId → ilvl → 已计到的值
}

func newNumberingState(part *numberingPart) *numberingState {
	if part == nil {
		return nil
	}
	return &numberingState{part: part, counter: map[string]map[int]int{}}
}

// prefix 算出一个段落该带的前缀文本；没有编号时返回空串。
func (s *numberingState) prefix(numID string, ilvl int) string {
	if s == nil || numID == "" || numID == "0" {
		return ""
	}
	lvl, ok := s.part.level(numID, ilvl)
	if !ok || lvl.format == "none" {
		return ""
	}

	cur := s.counter[numID]
	if cur == nil {
		cur = map[int]int{}
		s.counter[numID] = cur
	}
	if _, seen := cur[ilvl]; !seen {
		cur[ilvl] = lvl.start - 1
	}
	cur[ilvl]++
	// 进到第 n 级说明上一轮已经结束，比 n 深的各级都要归零。
	for deeper := range cur {
		if deeper > ilvl {
			delete(cur, deeper)
		}
	}

	text := s.render(numID, lvl, cur)
	if text == "" {
		return ""
	}
	if lvl.suffix == "nothing" {
		return text
	}
	return text + " "
}

// render 把 lvlText 里的 %1、%2 换成对应级别的序号。
func (s *numberingState) render(numID string, lvl levelDef, cur map[int]int) string {
	// 项目符号的 lvlText 本身就是那个符号，没有 %n 可替换。
	if lvl.format == "bullet" {
		return lvl.text
	}
	var b strings.Builder
	runes := []rune(lvl.text)
	for i := 0; i < len(runes); i++ {
		if runes[i] != '%' || i+1 >= len(runes) {
			b.WriteRune(runes[i])
			continue
		}
		j := i + 1
		for j < len(runes) && runes[j] >= '0' && runes[j] <= '9' {
			j++
		}
		if j == i+1 {
			b.WriteRune(runes[i])
			continue
		}
		ref, _ := strconv.Atoi(string(runes[i+1 : j]))
		b.WriteString(s.formatAt(numID, ref-1, cur, lvl.legal))
		i = j - 1
	}
	return b.String()
}

// formatAt 取出某一级当前的序号，并按该级的 numFmt 格式化。
func (s *numberingState) formatAt(numID string, ilvl int, cur map[int]int, legal bool) string {
	n, seen := cur[ilvl]
	format := "decimal"
	if def, ok := s.part.level(numID, ilvl); ok {
		format = def.format
		if !seen && def.start > 0 {
			n = def.start
		}
	}
	if legal {
		// isLgl 的语义是"本级及以上一律按十进制显示"，用来对付
		// "第 1 章"这类模板挂在罗马数字章节上的卷子。
		format = "decimal"
	}
	return formatOrdinal(n, format)
}

// formatOrdinal 把序号按 Word 的 numFmt 写出来。
//
// 只覆盖卷子里真会出现的那几种，其余一律退回十进制：
// 猜错格式顶多前缀难看，猜对则整份卷子的题号全部复原。
func formatOrdinal(n int, format string) string {
	switch format {
	case "decimal", "":
		return strconv.Itoa(n)
	case "decimalZero":
		return fmt.Sprintf("%02d", n)
	case "lowerLetter":
		return letterOrdinal(n)
	case "upperLetter":
		return strings.ToUpper(letterOrdinal(n))
	case "lowerRoman":
		return strings.ToLower(romanOrdinal(n))
	case "upperRoman":
		return romanOrdinal(n)
	case "chineseCounting", "chineseCountingThousand", "ideographDigital",
		"japaneseCounting", "taiwaneseCounting", "ideographZodiac":
		return chineseNumeral(n)
	case "ideographTraditional", "japaneseDigitalTenThousand":
		return heavenlyStem(n)
	default:
		return strconv.Itoa(n)
	}
}

// letterOrdinal 对应 ECMA-376 的 lowerLetter：a…z, aa, bb, cc。
// 它不是 Excel 那种 a…z, aa, ab——序数 27 给的是 "aa" 而不是 "ab"。
func letterOrdinal(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat(string(rune('a'+(n-1)%26)), (n-1)/26+1)
}

var romanValues = []struct {
	v int
	s string
}{
	{1000, "M"}, {900, "CM"}, {500, "D"}, {400, "CD"},
	{100, "C"}, {90, "XC"}, {50, "L"}, {40, "XL"},
	{10, "X"}, {9, "IX"}, {5, "V"}, {4, "IV"}, {1, "I"},
}

func romanOrdinal(n int) string {
	if n <= 0 || n >= 4000 {
		return strconv.Itoa(n)
	}
	var b strings.Builder
	for _, rv := range romanValues {
		for n >= rv.v {
			b.WriteString(rv.s)
			n -= rv.v
		}
	}
	return b.String()
}

// chineseNumeral 写中文数字（1–9999）。中文卷子的"一、选择题"
// 几乎都来自这一档 numFmt，缺了它整份卷子的题号会全部退回十进制。
func chineseNumeral(n int) string {
	if n <= 0 || n >= 10000 {
		return strconv.Itoa(n)
	}
	digits := []rune("零一二三四五六七八九")
	units := []string{"", "十", "百", "千"}
	s := strconv.Itoa(n)
	var b strings.Builder
	pendingZero := false
	for i := 0; i < len(s); i++ {
		d := int(s[i] - '0')
		if d == 0 {
			pendingZero = true
			continue
		}
		if pendingZero && b.Len() > 0 {
			b.WriteRune('零')
		}
		pendingZero = false
		b.WriteRune(digits[d])
		b.WriteString(units[len(s)-1-i])
	}
	return b.String()
}

// heavenlyStems 天干：甲乙丙丁戊己庚辛壬癸。
var heavenlyStems = []string{"甲", "乙", "丙", "丁", "戊", "己", "庚", "辛", "壬", "癸"}

func heavenlyStem(n int) string {
	if n <= 0 || n > len(heavenlyStems) {
		return strconv.Itoa(n)
	}
	return heavenlyStems[n-1]
}
