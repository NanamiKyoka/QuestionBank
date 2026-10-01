package service

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NanamiKyoka/questionmind/internal/infra/storage"
)

// 上传接口给的是 /media/xxx.jpg 这种**本服务自己的**相对路径，
// 而模型跑在供应商那边，没有任何办法访问我们的 /media。
// 原样发过去，模型只会拿到一个无法解析的地址，然后"看不见"地编一通题——
// 那比明确失败危险得多。这个用例把这条生死线钉住。
func TestMaterializeImagesInlinesLocalPaths(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewLocal(dir, "/media")

	data := fakeJPEG(t, 64, 48)
	saved, err := store.Save(data, "answer.jpg")
	if err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if !strings.HasPrefix(saved.URL, "/media/") {
		t.Fatalf("前置条件：上传返回的应当是 /media 下的路径，实际 %q", saved.URL)
	}

	svc := &ImportService{storage: store}
	got, warnings, err := svc.materializeImages([]string{saved.URL})
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("应当得到一张图片，实际 %d", len(got))
	}
	if !strings.HasPrefix(got[0], "data:image/") {
		t.Fatalf("必须变成 data URL，否则模型取不到：%q", truncateStr(got[0], 40))
	}
	if len(warnings) != 0 {
		t.Fatalf("不该有警告: %v", warnings)
	}
}

// 已经是 data URL 或公网地址的原样放行：不该把用户的公网图片下载一遍。
func TestMaterializeImagesPassesThroughExternalRefs(t *testing.T) {
	svc := &ImportService{storage: storage.NewLocal(t.TempDir(), "/media")}
	refs := []string{
		"data:image/png;base64,AAAA",
		"https://example.test/a.png",
		"http://example.test/b.png",
	}
	got, _, err := svc.materializeImages(refs)
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if len(got) != len(refs) {
		t.Fatalf("应当全部放行，实际 %d", len(got))
	}
	for i := range refs {
		if got[i] != refs[i] {
			t.Fatalf("第 %d 张应原样放行：%q", i, got[i])
		}
	}
}

// 单张坏图只丢那一张并如实报告，不让整批录题失败。
func TestMaterializeImagesSkipsBrokenOnes(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewLocal(dir, "/media")
	saved, _ := store.Save(fakeJPEG(t, 32, 32), "ok.jpg")

	svc := &ImportService{storage: store}
	got, warnings, err := svc.materializeImages([]string{
		saved.URL,
		"/media/does-not-exist.jpg",
	})
	if err != nil {
		t.Fatalf("不该整体失败: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("应当留下 1 张可用的，实际 %d", len(got))
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "第 2 张") {
		t.Fatalf("应当如实报告是哪一张坏了: %v", warnings)
	}
}

// 一张都不可用时要明确失败，而不是"成功"地返回 0 道题。
func TestMaterializeImagesFailsWhenNoneAreUsable(t *testing.T) {
	svc := &ImportService{storage: storage.NewLocal(t.TempDir(), "/media")}
	if _, _, err := svc.materializeImages([]string{"/media/missing.jpg"}); err == nil {
		t.Fatal("全部不可用时必须报错")
	}
}

// 路径穿越必须在读取这一步就被挡住，而不是读到了媒体目录之外的文件。
func TestMaterializeImagesRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(dir, "secret.txt")
	if err := os.WriteFile(outside, []byte("top secret"), 0o600); err != nil {
		t.Fatalf("准备文件失败: %v", err)
	}
	store := storage.NewLocal(filepath.Join(dir, "media"), "/media")
	if err := os.MkdirAll(store.MediaDir(), 0o755); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}

	svc := &ImportService{storage: store}
	got, warnings, err := svc.materializeImages([]string{"/media/../secret.txt"})
	if err == nil && len(got) == 1 {
		t.Fatal("穿越路径必须被拒绝")
	}
	if len(warnings) == 0 && err == nil {
		t.Fatal("被拒绝时至少要有一条提示")
	}
}

func fakeJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 90, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatalf("编码失败: %v", err)
	}
	return buf.Bytes()
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
