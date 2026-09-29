package ai

import (
	"strings"
	"testing"
)

// InterfaceTypes 与工厂必须给出一致的答案：清单里有的类型一定要能构造，
// 构造不了的类型一定不在清单里。界面下拉框、保存校验、工厂三者都读这份清单，
// 一旦分叉就会退回到"能选却存不下 / 存得下却用不了"的老问题。
func TestInterfaceTypesMatchFactory(t *testing.T) {
	types := InterfaceTypes()
	if len(types) == 0 {
		t.Fatal("至少要支持一种接口类型")
	}
	for _, typ := range types {
		if !SupportsInterfaceType(typ) {
			t.Fatalf("清单里的 %q 却不被 SupportsInterfaceType 认可", typ)
		}
		if _, err := NewProvider(Config{InterfaceType: typ, APIKey: "k"}); err != nil {
			t.Fatalf("清单里的 %q 竟然构造失败: %v", typ, err)
		}
	}
}

// 未实现的协议（如 Gemini 原生协议）必须在保存前就能被挡下，
// 且错误要说清现在支持什么——用户照着改才行。
func TestNewProviderRejectsUnsupportedType(t *testing.T) {
	_, err := NewProvider(Config{InterfaceType: TypeGemini, APIKey: "k"})
	if err == nil {
		t.Fatal("未实现的接口类型不应构造成功")
	}
	if SupportsInterfaceType(TypeGemini) {
		t.Fatal("未实现的接口类型不应被标为支持")
	}
	for _, want := range InterfaceTypes() {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("错误信息应说明当前可用的 %q，实际: %s", want, err.Error())
		}
	}
	if !strings.Contains(err.Error(), TypeGemini) {
		t.Fatalf("错误信息应回显被拒绝的类型，实际: %s", err.Error())
	}
}

func TestNewProviderRejectsUnknownType(t *testing.T) {
	_, err := NewProvider(Config{InterfaceType: "nope", APIKey: "k"})
	if err == nil {
		t.Fatal("未登记的类型不应构造成功")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Fatalf("错误信息应回显未知的类型，实际: %s", err.Error())
	}
}

// InterfaceTypes 返回副本：调用方就地改写不能污染包内的权威清单，
// 否则一次误改就会让保存校验与工厂行为分叉。
func TestInterfaceTypesReturnsCopy(t *testing.T) {
	got := InterfaceTypes()
	if len(got) == 0 {
		t.Fatal("清单不应为空")
	}
	got[0] = "mutated"

	if SupportsInterfaceType("mutated") {
		t.Fatal("调用方改写了返回值，污染了包内清单")
	}
	if _, err := NewProvider(Config{InterfaceType: "mutated", APIKey: "k"}); err == nil {
		t.Fatal("未登记的类型不应因为清单被污染而放行")
	}
}

// 空类型按 OpenAI 兼容处理，保存校验也要跟着认可，
// 否则"接口没填"这种合法状态会在保存时被莫名拒绝。
func TestEmptyTypeFallsBackToOpenAI(t *testing.T) {
	p, err := NewProvider(Config{APIKey: "k"})
	if err != nil {
		t.Fatalf("空类型不应报错: %v", err)
	}
	if _, ok := p.(*OpenAIProvider); !ok {
		t.Fatalf("空类型应构造出 OpenAI 实现，实际 %T", p)
	}
	if !SupportsInterfaceType("") {
		t.Fatal("空类型应被视为可用（等价于 openai）")
	}
}
