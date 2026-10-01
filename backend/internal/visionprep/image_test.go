package visionprep

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
)

func makePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	// 画点非纯色的内容，否则重编码后的体积对比没有意义。
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("编码失败: %v", err)
	}
	return buf.Bytes()
}

// 模型必须拿到 data URL，而不是本服务的 /media 路径。
// 这是整条图片识题链路的生死线：远程模型没有任何办法去访问我们的 /media。
func TestToDataURLInlinesTheImage(t *testing.T) {
	url, err := ToDataURL(makePNG(t, 64, 48))
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}
	if !strings.HasPrefix(url, "data:image/jpeg;base64,") {
		t.Fatalf("应当是 JPEG 的 data URL，实际开头 %q", truncate(url, 40))
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(url, "data:image/jpeg;base64,"))
	if err != nil {
		t.Fatalf("base64 非法: %v", err)
	}
	if _, err := jpeg.Decode(bytes.NewReader(raw)); err != nil {
		t.Fatalf("产出的不是合法 JPEG: %v", err)
	}
}

// 原图直接发会撑爆请求：一张 3000×4000 的手机截图有 5 MB，
// 而模型内部一律会缩到长边 1500 上下，多余的分辨率它根本看不见。
func TestDownscalesLargeImages(t *testing.T) {
	src := makePNG(t, 3173, 4491)
	url, err := ToDataURL(src)
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(url, "data:image/jpeg;base64,"))
	if err != nil {
		t.Fatalf("base64 非法: %v", err)
	}
	if len(raw) >= len(src) {
		t.Fatalf("缩放后应当更小：原图 %d 字节，结果 %d 字节", len(src), len(raw))
	}

	img, err := jpeg.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	b := img.Bounds()
	longest := b.Dx()
	if b.Dy() > longest {
		longest = b.Dy()
	}
	if longest > MaxEdge {
		t.Fatalf("长边应为 %d 以内，实际 %d", MaxEdge, longest)
	}
	// 等比：宽高比必须保持。
	wantRatio := 3173.0 / 4491.0
	gotRatio := float64(b.Dx()) / float64(b.Dy())
	if diff := gotRatio - wantRatio; diff > 0.01 || diff < -0.01 {
		t.Fatalf("宽高比应保持：期望 %.4f，实际 %.4f", wantRatio, gotRatio)
	}
}

// 区域平均而不是最近邻：试卷是细密的文字，最近邻会把笔画抽成虚线。
//
// 这里用"平均亮度是否保持"来判：一条细黑线缩放后会变成一片中灰，
// 平均值应当不变。而最近邻要么整条丢���（均值变白），要么整列变黑
// （均值骤降）——两种都会让这个断言失败。
func TestDownscalePreservesMeanBrightness(t *testing.T) {
	const w, h = 400, 100
	white := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	black := color.RGBA{R: 0, G: 0, B: 0, A: 255}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := white
			if x >= w/2-2 && x < w/2+2 {
				c = black // 占 1% 宽度
			}
			img.Set(x, y, c)
		}
	}
	want := meanBrightness(img)

	got := meanBrightness(Downscale(img, 40))
	// 1% 的黑从白里平均出来，均值应当落在 1% × 255 以内。
	if diff := want - got; diff > 4 || diff < -4 {
		t.Fatalf("平均亮度应基本保持：期望 %.1f，实际 %.1f", want, got)
	}
	if got > 254 {
		t.Fatalf("黑线在缩放后整个消失了，实际均值 %.1f（说明用的是最近邻抽样）", got)
	}
}

// meanBrightness 取 0..255 的平均亮度。
func meanBrightness(img image.Image) float64 {
	b := img.Bounds()
	var sum float64
	n := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bb, _ := img.At(x, y).RGBA()
			// RGBA() 返回 16 位预乘值，>>8 回到 8 位。
			sum += float64(uint32((r+g+bb)/3) >> 8)
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

func TestDownscaleLeavesSmallImagesAlone(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 100, 50))
	if got := Downscale(src, MaxEdge); got != image.Image(src) {
		t.Fatal("已经够小的图片不该被重建")
	}
}

func TestToDataURLRejectsGarbage(t *testing.T) {
	if _, err := ToDataURL([]byte("not an image")); err == nil {
		t.Fatal("非图片必须被拒绝，而不是原样塞给模型")
	}
	if _, err := ToDataURL(nil); err == nil {
		t.Fatal("空内容必须被拒绝")
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
