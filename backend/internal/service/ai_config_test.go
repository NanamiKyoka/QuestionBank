package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/NanamiKyoka/questionmind/internal/ai"
	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
	"github.com/NanamiKyoka/questionmind/internal/pkg/cryptox"
	"github.com/NanamiKyoka/questionmind/internal/schema"
)

// newTestRepos 建一个真实（临时文件）SQLite 库并跑一遍迁移。
// 顺带验证了模型定义能被两种方言之一的 SQLite 正确建表。
func newTestRepos(t *testing.T) *Repos {
	t.Helper()
	dir := t.TempDir()
	gdb, err := gorm.Open(sqlite.Open(filepath.Join(dir, "test.db")), &gorm.Config{
		NowFunc: func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := gdb.AutoMigrate(model.AllModels()...); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	// 必须在 t.TempDir 的清理之前关闭连接，否则 Windows 上会因文件仍被
	// 占用而无法删除临时目录（cleanup 是后进先出，因此这里注册的会先跑）。
	t.Cleanup(func() {
		if sqlDB, err := gdb.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return NewRepos(gdb)
}

func newAIService(t *testing.T, repos *Repos) *AIConfigService {
	t.Helper()
	cipher, err := cryptox.NewCipher("test-secret-key")
	if err != nil {
		t.Fatalf("构造加密器失败: %v", err)
	}
	return NewAIConfigService(repos, cipher, 5*time.Second, 1)
}

func adminUser() *model.User {
	return &model.User{Base: model.Base{ID: 1}, Username: "admin", IsSuperuser: true, IsActive: true}
}

func normalUser() *model.User {
	return &model.User{Base: model.Base{ID: 2}, Username: "user", IsActive: true}
}

// fakeOpenAI 起一个假的 OpenAI 兼容端点，用于验证密钥解密与请求链路。
func fakeOpenAI(t *testing.T, gotAuth *string, gotModel *string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if gotAuth != nil {
			*gotAuth = r.Header.Get("Authorization")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"正常"}}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv
}

func createProvider(t *testing.T, svc *AIConfigService, name, baseURL, apiKey string) *schema.ProviderResponse {
	t.Helper()
	res, err := svc.CreateProvider(context.Background(), adminUser(), &schema.ProviderCreateRequest{
		Name: name, InterfaceType: ai.TypeOpenAI, BaseURL: baseURL, APIKey: apiKey,
	})
	if err != nil {
		t.Fatalf("创建供应商失败: %v", err)
	}
	return res
}

// ---- 供应商 ----

// 密钥必须加密存储，响应里只能出现掩码。
func TestCreateProviderEncryptsKey(t *testing.T) {
	repos := newTestRepos(t)
	svc := newAIService(t, repos)

	const secret = "sk-super-secret-value-123456"
	res := createProvider(t, svc, "OpenAI", "", secret)

	if res.HasKey != true {
		t.Fatal("应标记为已配置密钥")
	}
	if res.KeyHint == secret {
		t.Fatal("响应绝不能包含明文密钥")
	}
	if !strings.Contains(res.KeyHint, "…") && !strings.Contains(res.KeyHint, "*") {
		t.Fatalf("掩码格式异常: %q", res.KeyHint)
	}

	// 直接查库确认存的是密文。
	stored, err := repos.AIProviders.Get(context.Background(), res.ID)
	if err != nil {
		t.Fatalf("读取供应商失败: %v", err)
	}
	if stored.APIKeyEncrypted == secret {
		t.Fatal("数据库中不能存明文密钥")
	}
	if !strings.HasPrefix(stored.APIKeyEncrypted, "") || stored.APIKeyEncrypted == "" {
		t.Fatal("密文不应为空")
	}
}

// 前端把掩码原样回传时，绝不能拿它覆盖真实密钥。
func TestUpdateProviderIgnoresMaskedKey(t *testing.T) {
	repos := newTestRepos(t)
	svc := newAIService(t, repos)
	ctx := context.Background()

	const secret = "sk-original-key-abcdef"
	created := createProvider(t, svc, "OpenAI", "", secret)

	// 模拟前端回传掩码。
	if _, err := svc.UpdateProvider(ctx, adminUser(), created.ID, &schema.ProviderUpdateRequest{
		Name:   strPtr("改名后"),
		APIKey: &created.KeyHint,
	}); err != nil {
		t.Fatalf("更新失败: %v", err)
	}

	stored, _ := repos.AIProviders.Get(ctx, created.ID)
	cipher, _ := cryptox.NewCipher("test-secret-key")
	plain, err := cipher.Decrypt(stored.APIKeyEncrypted)
	if err != nil {
		t.Fatalf("解密失败: %v", err)
	}
	if plain != secret {
		t.Fatalf("掩码不应覆盖真实密钥，期望 %q，实际 %q", secret, plain)
	}
	if stored.Name != "改名后" {
		t.Fatal("其他字段应正常更新")
	}
}

func TestUpdateProviderReplacesKey(t *testing.T) {
	repos := newTestRepos(t)
	svc := newAIService(t, repos)
	ctx := context.Background()

	created := createProvider(t, svc, "OpenAI", "", "sk-old-key-000000")
	newKey := "sk-new-key-111111"
	updated, err := svc.UpdateProvider(ctx, adminUser(), created.ID, &schema.ProviderUpdateRequest{
		APIKey: &newKey,
	})
	if err != nil {
		t.Fatalf("更新失败: %v", err)
	}

	stored, _ := repos.AIProviders.Get(ctx, created.ID)
	cipher, _ := cryptox.NewCipher("test-secret-key")
	plain, _ := cipher.Decrypt(stored.APIKeyEncrypted)
	if plain != newKey {
		t.Fatalf("应替换为新密钥，实际 %q", plain)
	}
	if updated.KeyHint == created.KeyHint {
		t.Fatal("掩码应随密钥变化")
	}
}

func TestProviderRejectsDuplicateName(t *testing.T) {
	repos := newTestRepos(t)
	svc := newAIService(t, repos)
	createProvider(t, svc, "OpenAI", "", "sk-a")

	_, err := svc.CreateProvider(context.Background(), adminUser(), &schema.ProviderCreateRequest{
		Name: "OpenAI", InterfaceType: ai.TypeOpenAI, APIKey: "sk-b",
	})
	if !apperr.Is(err, apperr.CodeDuplicateName) {
		t.Fatalf("重名应返回 409，实际 %v", err)
	}
}

func TestProviderRequiresSuperuser(t *testing.T) {
	repos := newTestRepos(t)
	svc := newAIService(t, repos)

	_, err := svc.CreateProvider(context.Background(), normalUser(), &schema.ProviderCreateRequest{
		Name: "x", InterfaceType: ai.TypeOpenAI, APIKey: "k",
	})
	if !errors.Is(err, apperr.ErrForbidden) {
		t.Fatalf("普通用户应被拒绝，实际 %v", err)
	}
}

// ---- 模型与默认选择 ----

func TestCreateModelAutoSelectsFirst(t *testing.T) {
	repos := newTestRepos(t)
	svc := newAIService(t, repos)
	ctx := context.Background()

	p := createProvider(t, svc, "OpenAI", "", "sk-a")
	m, err := svc.CreateModel(ctx, adminUser(), &schema.ModelCreateRequest{
		ProviderID: p.ID, Name: "gpt-4o", ModelType: string(model.AIModelText),
	})
	if err != nil {
		t.Fatalf("创建模型失败: %v", err)
	}
	if !m.InUse {
		t.Fatal("第一个文本模型应被自动选为默认")
	}
	if m.DisplayName != "gpt-4o" {
		t.Fatalf("未指定显示名时应回落为模型名，实际 %q", m.DisplayName)
	}

	// 第二个不该抢占默认。
	m2, _ := svc.CreateModel(ctx, adminUser(), &schema.ModelCreateRequest{
		ProviderID: p.ID, Name: "gpt-4o-mini", ModelType: string(model.AIModelText),
	})
	if m2.InUse {
		t.Fatal("后续模型不应自动成为默认")
	}

	cfg, err := svc.GetActiveConfig(ctx, adminUser())
	if err != nil {
		t.Fatalf("取配置失败: %v", err)
	}
	if cfg.TextModelID != m.ID || !cfg.Ready {
		t.Fatalf("默认模型解析错误: %+v", cfg)
	}
}

// 把向量模型设成对话模型必须在写入前就被拦住。
func TestSetActiveConfigValidatesModelType(t *testing.T) {
	repos := newTestRepos(t)
	svc := newAIService(t, repos)
	ctx := context.Background()

	p := createProvider(t, svc, "OpenAI", "", "sk-a")
	emb, _ := svc.CreateModel(ctx, adminUser(), &schema.ModelCreateRequest{
		ProviderID: p.ID, Name: "text-embedding-3-small", ModelType: string(model.AIModelEmbedding),
	})

	_, err := svc.SetActiveConfig(ctx, adminUser(), &schema.SetActiveConfigRequest{
		TextModelID: &emb.ID,
	})
	if !apperr.Is(err, apperr.CodeInvalidParam) {
		t.Fatalf("用途不匹配应被拒绝，实际 %v", err)
	}
}

// 删除模型后必须清掉指向它的默认配置，否则 AI 功能会指向已删除的模型。
func TestDeleteModelClearsActiveConfig(t *testing.T) {
	repos := newTestRepos(t)
	svc := newAIService(t, repos)
	ctx := context.Background()

	p := createProvider(t, svc, "OpenAI", "", "sk-a")
	m, _ := svc.CreateModel(ctx, adminUser(), &schema.ModelCreateRequest{
		ProviderID: p.ID, Name: "gpt-4o", ModelType: string(model.AIModelText),
	})

	if err := svc.DeleteModel(ctx, adminUser(), m.ID); err != nil {
		t.Fatalf("删除模型失败: %v", err)
	}

	cfg, _ := svc.GetActiveConfig(ctx, adminUser())
	if cfg.TextModelID != 0 || cfg.Ready {
		t.Fatalf("删除模型后应清空默认配置: %+v", cfg)
	}
	if !strings.Contains(cfg.Message, "尚未配置") {
		t.Fatalf("应给出未配置提示，实际 %q", cfg.Message)
	}
}

func TestDeleteProviderCascades(t *testing.T) {
	repos := newTestRepos(t)
	svc := newAIService(t, repos)
	ctx := context.Background()

	p := createProvider(t, svc, "OpenAI", "", "sk-a")
	svc.CreateModel(ctx, adminUser(), &schema.ModelCreateRequest{
		ProviderID: p.ID, Name: "gpt-4o", ModelType: string(model.AIModelText),
	})

	if err := svc.DeleteProvider(ctx, adminUser(), p.ID); err != nil {
		t.Fatalf("删除供应商失败: %v", err)
	}

	cfg, _ := svc.GetActiveConfig(ctx, adminUser())
	if cfg.TextModelID != 0 {
		t.Fatal("删除供应商后应清空默认模型配置")
	}
	if list, _ := repos.AIModels.ListByProvider(ctx, p.ID); len(list) != 0 {
		t.Fatalf("供应商标下的模型应一并删除，剩余 %d 个", len(list))
	}
}

// ---- 解析 ----

func TestResolveGivesActionableErrors(t *testing.T) {
	repos := newTestRepos(t)
	svc := newAIService(t, repos)
	ctx := context.Background()

	_, err := svc.Resolve(ctx, model.AIModelText)
	if !apperr.Is(err, apperr.CodeAIUnavailable) {
		t.Fatalf("未配置时应返回 503 语义，实际 %v", err)
	}
	if !strings.Contains(err.Error(), "文本模型") {
		t.Fatalf("错误信息应说明缺什么: %v", err)
	}

	// 向量模型是可选的：未配置返回 nil 而不是错误。
	if rm := svc.ResolveOptional(ctx, model.AIModelEmbedding); rm != nil {
		t.Fatal("可选用途未配置时应返回 nil")
	}
}

// 端到端：从库里解密密钥 → 构造 provider → 真的发出请求。
func TestResolveDecryptsKeyAndCallsProvider(t *testing.T) {
	var gotAuth string
	srv := fakeOpenAI(t, &gotAuth, nil)

	repos := newTestRepos(t)
	svc := newAIService(t, repos)
	ctx := context.Background()

	p := createProvider(t, svc, "OpenAI", srv.URL, "sk-live-key-abcdef")
	svc.CreateModel(ctx, adminUser(), &schema.ModelCreateRequest{
		ProviderID: p.ID, Name: "gpt-4o", ModelType: string(model.AIModelText),
	})

	rm, err := svc.Resolve(ctx, model.AIModelText)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if rm.ModelName != "gpt-4o" || rm.ProviderName != "OpenAI" {
		t.Fatalf("解析结果错误: %+v", rm)
	}

	var got strings.Builder
	err = rm.Provider.ChatStream(ctx, ai.ChatRequest{
		Model:    rm.ModelName,
		Messages: []ai.Message{{Role: ai.RoleUser, Content: "hi"}},
	}, func(c ai.Chunk) error {
		got.WriteString(c.Text)
		return nil
	})
	if err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	if got.String() != "正常" {
		t.Fatalf("响应内容错误: %q", got.String())
	}
	// 关键：解出来的明文密钥被正确用于鉴权。
	if gotAuth != "Bearer sk-live-key-abcdef" {
		t.Fatalf("鉴权头错误: %q", gotAuth)
	}
}

func TestResolveRejectsInactiveModel(t *testing.T) {
	repos := newTestRepos(t)
	svc := newAIService(t, repos)
	ctx := context.Background()

	p := createProvider(t, svc, "OpenAI", "", "sk-a")
	m, _ := svc.CreateModel(ctx, adminUser(), &schema.ModelCreateRequest{
		ProviderID: p.ID, Name: "gpt-4o", ModelType: string(model.AIModelText),
	})

	inactive := false
	if _, err := svc.UpdateModel(ctx, adminUser(), m.ID, &schema.ModelUpdateRequest{IsActive: &inactive}); err != nil {
		t.Fatalf("禁用失败: %v", err)
	}

	_, err := svc.Resolve(ctx, model.AIModelText)
	if !apperr.Is(err, apperr.CodeAIUnavailable) {
		t.Fatalf("禁用模型应不可用，实际 %v", err)
	}
	if !strings.Contains(err.Error(), "禁用") {
		t.Fatalf("错误信息应说明原因: %v", err)
	}
}

// ---- 连通性测试 ----

func TestTestProviderSucceeds(t *testing.T) {
	srv := fakeOpenAI(t, nil, nil)

	repos := newTestRepos(t)
	svc := newAIService(t, repos)
	ctx := context.Background()

	p := createProvider(t, svc, "OpenAI", srv.URL, "sk-a")
	svc.CreateModel(ctx, adminUser(), &schema.ModelCreateRequest{
		ProviderID: p.ID, Name: "gpt-4o", ModelType: string(model.AIModelText),
	})

	res, err := svc.TestProvider(ctx, adminUser(), p.ID, &schema.ProviderTestRequest{})
	if err != nil {
		t.Fatalf("测试失败: %v", err)
	}
	if !res.OK {
		t.Fatalf("应测试成功，错误: %s", res.Error)
	}
	if res.Model != "gpt-4o" {
		t.Fatalf("应报告使用的模型，实际 %q", res.Model)
	}
	if res.Reply == "" {
		t.Fatal("应返回模型回复")
	}
}

// 鉴权失败要翻译成用户能看懂的话，而不是抛出原始英文错误。
func TestTestProviderHumanizesAuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"invalid api key"}}`)
	}))
	t.Cleanup(srv.Close)

	repos := newTestRepos(t)
	svc := newAIService(t, repos)
	ctx := context.Background()

	p := createProvider(t, svc, "Bad", srv.URL, "sk-wrong")
	svc.CreateModel(ctx, adminUser(), &schema.ModelCreateRequest{
		ProviderID: p.ID, Name: "m", ModelType: string(model.AIModelText),
	})

	res, err := svc.TestProvider(ctx, adminUser(), p.ID, &schema.ProviderTestRequest{})
	if err != nil {
		t.Fatalf("连通性测试本身不应报错: %v", err)
	}
	if res.OK {
		t.Fatal("应判定为失败")
	}
	if !strings.Contains(res.Error, "API Key") {
		t.Fatalf("应给出中文可读原因，实际 %q", res.Error)
	}
}

func TestTestProviderWithoutTextModel(t *testing.T) {
	repos := newTestRepos(t)
	svc := newAIService(t, repos)
	ctx := context.Background()

	p := createProvider(t, svc, "OpenAI", "", "sk-a")
	_, err := svc.TestProvider(ctx, adminUser(), p.ID, &schema.ProviderTestRequest{})
	if !apperr.Is(err, apperr.CodeUnprocessable) {
		t.Fatalf("没有文本模型时应有明确提示，实际 %v", err)
	}
}

func strPtr(s string) *string { return &s }
