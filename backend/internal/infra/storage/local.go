// Package storage 提供本地文件存储访问。
//
// 两个方向都用同一套路径校验：
//   - 导出时按富文本里的地址（形如 /media/xxx/yyy.png）读取图片
//   - 上传时把字节写进媒体目录并返回对外地址
//
// 这里刻意做严格的白名单式校验：图片地址最终会参与文件路径拼接，
// 一旦允许 ".." 就会变成目录穿越漏洞。
package storage

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"image"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	// 注册解码器，供 DecodeConfig 识别尺寸。
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
)

// 图片尺寸上限。防止"解压炸弹"：一个几百 KB 的 PNG 可以解出几十亿像素，
// 后续渲染或导出时把内存吃光。上传时就挡住，别等到用的时候才发现。
const (
	MaxImageWidth  = 20000
	MaxImageHeight = 20000
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

// Save 保存上传的图片，返回对外可访问的地址。
//
// 扩展名**由内容嗅探决定**，不采信客户端给的文件名：
// 客户端可以把 a.exe 命名成 a.png，但改不了文件头的魔数。
// 落库地址里的扩展名会参与后续的 MIME 判断与 OOXML 打包，
// 信错一次就会产出打不开的文档。
func (l *Local) Save(data []byte, declaredName string) (*Saved, error) {
	info, err := InspectImage(data)
	if err != nil {
		return nil, err
	}

	// 按年月分目录：媒体文件全部堆在一个目录里时，
	// 文件数上万后连 ls 都会变慢。
	rel := path.Join(
		time.Now().UTC().Format("2006/01"),
		randomName()+"."+info.Ext,
	)
	full := filepath.Join(l.mediaDir, filepath.FromSlash(rel))

	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return nil, fmt.Errorf("storage: 创建媒体目录失败: %w", err)
	}
	// 0600：媒体文件只由本进程读写，不需要给同机其它用户开放。
	if err := os.WriteFile(full, data, 0o600); err != nil {
		return nil, fmt.Errorf("storage: 写入媒体文件失败: %w", err)
	}

	return &Saved{
		URL:       l.publicPrefix + "/" + rel,
		Ext:       info.Ext,
		Mime:      info.Mime,
		Width:     info.Width,
		Height:    info.Height,
		Size:      int64(len(data)),
		MediaPath: rel,
		// 落盘时的原始文件名只作为提示返回，不参与路径构造。
		OriginalName: sanitizeName(declaredName),
	}, nil
}

// Remove 删除媒体文件。地址非法或文件不存在都当作成功：
// 调用方要的是"这个地址不再可用"，重复删除不该报错。
func (l *Local) Remove(src string) error {
	rel, err := l.resolve(src)
	if err != nil {
		return nil
	}
	full := filepath.Join(l.mediaDir, filepath.FromSlash(rel))

	absRoot, err := filepath.Abs(l.mediaDir)
	if err != nil {
		return err
	}
	absFull, err := filepath.Abs(full)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(absFull, absRoot+string(os.PathSeparator)) {
		return fmt.Errorf("storage: 非法的媒体路径 %q", src)
	}

	if err := os.Remove(absFull); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("storage: 删除媒体文件失败: %w", err)
	}
	return nil
}

// Read 读回一个媒体文件的原始字节。
//
// 只接受媒体目录内的地址，且与 Remove 一样做绝对路径兜底：
// 地址来自客户端时，它只是"想去哪个文件"的表达，不是可信输入。
func (l *Local) Read(src string) ([]byte, error) {
	rel, err := l.resolve(src)
	if err != nil {
		return nil, err
	}
	full := filepath.Join(l.mediaDir, filepath.FromSlash(rel))

	absRoot, err := filepath.Abs(l.mediaDir)
	if err != nil {
		return nil, err
	}
	absFull, err := filepath.Abs(full)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(absFull, absRoot+string(os.PathSeparator)) {
		return nil, fmt.Errorf("storage: 非法的媒体路径 %q", src)
	}
	return os.ReadFile(absFull)
}

// Saved 保存结果。
type Saved struct {
	URL          string
	Ext          string
	Mime         string
	Width        int
	Height       int
	Size         int64
	MediaPath    string
	OriginalName string
}

// ImageInfo 图片的基本信息。
type ImageInfo struct {
	Ext    string
	Mime   string
	Width  int
	Height int
}

// InspectImage 校验字节确实是一张可用的图片，并返回其信息。
//
// 两道关：先看魔数判断格式，再用 DecodeConfig 读出尺寸。
// 只看魔数不够——文件头对得上但内容截断的图片，导出时会失败；
// 而 DecodeConfig 只解析文件头，代价很低。
func InspectImage(data []byte) (*ImageInfo, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("storage: 文件为空")
	}

	ext, mime, ok := sniffImage(data)
	if !ok {
		return nil, fmt.Errorf("storage: 不是受支持的图片格式（支持 PNG / JPEG / GIF / WebP）")
	}

	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("storage: 图片已损坏或无法解析: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, fmt.Errorf("storage: 图片尺寸非法")
	}
	if cfg.Width > MaxImageWidth || cfg.Height > MaxImageHeight {
		return nil, fmt.Errorf("storage: 图片尺寸过大（%d×%d，上限 %d×%d）",
			cfg.Width, cfg.Height, MaxImageWidth, MaxImageHeight)
	}

	return &ImageInfo{Ext: ext, Mime: mime, Width: cfg.Width, Height: cfg.Height}, nil
}

// sniffImage 按文件头判断图片格式。
//
// 不用 http.DetectContentType：它对 SVG 会返回 text/xml 或 text/plain，
// 而且我们希望**明确拒绝 SVG**——SVG 可以内嵌脚本，
// 直接当图片提供出去等于给了 stored XSS 的机会。
func sniffImage(data []byte) (ext, mime string, ok bool) {
	switch {
	case len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}):
		return "png", "image/png", true
	case len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		return "jpg", "image/jpeg", true
	case len(data) >= 6 && (bytes.Equal(data[:6], []byte("GIF87a")) || bytes.Equal(data[:6], []byte("GIF89a"))):
		return "gif", "image/gif", true
	case len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return "webp", "image/webp", true
	}
	return "", "", false
}

// randomName 生成 16 字节随机十六进制名。
//
// 用随机名而不是原始文件名：原始名既可能重名覆盖，
// 也可能包含路径分隔符或不可见字符，直接落盘会带来一串麻烦。
func randomName() string {
	var buf [16]byte
	if _, err := io.ReadFull(rand.Reader, buf[:]); err != nil {
		// 随机源不可用时退回时间戳。这里不该让上传整体失败。
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf[:])
}

// sanitizeName 清理原始文件名，仅用于回显。
func sanitizeName(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	name = strings.ReplaceAll(name, "\x00", "")
	// 截断过长的文件名，避免把整段内容回显到界面上。
	if len(name) > 200 {
		name = name[:200]
	}
	if name == "." || name == string(os.PathSeparator) {
		return ""
	}
	return name
}

// PublicPrefix 返回对外暴露的 URL 前缀。
func (l *Local) PublicPrefix() string { return l.publicPrefix }

// MediaDir 返回媒体根目录（绝对路径）。
func (l *Local) MediaDir() string { return l.mediaDir }

// Save 的别名形式，便于在只关心地址的场景使用。
func (l *Local) PublicURL(mediaPath string) string {
	return l.publicPrefix + "/" + strings.TrimPrefix(mediaPath, "/")
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
