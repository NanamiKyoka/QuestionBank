package ai

import "fmt"

// NewProvider 按配置构造供应商实现。
//
// 目前只实现 OpenAI 兼容协议。这不是偷懒：DeepSeek、通义千问、智谱、
// 月之暗面、硅基流动等国内主流厂商都提供 OpenAI 兼容端点，
// 差异只在 base_url 与模型名，一个实现就能覆盖绝大多数使用场景。
// Gemini 的原生协议差异较大（请求体、函数调用格式、思维链签名都不一样），
// 留到需要时再补——与其写一个半成品，不如先明确不支持。
func NewProvider(cfg Config) (Provider, error) {
	switch cfg.InterfaceType {
	case TypeOpenAI, "":
		// 空类型按 OpenAI 兼容处理：这是最常见的期望，
		// 也避免用户没填类型时直接报错。
		return NewOpenAI(cfg), nil

	case TypeGemini:
		return nil, fmt.Errorf("ai: Gemini 原生协议尚未实现，" +
			"若该厂商提供 OpenAI 兼容端点，请把接口类型改为 openai 并填写对应的 base_url")

	default:
		return nil, fmt.Errorf("ai: 未知的供应商类型 %q", cfg.InterfaceType)
	}
}

// InterfaceTypes 返回当前支持的接口类型，供配置界面下拉框使用。
func InterfaceTypes() []string {
	return []string{TypeOpenAI}
}
