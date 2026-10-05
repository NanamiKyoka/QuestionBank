package service

import (
	"os"
	"testing"

	"github.com/NanamiKyoka/questionmind/internal/ai"
)

// TestMain 测试里的假 AI 端点全部起在 127.0.0.1 上，因此统一放开 ai 包
// 对内网地址的限制——默认拒绝是给线上配置用的（详见 ai.SetAllowPrivateURL）。
func TestMain(m *testing.M) {
	ai.SetAllowPrivateURL(true)
	os.Exit(m.Run())
}
