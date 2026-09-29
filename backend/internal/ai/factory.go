package ai

import (
	"fmt"
	"slices"
	"strings"
)

// supportedInterfaceTypes 当前真正跑得起来的协议清单，全系统唯一的权威定义：
// 界面下拉框（InterfaceTypes）、保存时的校验（SupportsInterfaceType）
// 与工厂本身都读它，将来新增厂商只改这一处，不会出现"界面能选、后端不认"。
var supportedInterfaceTypes = []string{TypeOpenAI}

// NewProvider 按配置构造供应商实现。
//
// 目前只实现 OpenAI 兼容协议。这不是偷懒：DeepSeek、通义千问、智谱、
// 月之暗面、硅基流动等国内主流厂商都提供 OpenAI 兼容端点，
// 差异只在 base_url 与模型名，一个实现就能覆盖绝大多数使用场景。
// Gemini 的原生协议差异较大（请求体、函数调用格式、思维链签名都不一样），
// 留到需要时再补——与其写一个半成品，不如先明确不支持。
//
// "不支持"是被强制的，而不只是一句注释：
//   - 供应商保存时就按 InterfaceTypes 校验，存不下用不了的协议；
//   - 仍能通过历史数据或直接改库写入的类型，在这里的 NewProvider 拒绝，
//     由 Resolve 转换成「AI 不可用」，不会被当成配置成功。
//
// Google 官方也提供 OpenAI 兼容端点
// （https://generativelanguage.googleapis.com/v1beta/openai/），
// 所以要用 Gemini 走 openai 类型 + 该 base_url，而不是新增一个协议。
func NewProvider(cfg Config) (Provider, error) {
	// 空类型按 OpenAI 兼容处理：这是最常见的期望，
	// 也避免用户没填类型时直接报错。
	if cfg.InterfaceType == "" {
		cfg.InterfaceType = TypeOpenAI
	}

	if !SupportsInterfaceType(cfg.InterfaceType) {
		return nil, fmt.Errorf("ai: 暂不支持接口类型 %q，当前可用：%s；"+
			"若该厂商提供 OpenAI 兼容端点，请把接口类型改为 %s 并填写对应的 base_url",
			cfg.InterfaceType, strings.Join(InterfaceTypes(), "、"), TypeOpenAI)
	}

	switch cfg.InterfaceType {
	case TypeOpenAI:
		return NewOpenAI(cfg), nil
	default:
		// 登记进 supportedInterfaceTypes 却没写实现：属于开发时的疏漏，
		// 明确报错好过悄悄返回 nil 让上层空指针。
		return nil, fmt.Errorf("ai: 接口类型 %q 已登记为可用，但工厂里还没有对应实现", cfg.InterfaceType)
	}
}

// InterfaceTypes 返回当前支持的接口类型，供配置界面下拉框使用。
func InterfaceTypes() []string {
	// 返回副本：调用方若就地修改，不应污染包内的权威清单。
	return slices.Clone(supportedInterfaceTypes)
}

// SupportsInterfaceType 判断该接口类型当前是否真的能发起调用。
//
// 供配置保存时校验使用：与 NewProvider 共用同一份清单，
// 保证"界面能选的类型"和"系统跑得起来的类型"永远是同一个集合。
// 空串按 OpenAI 兼容处理，与 NewProvider 的宽容策略保持一致。
func SupportsInterfaceType(t string) bool {
	if t == "" {
		t = TypeOpenAI
	}
	return slices.Contains(supportedInterfaceTypes, t)
}
