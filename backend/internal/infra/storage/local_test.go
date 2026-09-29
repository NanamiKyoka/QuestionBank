package storage

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makePNG 生成一张真实的 PNG。
func makePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("生成 PNG 失败: %v", err)
	}
	return buf.Bytes()
}

func makeJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatalf("生成 JPEG 失败: %v", err)
	}
	return buf.Bytes()
}

func makeGIF(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewPaletted(image.Rect(0, 0, w, h), color.Palette{color.Black, color.White})
	var buf bytes.Buffer
	if err := gif.Encode(&buf, img, nil); err != nil {
		t.Fatalf("生成 GIF 失败: %v", err)
	}
	return buf.Bytes()
}

func TestInspectImageAcceptsSupportedFormats(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		ext  string
		mime string
		w, h int
	}{
		{"png", makePNG(t, 64, 48), "png", "image/png", 64, 48},
		{"jpeg", makeJPEG(t, 32, 16), "jpg", "image/jpeg", 32, 16},
		{"gif", makeGIF(t, 20, 10), "gif", "image/gif", 20, 10},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := InspectImage(tc.data)
			if err != nil {
				t.Fatalf("应接受 %s: %v", tc.name, err)
			}
			if got.Ext != tc.ext || got.Mime != tc.mime {
				t.Fatalf("格式识别错误: ext=%s mime=%s", got.Ext, got.Mime)
			}
			if got.Width != tc.w || got.Height != tc.h {
				t.Fatalf("尺寸错误: %dx%d，期望 %dx%d", got.Width, got.Height, tc.w, tc.h)
			}
		})
	}
}

// 扩展名必须由内容决定，不能采信客户端给的文件名。
func TestInspectImageRejectsNonImages(t *testing.T) {
	cases := map[string][]byte{
		"空文件":        {},
		"PHP 脚本":     []byte(`<?php system($_GET["c"]); ?>`),
		"纯文本":        []byte("hello world"),
		"SVG（可内嵌脚本）": []byte(`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`),
		"ZIP":        {0x50, 0x4B, 0x03, 0x04, 0x00, 0x00},
		"PDF":        []byte("%PDF-1.4\n"),
		"截断的 PNG":    {0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00},
	}

	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := InspectImage(data); err == nil {
				t.Fatalf("%s 不应被当作图片接受", name)
			}
		})
	}
}

// 只对文件头不足以信任：文件头对得上但内容截断的图片，
// 导出时才失败，那时已经很难归因了。
func TestInspectImageRejectsTruncatedImage(t *testing.T) {
	full := makePNG(t, 128, 128)
	// 只保留 PNG 签名与 IHDR 起始部分，去掉 IDAT 与 IEND。
	truncated := full[:20]

	if _, err := InspectImage(truncated); err == nil {
		t.Fatal("截断的 PNG 应被拒绝")
	}
}

// 解压炸弹：几百 KB 的 PNG 可以解出天文数字的像素。
//
// 这里必须生成一张**校验和正确**的真图：直接改 IHDR 里的宽高会让 CRC
// 对不上，解码器在读到尺寸之前就报"已损坏"，
// 那样测到的是 CRC 校验而不是尺寸上限。
func TestInspectImageRejectsOversizedDimensions(t *testing.T) {
	// 1 像素高、超出宽度上限：内存占用只有几十 KB，编码很快。
	huge := makePNG(t, MaxImageWidth+1, 1)

	if _, err := InspectImage(huge); err == nil {
		t.Fatal("超大尺寸的图片应被拒绝")
	} else if !strings.Contains(err.Error(), "过大") {
		t.Fatalf("错误信息应说明尺寸过大，实际 %v", err)
	}
}

// 边界：正好等于上限的应当通过。
func TestInspectImageAcceptsAtLimit(t *testing.T) {
	atLimit := makePNG(t, MaxImageWidth, 1)
	if _, err := InspectImage(atLimit); err != nil {
		t.Fatalf("等于上限的尺寸应被接受: %v", err)
	}
}

