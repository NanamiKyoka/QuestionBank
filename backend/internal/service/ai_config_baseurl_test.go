package service

import (
	"context"
	"strings"
	"testing"

	"github.com/NanamiKyoka/questionmind/internal/ai"
	"github.com/NanamiKyoka/questionmind/internal/schema"
)

// 新建路径挡住非 http(s) 的网关地址：file:// 这类 scheme 一旦存进去，
// 要等到真正发起请求才炸，报错还很难懂。
func TestCreateProviderRejectsNonHTTPBaseURL(t *testing.T) {
	repos := newTestRepos(t)
	svc := newAIService(t, repos)

	for _, bad := range []string{
		"file:///etc/passwd",
		"ftp://mirror.example.com/v1",
		"api.openai.com/v1",
	} {
		_, err := svc.CreateProvider(context.Background(), adminUser(), &schema.ProviderCreateRequest{
			Name: "地址测试", InterfaceType: ai.TypeOpenAI, BaseURL: bad, APIKey: "sk-x",
		})
		if err == nil || !strings.Contains(err.Error(), "网关地址无效") {
			t.Fatalf("%q 应被拒绝，实际 %v", bad, err)
		}
	}
}

// 更新路径与新建同一套规则：指针字段上的 binding 对"指向空串"不可靠，
// 校验必须落在 service 层，否则整条更新链路就是漏的。
func TestUpdateProviderValidatesBaseURL(t *testing.T) {
	repos := newTestRepos(t)
	svc := newAIService(t, repos)
	ctx := context.Background()

	created := createProvider(t, svc, "OpenAI", "https://api.openai.com/v1", "sk-x")

	if _, err := svc.UpdateProvider(ctx, adminUser(), created.ID, &schema.ProviderUpdateRequest{
		BaseURL: strPtr("ftp://evil.example.com/v1"),
	}); err == nil || !strings.Contains(err.Error(), "网关地址无效") {
		t.Fatalf("非 http(s) 地址应被拒绝，实际 %v", err)
	}

	// 合法地址照常保存。
	if _, err := svc.UpdateProvider(ctx, adminUser(), created.ID, &schema.ProviderUpdateRequest{
		BaseURL: strPtr("http://127.0.0.1:11434/v1"),
	}); err != nil {
		t.Fatalf("合法地址应当通过: %v", err)
	}

	// 空串表示清空（回落到官方地址），不能被误判成非法地址。
	if _, err := svc.UpdateProvider(ctx, adminUser(), created.ID, &schema.ProviderUpdateRequest{
		BaseURL: strPtr(""),
	}); err != nil {
		t.Fatalf("清空地址应当通过: %v", err)
	}
}
