package visionprep

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

func TestCropJPEG(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 100, 80))
	for y := 0; y < 80; y++ {
		for x := 0; x < 100; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 80, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
	raw, err := CropJPEG(dataURL, []float64{0.2, 0.25, 0.5, 0.5})
	if err != nil {
		t.Fatal(err)
	}
	got, err := jpeg.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if got.Bounds().Dx() != 50 || got.Bounds().Dy() != 40 {
		t.Fatalf("裁剪尺寸错误: %v", got.Bounds())
	}
}
