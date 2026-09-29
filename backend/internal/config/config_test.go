package config

import (
	"strings"
	"testing"
)

func TestSetAddr(t *testing.T) {
	cases := []struct {
		addr     string
		wantHost string
		wantPort int
	}{
		{"127.0.0.1:9000", "127.0.0.1", 9000},
		{"0.0.0.0:80", "0.0.0.0", 80},
		{":7000", "", 7000},
		{"localhost", "localhost", 0}, // 0 表示端口不变
	}

	for _, tc := range cases {
		t.Run(tc.addr, func(t *testing.T) {
			cfg := &Config{}
			cfg.Server.Host = "原始主机"
			cfg.Server.Port = 1234

			if err := SetAddr(cfg, tc.addr); err != nil {
				t.Fatalf("SetAddr(%q) 失败: %v", tc.addr, err)
			}
			if tc.wantHost != "" && cfg.Server.Host != tc.wantHost {
				t.Fatalf("主机应为 %q，实际 %q", tc.wantHost, cfg.Server.Host)
			}
			if tc.wantPort != 0 && cfg.Server.Port != tc.wantPort {
				t.Fatalf("端口应为 %d，实际 %d", tc.wantPort, cfg.Server.Port)
			}
		})
	}
}

// 空地址表示"不改动"，不该把已有配置清掉。
func TestSetAddrEmptyKeepsConfig(t *testing.T) {
	cfg := &Config{}
	cfg.Server.Host = "1.2.3.4"
	cfg.Server.Port = 5555

	if err := SetAddr(cfg, "   "); err != nil {
		t.Fatalf("空地址不应报错: %v", err)
	}
	if cfg.Server.Host != "1.2.3.4" || cfg.Server.Port != 5555 {
		t.Fatalf("空地址不应改动配置，实际 %s:%d", cfg.Server.Host, cfg.Server.Port)
	}
}

func TestSetAddrRejectsInvalid(t *testing.T) {
	cases := []string{
		"127.0.0.1:notaport",
		"127.0.0.1:0",
		"127.0.0.1:70000",
		"[::1]:abc",
	}
	for _, addr := range cases {
		cfg := &Config{}
		if err := SetAddr(cfg, addr); err == nil {
			t.Fatalf("地址 %q 应被拒绝", addr)
		}
	}
}

func TestSetAddrHandlesIPv6(t *testing.T) {
	cfg := &Config{}
	if err := SetAddr(cfg, "[::1]:8080"); err != nil {
		t.Fatalf("IPv6 地址应被接受: %v", err)
	}
	if cfg.Server.Host != "::1" || cfg.Server.Port != 8080 {
		t.Fatalf("IPv6 解析错误: %s:%d", cfg.Server.Host, cfg.Server.Port)
	}
}

// 不认识的覆盖键必须报错。
//
// 曾经这里静默跳过 "server.mode"，桌面版传了 release 却没生效，
// gin 以 debug 模式启动并把整张路由表打到控制台——
// 参数传了不生效，比参数写错更难发现。
func TestLoadOverridesRejectsUnknownKey(t *testing.T) {
	_, err := LoadOverrides("", map[string]string{"server.mod": "release"})
	if err == nil {
		t.Fatal("未知覆盖键应报错")
	}
	if !strings.Contains(err.Error(), "server.mod") {
		t.Fatalf("错误信息应指明是哪个键，实际 %v", err)
	}
}

func TestLoadOverridesAppliesMode(t *testing.T) {
	cfg, err := LoadOverrides("", map[string]string{
		"server.mode": "release",
		"server.port": "9090",
		"server.host": "127.0.0.1",
	})
	if err != nil {
		t.Fatalf("应用覆盖失败: %v", err)
	}
	if cfg.Server.Mode != "release" {
		t.Fatalf("模式应为 release，实际 %q", cfg.Server.Mode)
	}
	if !cfg.IsProduction() {
		t.Fatal("release 模式下 IsProduction 应为真")
	}
	if cfg.Server.Port != 9090 {
		t.Fatalf("端口应为 9090，实际 %d", cfg.Server.Port)
	}
}

func TestLoadOverridesRejectsInvalidMode(t *testing.T) {
	if _, err := LoadOverrides("", map[string]string{"server.mode": "prod"}); err == nil {
		t.Fatal("非法的模式取值应被拒绝")
	}
}

// 空值表示"不覆盖"，应在校验之前被跳过。
func TestLoadOverridesSkipsEmptyValues(t *testing.T) {
	cfg, err := LoadOverrides("", map[string]string{
		"server.mode":    "",
		"some.unknown":   "",
		"server.address": "",
	})
	if err != nil {
		t.Fatalf("空值应被跳过而不是报错: %v", err)
	}
	if cfg == nil {
		t.Fatal("应返回配置")
	}
}
