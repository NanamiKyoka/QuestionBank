// Package pdfium 把 PDF 渲染成位图。
//
// 为什么需要它：PDF 的**文本层**对数学公式是失效的。公式在 PDF 里由
// 定位字形排版（上下标、分式的分子分母各有自己的坐标），而文本层只能
// 按视觉顺序取字符，于是 sin(7π/6) 变成"已知集合 A = sin ,cos ,tan"
// 夹着一行孤零零的 "7π/6 5π/3 5π/4"。这不是解析得不够好，是信息在
// 取字那一步就没了——实测 2026 全国 1 卷，文本层里有 463 个字母、
// 377 个数字，抽完只剩个位数。
//
// 渲染成图就没有这个问题：视觉模型看到的是排版后的样子，公式、根号、
// 上下标、几何图形全在。实测同一份卷子，题干里的字母从 9 个变成 775 个。
//
// ---- 技术选型（都是实测出来的）----
//
// 这里用 **PDFium 的 WebAssembly 构建 + wazero（纯 Go 的 wasm 运行时）**，
// 而不是常见的 PDFium/MuPDF cgo 绑定：
//
//   - cgo 方案（go-fitz/MuPDF）：它的预编译静态库是用老版 MinGW 构建的，
//     在 GCC 15 下链接直接失败（`undefined reference to __intrinsic_setjmpex`）。
//     而一旦启用 cgo，build.ps1 里四处写死的 CGO_ENABLED=0 与六个交叉
//     编译目标全部要推翻。
//   - 单文件要求：wasm 模块随二进制走，部署时不需要额外文件。
//
// 代价是二进制大 5.5 MB（内嵌 wasm 模块），以及首次初始化约 2 秒——
// 后者用懒初始化解决：没上传过 PDF 的用户完全不付这个时间。
package pdfium

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/webassembly"
	"github.com/tetratelabs/wazero"
)

// DefaultDPI 渲染分辨率。
//
// 取 150 而不是更高：视觉模型内部会缩到 1500~2000 的处理边长
// （见 visionprep.MaxEdge），再高只是让请求变大而识别效果不再提升；
// 而 150 DPI 下试卷的下标、分数线仍然清晰可辨。实测三页 A4 约 400 KB/页。
const DefaultDPI = 150

// maxPages 一次最多渲染多少页。
//
// 上限的意义是给用户一个明确的失败而不是"内存被吃光"：一份 500 页的
// 教材整本传上来时，与其渲染到一半崩掉，不如直接告诉用户这份文件太长。
const maxPages = 100

// initTimeout 引擎初始化（含编译 wasm 模块）的上限。
const initTimeout = 60 * time.Second

// mountPoint 临时目录在 wasm 里的挂载点。
//
// 必须是 POSIX 路径：wasm 侧只认斜杠分隔，给 "C:\..." 会被直接拒绝
// （"using Windows volumes is not possible in WebAssembly"）。
const mountPoint = "/data"

// Page 渲染出来的一页。
type Page struct {
	Index  int // 0 起
	PNG    []byte
	Width  int
	Height int
}

// renderFunc 是实际的渲染入口，抽出变量是为了让单元测试能替换掉它：
// 真实实现要起一个 wasm 运行时，而测试要断言的是"某页失败时其余页仍然返回"
// 这类分支，不该为每个用例付两秒初始化。
var renderFunc = renderPDFium

var (
	initOnce sync.Once
	initErr  error

	// scratch 是引擎专用的临时目录：wasm 的挂载配置只能在初始化时确定，
	// 而渲染时才需要往里写文件——所以目录与引擎同生命周期，建一次一直复用。
	scratch     string
	wasmRuntime pdfium.Pool
)

// Render 把 PDF 逐页渲染成 PNG。
//
// 返回的字节是 PNG 编码结果：调用方（录题链路）要把它当成普通图片交给
// 视觉模型，直接传字节比传 *image.RGBA 少一次编解码往返。
//
// 部分页失败时返回**已成功的那些页**而不是整体报错：整份丢弃会让用户白等，
// 而少一两页的损失远小于重来一次。只有一页都没成才算失败。
func Render(ctx context.Context, data []byte) ([]Page, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("PDF 内容为空")
	}

	if err := ensureRuntime(); err != nil {
		return nil, err
	}

	pages, err := renderFunc(ctx, data, scratch, mountPoint, DefaultDPI)
	if err != nil {
		return nil, err
	}
	if len(pages) == 0 {
		return nil, fmt.Errorf("这份 PDF 的页面都没能渲染出来")
	}
	return pages, nil
}

