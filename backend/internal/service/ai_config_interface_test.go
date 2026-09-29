package service

import (
	"context"
	"strings"
	"testing"

	"github.com/NanamiKyoka/questionmind/internal/ai"
	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
	"github.com/NanamiKyoka/questionmind/internal/schema"
)

// 接口类型的权威清单在 ai 包里，界面下拉框读的是同一个函数，
// 这里校验的是"存不进去"——否则要等到第一次调用才发现协议不支持，
// 用户只会去怀疑自己的账号和额度。

// 创建时必须拒绝跑不了的协议，并把当前支持的协议列出来。
func TestCreateProviderRejectsUnsupportedInterfaceType(t *testing.T) {
	repos := newTestRepos(t)
	svc := newAIService(t, repos)
	ctx := context.Background()

	_, err := svc.CreateProvider(ctx, adminUser(), &schema.ProviderCreateRequest{
		Name: "Gemini", InterfaceType: ai.TypeGemini, APIKey: "sk-gemini-key",
	})
	if err == nil {
		t.Fatal("未实现的接口类型不应保存成功")
	}
	if !apperr.Is(err, apperr.CodeInvalidParam) {
		t.Fatalf("应为参数错误，实际: %v", err)
	}
	for _, want := range ai.InterfaceTypes() {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("错误信息应列出可用协议 %q，实际: %s", want, err.Error())
		}
	}

	// 拒绝的事不能落库。
	list, err := svc.ListProviders(ctx, adminUser())
	if err != nil {
		t.Fatalf("读取供应商列表失败: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("被拒绝的供应商不应入库，实际有 %d 条", len(list))
	}
}

// 更新时同样要拒绝：改协议是最容易被当成"顺手改一下"的操作。
func TestUpdateProviderRejectsUnsupportedInterfaceType(t *testing.T) {
	repos := newTestRepos(t)
	svc := newAIService(t, repos)
	ctx := context.Background()

	created := createProvider(t, svc, "OpenAI", "", "sk-original-key-abcdef")

	_, err := svc.UpdateProvider(ctx, adminUser(), created.ID, &schema.ProviderUpdateRequest{
		InterfaceType: strPtr(ai.TypeGemini),
	})
	if err == nil {
		t.Fatal("改成未实现的接口类型不应成功")
	}
	if !apperr.Is(err, apperr.CodeInvalidParam) {
		t.Fatalf("应为参数错误，实际: %v", err)
	}

	// 原记录不能被这次失败请求改坏。
	stored, err := repos.AIProviders.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("读取供应商失败: %v", err)
	}
	if string(stored.InterfaceType) != ai.TypeOpenAI {
		t.Fatalf("接口类型被失败的请求改成了 %q", stored.InterfaceType)
	}
}

// 清单里的类型照常保存，且不会被悄悄改写成别的值。
func TestCreateProviderAcceptsSupportedInterfaceType(t *testing.T) {
	repos := newTestRepos(t)
	svc := newAIService(t, repos)
	ctx := context.Background()

	for _, typ := range ai.InterfaceTypes() {
		res, err := svc.CreateProvider(ctx, adminUser(), &schema.ProviderCreateRequest{
			Name: "供应商-" + typ, InterfaceType: typ, APIKey: "sk-test-key-123456",
		})
		if err != nil {
			t.Fatalf("类型 %q 应能保存: %v", typ, err)
		}
		if res.InterfaceType != typ {
			t.Fatalf("保存后类型变成了 %q", res.InterfaceType)
		}
	}
}

// 历史数据里可能已经躺着跑不了的协议（界面曾经允许选）。
// 这种配置下就绪状态不能谎报"AI 功能已就绪"。
func TestActiveConfigNotReadyWhenProviderUnsupported(t *testing.T) {
	repos := newTestRepos(t)
	svc := newAIService(t, repos)
	ctx := context.Background()

	created := createProvider(t, svc, "OpenAI", "", "sk-secret-key-123456")
	if _, err := svc.CreateModel(ctx, adminUser(), &schema.ModelCreateRequest{
		ProviderID: created.ID, Name: "demo-model", ModelType: string(model.AIModelText),
	}); err != nil {
		t.Fatalf("创建模型失败: %v", err)
	}

	// 绕过校验，直接把协议改成未实现的类型，模拟历史数据。
	if err := repos.AIProviders.UpdateFields(ctx, created.ID,
		map[string]any{"interface_type": ai.TypeGemini}); err != nil {
		t.Fatalf("改写历史数据失败: %v", err)
	}

	resp, err := svc.GetActiveConfig(ctx, adminUser())
	if err != nil {
		t.Fatalf("读取生效配置失败: %v", err)
	}
	if resp.Ready {
		t.Fatal("供应商跑不起来时不应报已就绪")
	}
	if strings.Contains(resp.Message, "已就绪") {
		t.Fatalf("提示文案不应宣称已就绪，实际: %s", resp.Message)
	}

	// 真正调用时同样要给出可读原因，而不是让它一路崩下去。
	if _, err := svc.Resolve(ctx, model.AIModelText); err == nil {
		t.Fatal("不支持的协议不应被解析成可用的模型")
	}
}
