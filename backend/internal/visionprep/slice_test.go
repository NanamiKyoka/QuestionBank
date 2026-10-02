package visionprep

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"strings"
	"testing"
)

// 造一张"上文字 + 中间大片空白 + 下文字"的竖图，
// 用来验证切点落在空白上而不是把文字劈成两半。
func makeSheet(t *testing.T, w, h, bandH int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	// 必须先铺白底：image.NewRGBA 出来是全 0，也就是全黑，
	// 而切点靠"墨迹最少的一行"来定位——全黑图每一行墨迹都一样多，
	// 测试就会退化成"随便切一刀"，而它仍然看起来是绿的。
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: 250, G: 250, B: 250, A: 255})
		}
	}
	paint := func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			for x := 0; x < w; x++ {
				img.Set(x, y, color.RGBA{R: 10, G: 10, B: 10, A: 255})
			}
		}
	}
	paint(10, 10+bandH)
	paint(h/2-bandH/2, h/2+bandH/2)
	paint(h-10-bandH, h-10)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatalf("编码失败: %v", err)
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestSplitVerticalProducesBands(t *testing.T) {
	url := makeSheet(t, 400, 1200, 60)
	parts, err := SplitVertical(url, 2)
	if err != nil {
		t.Fatalf("切图失败: %v", err)
	}
	if len(parts) != 2 {
		t.Fatalf("应当切成 2 段，实际 %d", len(parts))
	}
	for i, p := range parts {
		if !strings.HasPrefix(p, "data:image/") {
			t.Fatalf("第 %d 段不是 data URL: %.40s", i, p)
		}
	}
}

// 切点应当落在中间那片空白上：上半段因此包含"上文字"而不含"中文字"。
//
// 这是整个设计的意义所在——对半砍也能切出两条，但会把一道题劈成两半，
// 模型看到半道题，出来的是废草稿。找空白切才不会。
func TestSplitVerticalCutsAtWhitespace(t *testing.T) {
	const w, h, bandH = 400, 1200, 60
	url := makeSheet(t, w, h, bandH)

	parts, err := SplitVertical(url, 2)
	if err != nil {
		t.Fatalf("切图失败: %v", err)
	}
	if len(parts) != 2 {
		t.Fatalf("应当切成 2 段，实际 %d", len(parts))
	}

	top := inkProfile(decode(t, parts[0]))

	// 上半段：应含顶部黑条，且不该含中间那条——
	// 中间那条在原图 y≈570~630，切点落在它上方的空白里时，
	// 它的行号在半段之外，profile 自然取不到。
	if !hasInk(top) {
		t.Fatal("上半段应当有内容（顶部文字条）")
	}
	cut := decode(t, parts[0]).Bounds().Dy()
	if cut > h/2-bandH/2 {
		t.Fatalf("切点 y=%d 落到了中间黑条之内，应当落在空白里", cut)
	}

	bottom := inkProfile(decode(t, parts[1]))
	if !hasInk(bottom) {
		t.Fatal("下半段应当有内容")
	}
}

// 切成 4 段也要保证每段非空且有序。
func TestSplitVerticalIntoFour(t *testing.T) {
	url := makeSheet(t, 400, 1200, 40)
	parts, err := SplitVertical(url, 4)
	if err != nil {
		t.Fatalf("切图失败: %v", err)
	}
	if len(parts) < 2 {
		t.Fatalf("切成 4 段后实际只有 %d 段", len(parts))
	}
	for i, p := range parts {
		img := decode(t, p)
		if img.Bounds().Dy() <= 0 {
			t.Fatalf("第 %d 段高度为 0", i)
		}
	}
}

// 高度不足以再切时原样返回：再切只会得到噪声。
func TestSplitVerticalRefusesTinyImages(t *testing.T) {
	url := makeSheet(t, 40, 12, 2)
	parts, err := SplitVertical(url, 4)
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if len(parts) != 1 || parts[0] != url {
		t.Fatalf("太小的图应当原样返回，实际 %d 段", len(parts))
	}
}

func TestSplitVerticalRejectsGarbage(t *testing.T) {
	if _, err := SplitVertical("not-a-data-url", 2); err == nil {
		t.Fatal("非 data URL 应当报错")
	}
	if _, err := SplitVertical("data:image/png;base64,!!!", 2); err == nil {
		t.Fatal("坏 base64 应当报错")
	}
}

func decode(t *testing.T, url string) image.Image {
	t.Helper()
	raw, err := decodeDataURL(url)
	if err != nil {
		t.Fatalf("解 base64 失败: %v", err)
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	return img
}

// hasInk 判断整段图里有没有墨迹。
func hasInk(profile []int) bool {
	for _, n := range profile {
		if n > 0 {
			return true
		}
	}
	return false
}
