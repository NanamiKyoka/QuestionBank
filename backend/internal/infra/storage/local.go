// Package storage 提供本地文件存储访问。
//
// 导出时需要把富文本里的图片地址（形如 /media/xxx/yyy.png）解析成真实字节。
// 这里刻意做严格的白名单式校验：图片地址最终会参与文件路径拼接，
// 一旦允许 ".." 就会变成目录穿越漏洞。
package storage

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Local 基于本地目录的媒体存储。
type Local struct {
	// mediaDir 媒体文件根目录（绝对路径）。
	mediaDir string
	// publicPrefix 对外暴露的 URL 前缀，如 /media。
	publicPrefix string
}

// NewLocal 构造。
func NewLocal(mediaDir, publicPrefix string) *Local {
	if publicPrefix == "" {
		publicPrefix = "/media"
	}
	return &Local{
		mediaDir:     filepath.Clean(mediaDir),
		publicPrefix: strings.TrimRight(publicPrefix, "/"),
	}
}

// Load 按对外地址读取图片，返回内容与扩展名。
func (l *Local) Load(src string) ([]byte, string, error) {
	rel, err := l.resolve(src)
	if err != nil {
		return nil, "", err
	}

	full := filepath.Join(l.mediaDir, filepath.FromSlash(rel))

	// 二次确认：解析后的绝对路径必须仍在媒体目录内。
	// 只做字符串前缀判断不够，符号链接会让检查失效，因此这里同时校验
	// Clean 之后的结果是否以媒体根目录开头。
	absRoot, err := filepath.Abs(l.mediaDir)
	if err != nil {
		return nil, "", fmt.Errorf("storage: 解析媒体目录失败: %w", err)
	}
	absFull, err := filepath.Abs(full)
	if err != nil {
		return nil, "", fmt.Errorf("storage: 解析文件路径失败: %w", err)
	}
	if !strings.HasPrefix(absFull, absRoot+string(os.PathSeparator)) {
		return nil, "", fmt.Errorf("storage: 非法的媒体路径 %q", src)
	}

	data, err := os.ReadFile(absFull)
	if err != nil {
		return nil, "", fmt.Errorf("storage: 读取 %s 失败: %w", rel, err)
	}
	return data, extOf(rel), nil
}

// Exists 判断媒体文件是否存在。
func (l *Local) Exists(src string) bool {
	_, _, err := l.Load(src)
	return err == nil
}

// resolve 把对外地址转成相对于媒体根目录的路径。
func (l *Local) resolve(src string) (string, error) {
	s := strings.TrimSpace(src)
	if s == "" {
		return "", fmt.Errorf("storage: 空地址")
	}

	// 允许传入完整的 http(s) 地址：只取路径部分。
	if i := strings.Index(s, "://"); i >= 0 {
		if j := strings.Index(s[i+3:], "/"); j >= 0 {
			s = s[i+3+j:]
		} else {
			return "", fmt.Errorf("storage: 地址中没有路径 %q", src)
		}
	}

	// 去掉查询串与片段。
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}

	s = strings.TrimPrefix(s, l.publicPrefix)
	s = strings.TrimPrefix(s, "/")

	// path.Clean 会消解 ".."，再用 .. 前缀或绝对路径判定是否为穿越尝试。
	clean := path.Clean("/" + s)
	clean = strings.TrimPrefix(clean, "/")
	if clean == "" || clean == "." {
		return "", fmt.Errorf("storage: 地址为空 %q", src)
	}
	if strings.HasPrefix(clean, "..") || path.IsAbs(clean) {
		return "", fmt.Errorf("storage: 非法路径 %q", src)
	}
	// 只允许访问媒体目录下的普通文件（拒绝子目录逃逸与设备文件）。
	if strings.Contains(clean, "\x00") {
		return "", fmt.Errorf("storage: 路径包含空字节")
	}
	return clean, nil
}

var imageExts = map[string]bool{
	"png": true, "jpg": true, "jpeg": true, "gif": true,
	"bmp": true, "webp": true, "svg": true,
}

// extOf 返回小写扩展名。
func extOf(name string) string {
	i := strings.LastIndex(name, ".")
	if i < 0 || i == len(name)-1 {
		return ""
	}
	ext := strings.ToLower(name[i+1:])
	if !imageExts[ext] {
		// docx 只认这几种位图；其它一律按 png 处理，避免包内出现
		// Word 不认识的扩展名导致整个文档打不开。
		return "png"
	}
	if ext == "svg" {
		// OOXML 的内联图片不支持 SVG（需要额外的后备位图），降级为 png。
		return "png"
	}
	return ext
}

// MimeOf 返回图片的 MIME 类型。
func MimeOf(ext string) string {
	switch strings.ToLower(ext) {
	case "png":
		return "image/png"
	case "jpg", "jpeg":
		return "image/jpeg"
	case "gif":
		return "image/gif"
	case "bmp":
		return "image/bmp"
	case "webp":
		return "image/webp"
	}
	return "application/octet-stream"
}
