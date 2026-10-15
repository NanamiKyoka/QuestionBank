package visionprep

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
)

// CropJPEG 按归一化矩形 [x,y,w,h] 裁剪 data URL 图片并返回 JPEG 字节。
func CropJPEG(dataURL string, box []float64) ([]byte, error) {
	if len(box) < 4 {
		return nil, fmt.Errorf("裁剪框需要四个数字")
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
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("图片尺寸无效")
	}
	x0 := b.Min.X + clampInt(int(box[0]*float64(w)), 0, w-1)
	y0 := b.Min.Y + clampInt(int(box[1]*float64(h)), 0, h-1)
	cw := clampInt(int(box[2]*float64(w)), 1, w-(x0-b.Min.X))
	ch := clampInt(int(box[3]*float64(h)), 1, h-(y0-b.Min.Y))
	if cw < 2 || ch < 2 {
		return nil, fmt.Errorf("裁剪区域过小")
	}
	out := image.NewRGBA(image.Rect(0, 0, cw, ch))
	draw.Draw(out, out.Bounds(), img, image.Pt(x0, y0), draw.Src)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, out, &jpeg.Options{Quality: 92}); err != nil {
		return nil, fmt.Errorf("裁剪图片编码失败：%w", err)
	}
	return buf.Bytes(), nil
}

func clampInt(v, lo, hi int) int {
	if hi < lo {
		return lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
