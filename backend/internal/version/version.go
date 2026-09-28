// Package version 保存构建期注入的版本信息。
//
// 通过 -ldflags 在编译时注入，例如：
//
//	go build -ldflags "-X github.com/.../internal/version.Version=1.2.3"
//
// 这样 /api/v1/system/version 返回的就是真实构建版本，
// 便于交付后确认用户装的到底是哪一个包。
package version

import "runtime"

// 以下变量由构建脚本注入，默认值用于本地开发。
var (
	Version   = "0.1.0-dev"
	Commit    = "unknown"
	BuildTime = "unknown"
)

// Info 构建信息。
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildTime string `json:"build_time"`
	GoVersion string `json:"go_version"`
	Platform  string `json:"platform"`
}

// Get 返回当前构建信息。
func Get() Info {
	return Info{
		Version:   Version,
		Commit:    Commit,
		BuildTime: BuildTime,
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}
}
