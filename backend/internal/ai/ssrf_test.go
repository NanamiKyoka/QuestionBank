package ai

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
)

// TestMain 测试里的假端点全部起在 127.0.0.1 上，因此统一放开对内网地址的
// 限制——默认拒绝是给线上配置用的。需要验证拦截行为的用例自己临时关掉并还原。
func TestMain(m *testing.M) {
	SetAllowPrivateURL(true)
	os.Exit(m.Run())
}

// 网关地址必须是 http/https 的绝对地址。
func TestValidateBaseURL(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string // 期望的错误片段；空串表示应当通过
	}{
		{"", ""},
		{"  ", ""},
		{"https://api.openai.com/v1", ""},
		{"http://127.0.0.1:11434/v1", ""},
		{"file:///etc/passwd", "http"},
		{"ftp://mirror.example.com/v1", "http"},
		{"api.openai.com/v1", "http://"},
		{"https://user:pass@example.com/v1", "用户名"},
	} {
		err := ValidateBaseURL(tc.in)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%q 应当通过，实际报错: %v", tc.in, err)
		case tc.want != "" && err == nil:
			t.Errorf("%q 应当被拒绝", tc.in)
		case tc.want != "" && !strings.Contains(err.Error(), tc.want):
			t.Errorf("%q 的报错应包含 %q，实际: %v", tc.in, tc.want, err)
		}
	}
}

// 回环、私网、链路本地、CGNAT 一律不许拨号。
func TestIsRestrictedIP(t *testing.T) {
	for _, tc := range []struct {
		ip   string
		want bool
	}{
		{"127.0.0.1", true},
		{"127.8.8.8", true},
		{"10.0.0.1", true},
		{"172.16.0.1", true},
		{"192.168.1.1", true},
		{"169.254.169.254", true}, // 云厂商元数据
		{"0.0.0.0", true},
		{"0.1.2.3", true},
		{"100.64.0.1", true}, // 运营商级 NAT
		{"255.255.255.255", true},
		{"::1", true},
		{"fe80::1", true},
		{"fd00::1", true},
		{"::", true},
		{"8.8.8.8", false},
		{"93.184.216.34", false},
		{"100.63.0.1", false},
		{"2606:4700:4700::1111", false},
	} {
		if got := isRestrictedIP(net.ParseIP(tc.ip)); got != tc.want {
			t.Errorf("isRestrictedIP(%s) = %v，期望 %v", tc.ip, got, tc.want)
		}
	}
}

// 关掉豁免后，拨向本机地址必须在建连前就被拦下。
func TestGuardedDialBlocksLoopback(t *testing.T) {
	SetAllowPrivateURL(false)
	t.Cleanup(func() { SetAllowPrivateURL(true) })

	for _, addr := range []string{
		"127.0.0.1:80",
		"169.254.169.254:80",
		"10.1.2.3:443",
		"[::1]:80",
	} {
		_, err := guardedDialContext(context.Background(), "tcp", addr)
		if !errors.Is(err, ErrBlockedAddress) {
			t.Errorf("%s 应被拒绝，实际 %v", addr, err)
		}
	}
}

// 放开豁免后，同样的地址可以正常建连。
func TestGuardedDialAllowsLoopbackWhenEnabled(t *testing.T) {
	SetAllowPrivateURL(true)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	defer ln.Close()
	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr == nil {
			_ = conn.Close()
		}
	}()

	conn, err := guardedDialContext(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("豁免打开时本机地址应能建连，实际 %v", err)
	}
	_ = conn.Close()
}

// 每次重定向都要重新过一遍地址校验，否则"先指公网再跳内网"就绕过去了。
func TestCheckRedirectRevalidatesTarget(t *testing.T) {
	SetAllowPrivateURL(false)
	t.Cleanup(func() { SetAllowPrivateURL(true) })

	req, err := http.NewRequest(http.MethodGet, "http://169.254.169.254/latest/meta-data/", nil)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	err = checkRedirect(req, []*http.Request{req})
	if !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("重定向到内网必须被拦，实际 %v", err)
	}

	// 非 http(s) 的重定向同样拒绝。
	fileReq, err := http.NewRequest(http.MethodGet, "file:///etc/passwd", nil)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	if err := checkRedirect(fileReq, []*http.Request{fileReq}); err == nil {
		t.Fatal("重定向到 file:// 应当被拒绝")
	}

	// 重定向次数有上限。
	plain, err := http.NewRequest(http.MethodGet, "https://api.openai.com/v1", nil)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	if err := checkRedirect(plain, make([]*http.Request, maxRedirects)); err == nil {
		t.Fatal("超过重定向上限应当被拒绝")
	}
}

// 上游响应体只进日志，绝不出现在错误串里——它会被原样回显给调用方。
func TestClassifyHTTPStatusDoesNotEchoUpstreamBody(t *testing.T) {
	const secret = `{"detail":"内网主机 db.internal:5432 的 502"}`
	for _, status := range []int{400, 401, 404, 429, 500, 502} {
		err := classifyHTTPStatus(status, secret, "")
		if err == nil {
			t.Fatalf("HTTP %d 应当产出错误", status)
		}
		if strings.Contains(err.Error(), "db.internal") || strings.Contains(err.Error(), "detail") {
			t.Fatalf("HTTP %d 的错误串不应包含上游响应体: %v", status, err)
		}
	}
	if err := classifyHTTPStatus(400, "", ""); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("400 应识别为 ErrBadRequest，实际 %v", err)
	}
	if err := classifyHTTPStatus(502, "", ""); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("502 应识别为 ErrUnavailable，实际 %v", err)
	}
}

// 同样的纪律适用于响应体里的 error 对象。
func TestClassifyAPIErrorDoesNotEchoUpstreamMessage(t *testing.T) {
	err := classifyAPIError("quota exceeded for tenant at 10.0.0.7", "insufficient_quota")
	if err == nil {
		t.Fatal("应当产出错误")
	}
	if strings.Contains(err.Error(), "10.0.0.7") || strings.Contains(err.Error(), "tenant") {
		t.Fatalf("错误串不应包含上游 message: %v", err)
	}
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("应识别为限流，实际 %v", err)
	}

	unknown := classifyAPIError("something about internal-host", "weird_type")
	if strings.Contains(unknown.Error(), "internal-host") {
		t.Fatalf("未识别的错误也不应回显上游 message: %v", unknown)
	}
	if !errors.Is(unknown, ErrBadRequest) {
		t.Fatalf("未识别的错误应归为 ErrBadRequest，实际 %v", unknown)
	}
}

// 配置里的地址在发起请求前就该被校验，而不是等拨号失败。
func TestConfigValidateRejectsNonHTTPBaseURL(t *testing.T) {
	cfg := Config{InterfaceType: TypeOpenAI, APIKey: "k", BaseURL: "file:///etc/passwd"}
	err := cfg.validate()
	if err == nil || !strings.Contains(err.Error(), "网关地址无效") {
		t.Fatalf("非 http(s) 地址应被拒绝，实际 %v", err)
	}
}
