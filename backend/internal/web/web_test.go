package web

import "testing"

// 前端路由与静态资源的分界：末段带扩展名的按资源处理。
func TestIsSPARoute(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		// 前端路由 → 交给 SPA
		{"/", true},
		{"/questions", true},
		{"/papers/1/edit", true},
		{"/knowledge-points", true},
		{"/users", true},

		// 静态资源 → 不进 SPA
		{"/assets/index-abc123.js", false},
		{"/assets/style.css", false},
		{"/favicon.svg", false},
		{"/logo.png", false},

		// 真实存在的文件即使在根下也不该当作前端路由
		{"/index.html", false},
	}

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			if got := IsSPARoute(tc.path); got != tc.want {
				t.Fatalf("IsSPARoute(%q) = %v，期望 %v", tc.path, got, tc.want)
			}
		})
	}
}

// 带内容哈希的资源可以长期缓存；index.html 绝不能缓存，
// 否则用户会一直加载到旧版本的资源清单。
func TestHashedAssets(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/assets/index-BpObkNU9.js", true},
		{"/assets/style-Cj3qadPg.css", true},
		{"/index.html", false},
		{"/", false},
		{"", false},
		{"/favicon.svg", false},
	}

	for _, tc := range cases {
		if got := HashedAssets(tc.path); got != tc.want {
			t.Fatalf("HashedAssets(%q) = %v，期望 %v", tc.path, got, tc.want)
		}
	}
}

// 仓库里那个用于通过编译的 .gitkeep 会让 dist 目录始终存在，
// 因此不能用"目录存在"判断前端是否已构建。
func TestAvailableRequiresIndexHTML(t *testing.T) {
	// 测试环境里 dist 下只有 .gitkeep（未构建前端），此时应返回 false。
	// 若开发者本地已构建过前端，返回 true 也算正确——
	// 这里断言的是"不会因为目录里有文件就误判"。
	if Available() {
		dist, err := Dist()
		if err != nil {
			t.Fatalf("Available 为真但 Dist 失败: %v", err)
		}
		if _, err := dist.Open("index.html"); err != nil {
			t.Fatalf("Available 为真时 index.html 必须存在: %v", err)
		}
	}
}

func TestLastSegment(t *testing.T) {
	cases := map[string]string{
		"a/b/c":  "c",
		"c":      "c",
		"/a/b/":  "",
		"":       "",
		"a/b.js": "b.js",
	}
	for in, want := range cases {
		if got := lastSegment(in); got != want {
			t.Fatalf("lastSegment(%q) = %q，期望 %q", in, got, want)
		}
	}
}
