package ai

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

// allowPrivateURL 是否允许把请求拨向内网地址。
//
// base_url 由超管配置，而请求会带上 API Key：把它指向 169.254.169.254
// 这类云厂商元数据地址、或内网的管理端口，就是一次带着凭证的 SSRF。
// 因此默认拒绝回环 / RFC1918 / 链路本地 / 运营商级 NAT 网段。
//
// 本机自建网关（Ollama、vLLM 常跑在 127.0.0.1）是合理用法，
// 对应配置项 QM_AI_ALLOW_PRIVATE_URL=true 显式放开；配置了内网
// HTTP 代理（HTTP_PROXY / HTTPS_PROXY 指向内网）时同样需要打开——
// 拨号器拦下的是"实际连出去的那个地址"，也就是代理。
var allowPrivateURL atomic.Bool

// SetAllowPrivateURL 放开（或恢复）对内网地址的限制，由启动配置调用。
func SetAllowPrivateURL(v bool) { allowPrivateURL.Store(v) }

// privateURLAllowed 当前是否放行内网地址。
func privateURLAllowed() bool { return allowPrivateURL.Load() }

// ValidateBaseURL 校验供应商自定义网关地址。
//
// 空串表示"用官方默认地址"，放行；其余必须是 http/https 的绝对地址——
// file://、ftp:// 这类 scheme 被 Go 的 http 客户端拒收时，报错既晚又难懂。
func ValidateBaseURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("地址格式不正确")
	}
	if u.Scheme == "" {
		return errors.New("地址必须以 http:// 或 https:// 开头")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("只支持 http:// 与 https://，收到 %q", u.Scheme)
	}
	if u.Hostname() == "" {
		return errors.New("地址缺少主机名")
	}
	if u.User != nil {
		return errors.New("地址不能携带用户名或密码")
	}
	return nil
}

// ---- 拨号与重定向防护 ----

// sharedTransport 全部供应商共用的传输。
//
// 只构造一次：拨号防护是全局策略，而 http.Client 每次 Resolve 都会新建，
// 若每次都换一个新的传输，连接池就永远攒不起来，每条消息都要重连一遍。
var sharedTransport = newGuardedTransport()

// newGuardedTransport 在默认传输上换掉拨号器：每个目标 IP 都要先过网段检查。
//
// 之所以克隆默认传输而不是从零构造，是为了保留它的代理、超时与
// keep-alive 调优——这些与安全无关，没必要重写一遍。
func newGuardedTransport() http.RoundTripper {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return &http.Transport{DialContext: guardedDialContext}
	}
	t := base.Clone()
	t.DialContext = guardedDialContext
	return t
}

// guardedDialContext 解析一次、校验一次、再按校验过的 IP 拨号。
//
// 解析与拨号必须在同一次调用里完成：先查一遍地址再交给系统去连，
// 中间那段空隙正好够一次 DNS 重绑定把名字换成内网地址。
func guardedDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("ai: 拨号地址不合法: %w", err)
	}
	ips, err := allowedIPs(ctx, host)
	if err != nil {
		return nil, err
	}

	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	var lastErr error
	for _, ip := range ips {
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = ErrBlockedAddress
	}
	return nil, lastErr
}

// allowedIPs 解析 host，挑出其中允许拨号的地址。
//
// 域名同时解析出公网与内网地址时只用公网的那几个（常见于 CDN 或
// 反向代理），全部落在被禁网段才报错。
func allowedIPs(ctx context.Context, host string) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		if err := checkIP(ip); err != nil {
			return nil, err
		}
		return []net.IP{ip}, nil
	}

	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("ai: 解析 %s 失败: %w", host, err)
	}
	var (
		out     []net.IP
		blocked error
	)
	for _, a := range addrs {
		if err := checkIP(a.IP); err != nil {
			blocked = err
			continue
		}
		out = append(out, a.IP)
	}
	if len(out) == 0 {
		if blocked == nil {
			blocked = ErrBlockedAddress
		}
		return nil, blocked
	}
	return out, nil
}

// checkIP 判断这个地址是否允许拨号。
func checkIP(ip net.IP) error {
	if privateURLAllowed() {
		return nil
	}
	if isRestrictedIP(ip) {
		return fmt.Errorf("%w: %s；本机自建网关可设置 QM_AI_ALLOW_PRIVATE_URL=true 放开",
			ErrBlockedAddress, ip)
	}
	return nil
}

// isRestrictedIP 覆盖四类 SSRF 常用目标：回环、RFC1918 私网、链路本地
// （云厂商元数据 169.254.169.254 就在这里）、运营商级 NAT。
// 多播与未指定地址一并挡下——它们同样不是一台真实的服务。
func isRestrictedIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil {
		// 0.0.0.0/8：IsUnspecified 只认 0.0.0.0 本身。
		if ip4[0] == 0 {
			return true
		}
		// 100.64.0.0/10：不在 RFC1918 里，但同样是内网。
		if ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
			return true
		}
		// 255.255.255.255 的广播地址 Go 没有现成的判断方法，
		// 整个 255.0.0.0/8 也都留作将来使用，一并挡下。
		if ip4[0] == 255 {
			return true
		}
	}
	return false
}

// maxRedirects 重定向上限。AI 网关本不该跳来跳去，
// 每多一跳就多一次"先指到公网、再跳进内网"的机会。
const maxRedirects = 3

// checkRedirect 每次重定向前重新校验目标地址，规则与首次请求完全一致。
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("ai: 重定向次数超过 %d", maxRedirects)
	}
	if err := ValidateBaseURL(req.URL.String()); err != nil {
		return fmt.Errorf("ai: 重定向到了不合法的地址: %w", err)
	}
	if _, err := allowedIPs(req.Context(), req.URL.Hostname()); err != nil {
		return fmt.Errorf("ai: 重定向目标被拒绝: %w", err)
	}
	return nil
}
