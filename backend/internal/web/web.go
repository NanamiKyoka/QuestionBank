// Package web 内嵌前端构建产物，让后端可以单文件部署。
//
// 构建流程：先在前端目录执行 pnpm build，再由 Makefile 把
// frontend/dist 复制到本包的 dist 目录，最后编译 Go 二进制。
// 内嵌是"编译期快照"——改完前端必须重新构建二进制才会生效，
// 这一点在 Makefile 里通过依赖关系保证。
//
// 目录里保留一个 .gitkeep，使得**没构建过前端时也能编译通过**：
// 此时 Available() 返回 false，服务端只提供 API，不提供界面。
// 比起用构建标签或两个二进制，这样对使用者更简单。
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var embedded embed.FS

// Dist 返回内嵌的前端产物根目录。
func Dist() (fs.FS, error) {
	return fs.Sub(embedded, "dist")
}

// Available 判断前端产物是否真的存在。
//
// 只判断目录存在是不够的：仓库里那个用于通过编译的 .gitkeep
// 会让目录始终存在。真正的标志是 index.html。
func Available() bool {
	f, err := Dist()
	if err != nil {
		return false
	}
	info, err := fs.Stat(f, "index.html")
	return err == nil && !info.IsDir()
}

// HashedAssets 判断该请求是否指向带内容哈希的静态资源。
//
// Vite 产出的文件名里带哈希（如 index-BpObkNU9.js），内容变了文件名就变，
// 因此可以放心地长期缓存。index.html 本身绝不能缓存——
// 它引用的哈希文件名会随每次构建变化，缓存住 index.html
// 会让用户永远加载到旧版本的资源清单。
func HashedAssets(p string) bool {
	if strings.HasSuffix(p, "index.html") || p == "/" || p == "" {
		return false
	}
	// 带 -XXXXXXXX 形式的哈希后缀，或位于 assets/ 目录下。
	if strings.Contains(p, "/assets/") {
		return true
	}
	return false
}

// IsSPARoute 判断该路径是否应交给单页应用处理（而不是返回 404）。
//
// 判据是"末段带不带扩展名"：带扩展名的当作静态资源请求，
// 找不到就老实 404；不带的当作前端路由，回落 index.html。
func IsSPARoute(urlPath string) bool {
	p := strings.Trim(urlPath, "/")
	if p == "" {
		return true
	}
	if _, err := Dist(); err != nil {
		return false
	}
	// 真实存在的文件不算前端路由。
	if dist, err := Dist(); err == nil {
		if info, err := fs.Stat(dist, p); err == nil && !info.IsDir() {
			return false
		}
	}
	return !strings.Contains(lastSegment(p), ".")
}

// ServeSPA 返回一个处理单页应用的处理器。
//
// 逻辑：
//  1. 命中真实文件 → 直接返回（带合适的缓存头）
//  2. 未命中且看起来像静态资源（有扩展名） → 404，不要回落到 index.html
//     否则一个写错的图片地址会返回 HTML，浏览器报"图片格式错误"，
//     排查起来很费劲
//  3. 其余路径 → 返回 index.html，交给前端路由
//
// ⚠️ 调用方注意：gin 在进入 NoRoute 之前就已经把状态码写成 404
// （见其 serveError），因此这里写出的响应会带着 404 状态。
// 在 gin 里使用时必须先调 IsSPARoute 判断，是前端路由就先
// c.Status(http.StatusOK) 把状态改回来。
func ServeSPA() http.Handler {
	dist, err := Dist()
	if err != nil {
		return http.NotFoundHandler()
	}
	files := http.FileServer(http.FS(dist))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upath := strings.TrimPrefix(r.URL.Path, "/")
		if upath == "" {
			upath = "index.html"
		}

		if info, err := fs.Stat(dist, upath); err == nil && !info.IsDir() {
			if HashedAssets(r.URL.Path) {
				// 一年，且 immutable：文件名带哈希，内容永不改变。
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}
			files.ServeHTTP(w, r)
			return
		}

		// 看起来是资源请求但文件不存在 → 老实返回 404。
		if strings.Contains(lastSegment(upath), ".") {
			http.NotFound(w, r)
			return
		}

		// 前端路由：交给 index.html。
		w.Header().Set("Cache-Control", "no-cache")
		r.URL.Path = "/"
		serveIndex(w, r, dist)
	})
}

func serveIndex(w http.ResponseWriter, r *http.Request, dist fs.FS) {
	data, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

func lastSegment(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}
