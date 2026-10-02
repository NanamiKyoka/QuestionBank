package visionprep

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"strings"
)

// SplitVertical 把一张图横向切成若干条，返回 data URL 列表。
//
// 用途：单页试卷题目密度太高时，一次调用的输出装不下
// （实测 2026 全国 1 卷第 1 页，11 道题带解析就要 3 万多 token），
// 而"一页拆成几段"是最贴近模型直觉的降法——每一段都是完整的一批题。
//
// 切点**找行间空白**而不是对半砍：试卷的题目上下都有空行，
// 沿空白切，两段各自都从完整题目开始；对半砍则可能把一道题劈成两半，
// 模型看到半道题，出来的草稿也是废的。
//
// 找不到足够好的空白时退回等分：宁可题目被切开，也不能因为找不到空行
// 就原样返回整张图——那等于什么都没做。
func SplitVertical(dataURL string, parts int) ([]string, error) {
	if parts <= 1 {
		return []string{dataURL}, nil
	}
	raw, err := decodeDataURL(dataURL)
	if err != nil {
		return nil, err
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("图片无法解码：%w", err)
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if h < parts*8 {
		// 每条不足 8 像素高，再切只会得到噪声。
		return []string{dataURL}, nil
	}

	profile := inkProfile(img)
	cuts := pickCuts(profile, h, parts)

	out := make([]string, 0, len(cuts)-1)
	for i := 0; i+1 < len(cuts); i++ {
		band := img.(interface {
			SubImage(image.Rectangle) image.Image
		}).SubImage(image.Rect(b.Min.X, cuts[i], b.Min.X+w, cuts[i+1]))
		url, err := ImageToDataURL(band)
		if err != nil {
			// 单条编不出来就跳过：这一段没拿到，好过整次重来。
			continue
		}
		out = append(out, url)
	}
	if len(out) == 0 {
		return []string{dataURL}, nil
	}
	return out, nil
}

// decodeDataURL 取出 data URL 里的原始字节。
func decodeDataURL(s string) ([]byte, error) {
	i := strings.Index(s, "base64,")
	if i < 0 {
		return nil, fmt.Errorf("不是 data URL")
	}
	return base64.StdEncoding.DecodeString(s[i+len("base64,"):])
}

// inkProfile 逐行统计非白像素数，用来定位行间空白。
func inkProfile(img image.Image) []int {
	b := img.Bounds()
	profile := make([]int, b.Dy())
	for y := 0; y < b.Dy(); y++ {
		n := 0
		for x := 0; x < b.Dx(); x++ {
			r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			// 阈值 240：试卷是白底黑字，浅灰网格线不算内容。
			if r>>8 < 240 || g>>8 < 240 || bl>>8 < 240 {
				n++
			}
		}
		profile[y] = n
	}
	return profile
}

// pickCuts 选出 parts-1 条切线，首尾固定为 0 与高度。
func pickCuts(profile []int, h, parts int) []int {
	cuts := make([]int, 0, parts+1)
	cuts = append(cuts, 0)
	for i := 1; i < parts; i++ {
		cuts = append(cuts, findBlankNear(profile, h*i/parts, h))
	}
	cuts = append(cuts, h)

	// 切线必须严格递增，否则会切出零高度甚至负高度的段。
	out := cuts[:1]
	for i := 1; i < len(cuts); i++ {
		if cuts[i] > out[len(out)-1] && cuts[i] < h {
			out = append(out, cuts[i])
		}
	}
	if out[len(out)-1] != h {
		out = append(out, h)
	}
	return out
}

// findBlankNear 在 target 附近找墨迹最少的一行作为切点。
//
// 搜索窗口取页高的 15%：太窄则可能找不到空白而被迫对半砍，
// 太宽则可能把切点挪得很远，让某一小段空得没有内容。
func findBlankNear(profile []int, target, h int) int {
	window := h * 15 / 100
	if window < 8 {
		window = 8
	}
	lo, hi := target-window, target+window
	if lo < 1 {
		lo = 1
	}
	if hi >= h {
		hi = h - 1
	}
	best := target
	if hi < lo {
		return best
	}
	for y := lo; y <= hi; y++ {
		if profile[y] < profile[best] {
			best = y
		}
	}
	return best
}
