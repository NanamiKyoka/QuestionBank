package docx

// stylesXML 定义文档默认字体、标题样式、列表样式与表格样式。
//
// 字体选择：西文用 Times New Roman、中文用宋体，字号 10.5 磅（小四）——
// 这是中文试卷最常见的排版，避免用户打开后还要手动调格式。
const stylesXML = xmlHeader + `<w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">` +
	`<w:docDefaults><w:rPrDefault><w:rPr>` +
	`<w:rFonts w:ascii="Times New Roman" w:hAnsi="Times New Roman" w:eastAsia="宋体" w:cs="Times New Roman"/>` +
	`<w:sz w:val="21"/><w:szCs w:val="21"/>` +
	`</w:rPr></w:rPrDefault>` +
	`<w:pPrDefault><w:pPr><w:spacing w:line="360" w:lineRule="auto"/></w:pPr></w:pPrDefault>` +
	`</w:docDefaults>` +

	`<w:style w:type="paragraph" w:default="1" w:styleId="Normal">` +
	`<w:name w:val="Normal"/><w:qFormat/></w:style>` +

	`<w:style w:type="paragraph" w:styleId="Heading1"><w:name w:val="heading 1"/><w:basedOn w:val="Normal"/>` +
	`<w:qFormat/><w:pPr><w:outlineLvl w:val="0"/><w:spacing w:before="240" w:after="120"/></w:pPr>` +
	`<w:rPr><w:b/><w:sz w:val="32"/><w:szCs w:val="32"/></w:rPr></w:style>` +

	`<w:style w:type="paragraph" w:styleId="Heading2"><w:name w:val="heading 2"/><w:basedOn w:val="Normal"/>` +
	`<w:qFormat/><w:pPr><w:outlineLvl w:val="1"/><w:spacing w:before="200" w:after="100"/></w:pPr>` +
	`<w:rPr><w:b/><w:sz w:val="28"/><w:szCs w:val="28"/></w:rPr></w:style>` +

	`<w:style w:type="paragraph" w:styleId="Heading3"><w:name w:val="heading 3"/><w:basedOn w:val="Normal"/>` +
	`<w:qFormat/><w:pPr><w:outlineLvl w:val="2"/><w:spacing w:before="160" w:after="80"/></w:pPr>` +
	`<w:rPr><w:b/><w:sz w:val="24"/><w:szCs w:val="24"/></w:rPr></w:style>` +

	`<w:style w:type="paragraph" w:styleId="ListParagraph"><w:name w:val="List Paragraph"/>` +
	`<w:basedOn w:val="Normal"/><w:qFormat/>` +
	`<w:pPr><w:ind w:left="420"/><w:contextualSpacing/></w:pPr></w:style>` +

	`<w:style w:type="table" w:styleId="TableGrid"><w:name w:val="Table Grid"/>` +
	`<w:tblPr><w:tblBorders>` +
	`<w:top w:val="single" w:sz="4" w:color="808080"/>` +
	`<w:left w:val="single" w:sz="4" w:color="808080"/>` +
	`<w:bottom w:val="single" w:sz="4" w:color="808080"/>` +
	`<w:right w:val="single" w:sz="4" w:color="808080"/>` +
	`<w:insideH w:val="single" w:sz="4" w:color="808080"/>` +
	`<w:insideV w:val="single" w:sz="4" w:color="808080"/>` +
	`</w:tblBorders></w:tblPr></w:style>` +

	`</w:styles>`

// numberingXML 定义两套编号：numId=1 十进制数字，numId=2 项目符号。
//
// 编号交给 Word 计算而不是在文本里写死"1."，这样在 Word 里增删题目时
// 编号会自动连续，也不会因为排版调整而错位。
//
// 用 var 而非 const：各层级由辅助函数拼接，不是编译期常量。
var numberingXML = xmlHeader + `<w:numbering xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">` +

	// 十进制，三级
	`<w:abstractNum w:abstractNumId="0">` +
	`<w:multiLevelType w:val="multilevel"/>` +
	level(0, "%1.", 720) +
	level(1, "%2)", 1440) +
	level(2, "%3.", 2160) +
	`</w:abstractNum>` +
	`<w:num w:numId="1"><w:abstractNumId w:val="0"/></w:num>` +

	// 项目符号，三级
	`<w:abstractNum w:abstractNumId="1">` +
	`<w:multiLevelType w:val="multilevel"/>` +
	bulletLevel(0, "•", 720) +
	bulletLevel(1, "○", 1440) +
	bulletLevel(2, "▪", 2160) +
	`</w:abstractNum>` +
	`<w:num w:numId="2"><w:abstractNumId w:val="1"/></w:num>` +

	`</w:numbering>`

func level(idx int, format string, indent int) string {
	return `<w:lvl w:ilvl="` + itoa(idx) + `">` +
		`<w:start w:val="1"/>` +
		`<w:numFmt w:val="decimal"/>` +
		`<w:lvlText w:val="` + format + `"/>` +
		`<w:lvlJc w:val="left"/>` +
		`<w:pPr><w:ind w:left="` + itoa(indent) + `" w:hanging="360"/></w:pPr>` +
		`</w:lvl>`
}

func bulletLevel(idx int, bullet string, indent int) string {
	return `<w:lvl w:ilvl="` + itoa(idx) + `">` +
		`<w:start w:val="1"/>` +
		`<w:numFmt w:val="bullet"/>` +
		`<w:lvlText w:val="` + bullet + `"/>` +
		`<w:lvlJc w:val="left"/>` +
		`<w:pPr><w:ind w:left="` + itoa(indent) + `" w:hanging="360"/></w:pPr>` +
		`<w:rPr><w:rFonts w:ascii="Symbol" w:hAnsi="Symbol" w:hint="default"/></w:rPr>` +
		`</w:lvl>`
}

// itoa 避免为一处格式化引入 strconv。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
