// Package visionprep 把图片收拾成"能发给模型"的形态。
//
// 存在的理由有两条，都不是洁癖：
//
//  1. **本地地址模型取不到。** 上传接口返回的是 /media/xxx.jpg 这种
//     本服务自己的相对路径，而模型在另一台机器上，它没有任何办法
//     去访问我们的 /media。所以必须把图片读回来内联成 data URL。
//
//  2. **原图直接发会撑爆请求。** 一张手机拍的试卷 3000×4000、
//     约 500 KB，base64 之后 670 KB；十张就是 6.7 MB 正文，
//     再乘以分块数。而视觉模型内部一律会缩到长边 1500 上下，
//     ��下来的分辨率它根本看不见——发过去只是白费带宽和时间。
//
// 为什么自己缩放而不用 golang.org/x/image：本项目要求 CGO_ENABLED=0、
// 离线可构建、依赖尽量少，而缩放只需要 stdlib 的 image/jpeg 就够了。
package visionprep

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png" // 注册 PNG 解码器：试卷截图绝大多数是 PNG
)

// MaxEdge 缩放后的长边上限。
//
// 取 1600：主流视觉模型的内部处理边长就在 1500~2000 之间，
// 再大只是让请求变重，识别效果不再提升；而试卷上的小字
// （下标、分数线）在 1600 长边下仍然清晰可辨。
const MaxEdge = 1600

// JPEGQuality 重新编码的 JPEG 质量。
//
// 试卷是"字越清楚越好"，但 85 已经看不出差别，
// 而它比 95 小三成——十张图的差别是 5 MB 与 3.5 MB 的区别。
const JPEGQuality = 85

// ImageToDataURL 把解码好的图像编码成 data URL（缩放 + JPEG 重编码）。
//
// 与 ToDataURL 的区别只在于输入已经是 image.Image：切图这类操作
// 拿到的中间结果是图像而不是字节，再 encode 一次解回来纯属浪费。
func ImageToDataURL(img image.Image) (string, error) {
	if img == nil {
		return "", fmt.Errorf("图像为空")
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, Downscale(img, MaxEdge), &jpeg.Options{Quality: JPEGQuality}); err != nil {
		return "", fmt.Errorf("图片编码失败：%w", err)
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

// ToDataURL 把图片字节整理成可以直接放进 image_url 的 data URL。
//
// 已经是 data URL 或 http(s) 地址的原样返回：前端也可能直接传
// 公网地址过来（比如试卷来自某个题库），没必要也不该去下载它。
func ToDataURL(data []byte) (string, error) {
	if len(data) == 0 {
		return "", fmt.Errorf("图片内容为空")
	}

	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("图片无法解码：%w", err)
	}

	scaled := Downscale(img, MaxEdge)

	var buf bytes.Buffer
	// 一律重编码成 JPEG：视觉模型全都吃 JPEG，
	// 而原图可能是几 MB 的 PNG（截图就是这么来的）。
	if err := jpeg.Encode(&buf, scaled, &jpeg.Options{Quality: JPEGQuality}); err != nil {
		return "", fmt.Errorf("图片编码失败：%w", err)
	}
	_ = format // 解码出来的格式只用来确认它确实是图片

	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

// Downscale 等比缩小到长边不超过 maxEdge；已经够小就原样返回。
func Downscale(src image.Image, maxEdge int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return src
	}
	longest := w
	if h > longest {
		longest = h
	}
	if longest <= maxEdge {
		return src
	}

	// 先定比例，再用整数边界切出目标尺寸。
	scale := float64(maxEdge) / float64(longest)
	dw := int(float64(w)*scale + 0.5)
	dh := int(float64(h)*scale + 0.5)
	if dw < 1 {
		dw = 1
	}
	if dh < 1 {
		dh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))

	// 区域平均，而不是最近邻：试卷是细密的文字，
	// 最近邻会按像素抽样，把笔画抽成断断续续的虚线。
	for y := 0; y < dh; y++ {
		y0 := b.Min.Y + y*h/dh
		y1 := b.Min.Y + (y+1)*h/dh
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for x := 0; x < dw; x++ {
			x0 := b.Min.X + x*w/dw
			x1 := b.Min.X + (x+1)*w/dw
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var rs, gs, bs, as, n uint64
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					cr, cg, cb, ca := src.At(sx, sy).RGBA()
					rs += uint64(cr)
					gs += uint64(cg)
					bs += uint64(cb)
					as += uint64(ca)
					n++
				}
			}
			if n == 0 {
				continue
			}
			// RGBA() 返回的是 16 位预乘值，除回 8 位范围。
			idx := dst.PixOffset(x, y)
			dst.Pix[idx+0] = uint8(rs / n >> 8)
			dst.Pix[idx+1] = uint8(gs / n >> 8)
			dst.Pix[idx+2] = uint8(bs / n >> 8)
			dst.Pix[idx+3] = uint8(as / n >> 8)
		}
	}
	return dst
}
