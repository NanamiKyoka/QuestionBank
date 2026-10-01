package visionprep

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 用真实的试卷扫描件跑一遍：合成图跑得通不代表真照片跑得通
// （真照片有压缩噪点与非整数缩放比）。
func TestToDataURLOnRealPhotos(t *testing.T) {
	dir := os.Getenv("QMB_PHOTO_DIR")
	if dir == "" {
		t.Skip("未配置 QMB_PHOTO_DIR")
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.jpg"))
	if err != nil || len(files) == 0 {
		t.Skipf("目录下没有图片: %v", err)
	}

	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("读取失败: %v", err)
		}
		url, err := ToDataURL(raw)
		if err != nil {
			t.Errorf("%s 转换失败: %v", filepath.Base(f), err)
			continue
		}
		payload := len(strings.TrimPrefix(url, "data:image/jpeg;base64,"))
		decoded, _ := base64.StdEncoding.DecodeString(
			strings.TrimPrefix(url, "data:image/jpeg;base64,"))
		t.Logf("%s：%d KB -> %d KB（%.0f%%），解码后 %d 字节",
			filepath.Base(f)[:8], len(raw)>>10, payload>>10,
			float64(payload)*100/float64(len(raw)), len(decoded))
	}
}