func TestSaveWritesFileAndReturnsURL(t *testing.T) {
	dir := t.TempDir()
	store := NewLocal(dir, "/media")

	saved, err := store.Save(makePNG(t, 10, 10), "题目配图.png")
	if err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	if !strings.HasPrefix(saved.URL, "/media/") {
		t.Fatalf("地址前缀错误: %s", saved.URL)
	}
	if !strings.HasSuffix(saved.URL, ".png") {
		t.Fatalf("地址扩展名错误: %s", saved.URL)
	}
	if saved.Size == 0 || saved.Width != 10 {
		t.Fatalf("保存结果信息不全: %+v", saved)
	}

	// 文件真的落盘了，并且能按地址读回来。
	loaded, ext, err := store.Load(saved.URL)
	if err != nil {
		t.Fatalf("按地址读取失败: %v", err)
	}
	if ext != "png" || len(loaded) != int(saved.Size) {
		t.Fatalf("读回内容不一致: ext=%s len=%d", ext, len(loaded))
	}
	if !store.Exists(saved.URL) {
		t.Fatal("Exists 应为真")
	}
}

// 落盘文件名用随机名，不采信客户端给的名字。
func TestSaveIgnoresClientFileName(t *testing.T) {
	dir := t.TempDir()
	store := NewLocal(dir, "/media")

	saved, err := store.Save(makePNG(t, 4, 4), "../../etc/passwd.png")
	if err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	// URL 里不能出现客户端文件名的任何痕迹。
	if strings.Contains(saved.URL, "etc") || strings.Contains(saved.URL, "passwd") ||
		strings.Contains(saved.URL, "..") {
		t.Fatalf("地址不应包含客户端文件名: %s", saved.URL)
	}
	// 原始名只作为回显字段返回，且已去掉路径部分。
	if strings.Contains(saved.OriginalName, "..") || strings.Contains(saved.OriginalName, "/") {
		t.Fatalf("回显的文件名应被清理，实际 %q", saved.OriginalName)
	}

	// 确认文件确实写在媒体目录内。
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读取媒体目录失败: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("媒体目录下应只有月份子目录，实际 %d 项", len(entries))
	}
}

func TestSaveGeneratesUniqueNames(t *testing.T) {
	dir := t.TempDir()
	store := NewLocal(dir, "/media")
	data := makePNG(t, 4, 4)

	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		saved, err := store.Save(data, "same.png")
		if err != nil {
			t.Fatalf("第 %d 次保存失败: %v", i, err)
		}
		if seen[saved.URL] {
			t.Fatalf("地址重复: %s", saved.URL)
		}
		seen[saved.URL] = true
	}
}

func TestRemoveDeletesFile(t *testing.T) {
	dir := t.TempDir()
	store := NewLocal(dir, "/media")

	saved, err := store.Save(makePNG(t, 4, 4), "a.png")
	if err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if !store.Exists(saved.URL) {
		t.Fatal("删除前应存在")
	}

	if err := store.Remove(saved.URL); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if store.Exists(saved.URL) {
		t.Fatal("删除后不应存在")
	}

	// 重复删除应当成功：调用方要的是"这个地址不再可用"。
	if err := store.Remove(saved.URL); err != nil {
		t.Fatalf("重复删除不应报错: %v", err)
	}
}

// 删除绝不能逃出媒体目录，也不能碰目录外的文件。
func TestRemoveRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	mediaDir := filepath.Join(dir, "media")
	if err := os.MkdirAll(mediaDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// 在媒体目录外放一个文件，确保它不会被删掉。
	victim := filepath.Join(dir, "victim.txt")
	if err := os.WriteFile(victim, []byte("important"), 0o600); err != nil {
		t.Fatal(err)
	}

	store := NewLocal(mediaDir, "/media")
	for _, bad := range []string{
		"/media/../../victim.txt",
		"/media/../victim.txt",
		"../../victim.txt",
		"",
	} {
		// 非法地址应被静默忽略（返回 nil），但绝不执行删除。
		_ = store.Remove(bad)
	}

	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("媒体目录外的文件被删除了: %v", err)
	}
}

func TestLoadRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	store := NewLocal(dir, "/media")

	for _, bad := range []string{
		"/media/../../etc/passwd",
		"..%2f..%2fetc/passwd",
		"/media/..",
		"",
	} {
		if _, _, err := store.Load(bad); err == nil {
			t.Fatalf("非法地址 %q 应被拒绝", bad)
		}
	}
}

// Save 与 Load 必须使用同一套前缀规则，否则导出时找不到上传的图。
func TestSaveThenLoadRoundTripWithCustomPrefix(t *testing.T) {
	dir := t.TempDir()
	store := NewLocal(dir, "static/media")

	saved, err := store.Save(makePNG(t, 6, 6), "x.png")
	if err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if !strings.HasPrefix(saved.URL, "static/media/") {
		t.Fatalf("地址前缀错误: %s", saved.URL)
	}
	if _, _, err := store.Load(saved.URL); err != nil {
		t.Fatalf("自定义前缀下应能读回: %v", err)
	}
}