// ensureRuntime 懒初始化 wasm 运行时。
//
// 用 sync.Once 而不是每次调用都新建：编译 wasm 模块一次要约 2 秒，
// 而录题是低频操作，绝大多数进程生命周期里根本不会用到它——
// 懒初始化让"从不上传 PDF"的用户完全不付这个时间。
//
// 只初始化一次也顺带解决了并发问题：wazero 的实例不是并发安全的。
// MaxTotal=1 保证同一时刻只有一个实例在跑，并发调用会等待而不是
// 把渲染器搞坏（录题队列本身并发度就是 1）。
func ensureRuntime() error {
	initOnce.Do(func() {
		dir, err := os.MkdirTemp("", "qm-pdfium-")
		if err != nil {
			initErr = fmt.Errorf("准备渲染目录失败：%w", err)
			return
		}
		p, err := webassembly.Init(webassembly.Config{
			MinIdle:  0,
			MaxIdle:  1,
			MaxTotal: 1,
			// wazero 跑在沙箱里，看不到真实文件系统，必须显式把 scratch
			// 挂进 wasm 的路径空间。不挂的话它默认挂"当前工作目录所在卷"，
			// 而服务的 CWD 是什么并不由我们决定。
			FSConfig: wazero.NewFSConfig().WithDirMount(dir, mountPoint),
		})
		if err != nil {
			_ = os.RemoveAll(dir)
			initErr = fmt.Errorf("PDF 渲染引擎初始化失败：%w", err)
			return
		}
		scratch, wasmRuntime = dir, p
	})
	return initErr
}

// renderPDFium 是真实渲染实现。
func renderPDFium(ctx context.Context, data []byte, dir, mount string, dpi int) ([]Page, error) {
	name := randName() + ".pdf"
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
		return nil, fmt.Errorf("写入临时 PDF 失败：%w", err)
	}
	// 渲染完立刻删掉：原始试卷是未公开的教研材料，
	// 放在临时目录里等系统清理，窗口期能短一点是一点。
	defer func() { _ = os.Remove(filepath.Join(dir, name)) }()

	inst, err := wasmRuntime.GetInstance(initTimeout)
	if err != nil {
		return nil, fmt.Errorf("取渲染实例失败：%w", err)
	}
	// 必须归还实例：池的 MaxTotal=1，不 Close 的话实例一直被占着，
	// 第二次渲染会一直等到 initTimeout 才失败——第一次正常、之后全部超时，
	// 这种故障在现场极难与"PDF 太大"区分开。
	defer func() { _ = inst.Close() }()

	d, err := inst.OpenDocument(&requests.OpenDocument{
		FilePath: strPtr(mount + "/" + name),
	})
	if err != nil {
		return nil, humanizeOpenError(err)
	}

	pc, err := inst.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: d.Document})
	if err != nil {
		return nil, fmt.Errorf("读取页数失败：%w", err)
	}
	count := pc.PageCount
	if count == 0 {
		return nil, fmt.Errorf("这份 PDF 没有任何页面")
	}
	if count > maxPages {
		return nil, fmt.Errorf("这份 PDF 有 %d 页，超过单次上限 %d 页；请拆分成几份再上传", count, maxPages)
	}

	pages := make([]Page, 0, count)
	for i := 0; i < count; i++ {
		if ctx.Err() != nil {
			break
		}
		p, err := inst.RenderPageInDPI(&requests.RenderPageInDPI{
			Page: requests.Page{ByIndex: &requests.PageByIndex{
				Document: d.Document,
				Index:    i,
			}},
			DPI: dpi,
		})
		if err != nil {
			// 单页失败就跳过：一份卷子里某一页有特殊内容（比如损坏的字体）
			// 不该让整份卷子录不进来。少一页远好过重来一次。
			continue
		}
		raw, w, h, encErr := encodePNG(p.Result.Image)
		p.Cleanup()
		if encErr != nil {
			continue
		}
		pages = append(pages, Page{Index: i, PNG: raw, Width: w, Height: h})
	}
	return pages, nil
}

// humanizeOpenError 把 PDFium 的英文错误翻成用户看得懂的话。
func humanizeOpenError(err error) error {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "password"):
		return fmt.Errorf("这份 PDF 有打开密码，请先去掉密码再上传")
	case strings.Contains(msg, "corrupt"), strings.Contains(msg, "damaged"):
		return fmt.Errorf("这份 PDF 已损坏，无法渲染")
	}
	return fmt.Errorf("PDF 无法打开：%w", err)
}

// encodePNG 把渲染结果编码成 PNG 字节。
func encodePNG(img image.Image) ([]byte, int, int, error) {
	if img == nil {
		return nil, 0, 0, fmt.Errorf("空图像")
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, 0, 0, err
	}
	b := img.Bounds()
	return buf.Bytes(), b.Dx(), b.Dy(), nil
}

// randName 生成不会撞车的临时文件名。
//
// 用随机串而不是自增序号：录题可能并发（HTTP 上传与队列 worker 各一份），
// 撞名会让两个请求互相覆盖对方的 PDF。
func randName() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "input"
	}
	return hex.EncodeToString(b)
}

func strPtr(s string) *string { return &s }
