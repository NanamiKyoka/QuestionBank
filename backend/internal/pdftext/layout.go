package pdftext

import (
	"math"
	"sort"
	"strings"
	"unicode"
)

// 排版还原：把带坐标的文字片段拼成"从上往下、从左往右"的行。
//
// 为什么不能直接按坐标排序输出：一份 PDF 里每一行往往被拆成
// 十几个片段（字体切换、上下标、公式微调），而相邻两行的 y 只差
// 十几点。要还原成可读文本，只有"先归行、再按行内 x 排序"这一条路。

// 空格补齐的阈值：间距超过字号的这个比例，就认为原来那里有个空格。
//
// 取 0.2 而不是 0：数学公式里大量符号紧挨着（如 "a²+b²=c²"），
// 补得太宽会把一个整体拆成 "a ² + b ² = c ²"；
// 取得太窄又会把英文单词粘起来。这是个观感上的折中。
const spaceGapRatio = 0.2

// 归行的容差：基线相差不到字号的这个比例，算同一行。
const lineToleranceRatio = 0.35

// pageText 把一页的文字片段还原成文本。
func pageText(runs []textRun) string {
	if len(runs) == 0 {
		return ""
	}
	lines := groupIntoLines(runs)
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if s := renderLine(line); s != "" {
			out = append(out, s)
		}
	}
	return strings.Join(out, "\n")
}

// line 是一行：若干片段按 x 排好序。
type line struct {
	items []textRun
	// y 这一行的基线（取片段里的中位数，抗离群值）。
	y float64
	// size 这一行的代表字号。
	size float64
	// left 最左边的 x。
	left float64
}

// groupIntoLines 按基线把片段归行。
func groupIntoLines(runs []textRun) []*line {
	// 先按 y 降序（PDF 里 y 向上，所以降序才是从上往下），
	// 再按 x 升序。同一条线内的顺序在 renderLine 里还会再排一次。
	sorted := make([]textRun, len(runs))
	copy(sorted, runs)
	sort.SliceStable(sorted, func(a, b int) bool {
		if math.Abs(sorted[a].y-sorted[b].y) > 0.01 {
			return sorted[a].y > sorted[b].y
		}
		return sorted[a].x < sorted[b].x
	})

	var lines []*line
	for _, r := range sorted {
		if r.h <= 0 {
			r.h = 10 // 缺失字号时的兜底，保证行距不会塌成 0
		}
		placed := false
		for _, ln := range lines {
			tol := math.Max(ln.size, r.h) * lineToleranceRatio
			if math.Abs(ln.y-r.y) <= tol {
				ln.items = append(ln.items, r)
				ln.y = (ln.y*float64(len(ln.items)-1) + r.y) / float64(len(ln.items))
				ln.size = math.Max(ln.size, r.h)
				placed = true
				break
			}
		}
		if !placed {
			lines = append(lines, &line{items: []textRun{r}, y: r.y, size: r.h, left: r.x})
		}
	}
	for _, ln := range lines {
		sort.SliceStable(ln.items, func(a, b int) bool { return ln.items[a].x < ln.items[b].x })
		ln.left = ln.items[0].x
	}
	return lines
}

// renderLine 把一行拼成字符串。
//
// 片段之间要不要补空格，看的是"上一个片段的右边界到下一个片段的左边界"
// 差了多少，而不是它们的内容——同一条 PDF 文本流里，
// "A. 1" 和 "A" " " "1" 是完全等价的两种写法。
func renderLine(ln *line) string {
	var b strings.Builder
	prevEnd := 0.0
	havePrev := false
	trailingSpace := false // 上一次是否刚补过一个空格

	for i := range ln.items {
		r := ln.items[i]
		// 纯空白的片段只占位，不产出内容。但它推进了位置，
		// 因此仍要更新 prevEnd，否则下一段会被误判成"紧挨着"。
		if strings.TrimSpace(r.text) == "" {
			prevEnd = r.x + r.w
			havePrev = true
			continue
		}

		if havePrev {
			gap := r.x - prevEnd
			// 明显拉开 = 原来那里有个空格；明显叠回来 = TJ 数组里塞了空格。
			if (gap > ln.size*spaceGapRatio || gap < -ln.size*0.5) &&
				b.Len() > 0 && !trailingSpace {
				b.WriteByte(' ')
				trailingSpace = true
			}
		}

		text := collapseSpaces(r.text)
		b.WriteString(text)
		trailingSpace = strings.HasSuffix(text, " ")
		prevEnd = r.x + r.w
		havePrev = true
	}
	return strings.TrimRight(b.String(), " ")
}

// collapseSpaces 把连续空白压成一个空格。
func collapseSpaces(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	space := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	if space && b.Len() > 0 {
		b.WriteByte(' ')
	}
	return b.String()
}
