package service

import (
	"context"
	"testing"

	"github.com/NanamiKyoka/questionmind/internal/model"
	"github.com/NanamiKyoka/questionmind/internal/pkg/apperr"
)

func newSettingsService(t *testing.T) (*SettingsService, *Repos) {
	t.Helper()
	repos := newTestRepos(t)
	return NewSettingsService(repos), repos
}

// 目录里每一条都必须有唯一键与中文标签：重复键会让 settingIndex 静默覆盖，
// 后登记的那条在 List 里再也不出现。
func TestSettingCatalogIsWellFormed(t *testing.T) {
	seen := make(map[string]bool, len(settingCatalog))
	for _, def := range settingCatalog {
		if def.Key == "" || def.Label == "" {
			t.Fatalf("设置项缺少 key 或 label: %+v", def)
		}
		if seen[def.Key] {
			t.Fatalf("设置键重复: %s", def.Key)
		}
		seen[def.Key] = true
		if def.Note == "" {
			t.Fatalf("设置项 %s 缺少说明：宁可写清「改了不起作用」，也不要留空让人猜", def.Key)
		}
	}
}

// 可写的键必须真的有读取方，否则这个页面就在骗人。
// 这张表与 settingCatalog 一起构成契约：新增 Editable 的键时要同步更新它。
func TestOnlyConsumedKeysAreEditable(t *testing.T) {
	// 截至本次改动，全仓唯一被读取的非 AI 设置键。
	consumed := map[string]bool{
		model.SettingRegistrationOpen: true,
	}
	for _, def := range settingCatalog {
		if def.Editable != consumed[def.Key] {
			t.Fatalf("设置项 %s 的可写标记为 %v，但已知有读取方的键是 %v；"+
				"没有读取方的键必须保持只读", def.Key, def.Editable, consumed[def.Key])
		}
	}
}

func TestSettingsListRequiresSuperuser(t *testing.T) {
	svc, _ := newSettingsService(t)
	if _, err := svc.List(context.Background(), normalUser()); !apperr.Is(err, apperr.CodeForbidden) {
		t.Fatalf("普通用户应被拒绝，实际 %v", err)
	}
	if _, err := svc.List(context.Background(), nil); !apperr.Is(err, apperr.CodeUnauthorized) {
		t.Fatalf("空用户应返回未登录，实际 %v", err)
	}
	if _, err := svc.List(context.Background(), adminUser()); err != nil {
		t.Fatalf("管理员应能读取: %v", err)
	}
}

// 库里没有这一行时展示默认值，而不是空串——
// 否则界面会把"用默认值"和"管理员清空了"显示成同一个东西。
func TestSettingsListFallsBackToDefault(t *testing.T) {
	svc, _ := newSettingsService(t)
	items, err := svc.List(context.Background(), adminUser())
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}

	found := false
	for _, item := range items {
		if item.Key != model.SettingRegistrationOpen {
			continue
		}
		found = true
		if item.Saved {
			t.Fatal("数据库里还没有这一行，Saved 应为 false")
		}
		if item.Value != "false" || item.Def != "false" {
			t.Fatalf("应回落到默认值 false，实际 value=%q def=%q", item.Value, item.Def)
		}
	}
	if !found {
		t.Fatal("目录里缺少 registration_enabled")
	}
}

func TestSettingsUpdateWritesAndNormalizes(t *testing.T) {
	svc, repos := newSettingsService(t)
	ctx := context.Background()
	admin := adminUser()

	// "1" 必须落成 "true"：仓储 GetBool 认得 "1"，但页面读回的是 value，
	// 两种写法混在一张表里迟早有人读错。
	item, err := svc.Update(ctx, admin, model.SettingRegistrationOpen, " 1 ")
	if err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if item.Value != "true" {
		t.Fatalf("应规整为 true，实际 %q", item.Value)
	}
	if item.Saved != true {
		t.Fatal("写入后 Saved 应为 true")
	}
	if !repos.Settings.GetBool(ctx, model.SettingRegistrationOpen, false) {
		t.Fatal("仓储读回应为 true")
	}
	if item.UpdatedBy != admin.ID {
		t.Fatalf("updated_by 应为 %d，实际 %d", admin.ID, item.UpdatedBy)
	}

	// 非法取值必须被挡在落库之前。
	for _, bad := range []string{"", "yes please", "2", "null"} {
		if _, err := svc.Update(ctx, admin, model.SettingRegistrationOpen, bad); !apperr.Is(err, apperr.CodeInvalidParam) {
			t.Fatalf("取值 %q 应被拒绝，实际 %v", bad, err)
		}
	}
	// 上面的非法写入一个都不该落到表里。
	if got := repos.Settings.Get(ctx, model.SettingRegistrationOpen, ""); got != "true" {
		t.Fatalf("非法写入不应影响已保存的值，实际 %q", got)
	}
}

// 白名单是这一层唯一的防线：没有它，管理员可以把内部标记
// system_initialized 改成任意内容，把"系统初始化过没有"变得不可解释。
func TestSettingsUpdateRefusesNonWhitelistedKeys(t *testing.T) {
	svc, _ := newSettingsService(t)
	ctx := context.Background()
	admin := adminUser()

	// 目录内但只读
	if _, err := svc.Update(ctx, admin, model.SettingReviewThreshold, "3"); !apperr.Is(err, apperr.CodeForbidden) {
		t.Fatalf("只读键应返回 403，实际 %v", err)
	}
	if _, err := svc.Update(ctx, admin, setupMarkerKey, "1999-01-01T00:00:00Z"); !apperr.Is(err, apperr.CodeForbidden) {
		t.Fatalf("内部标记键应被拒绝，实际 %v", err)
	}
	// 目录外
	if _, err := svc.Update(ctx, admin, "made_up_key", "x"); !apperr.Is(err, apperr.CodeNotFound) {
		t.Fatalf("未知键应返回 404，实际 %v", err)
	}
	// AI 模型键由 /ai 组管理
	if _, err := svc.Update(ctx, admin, model.SettingAITextModelID, "7"); !apperr.Is(err, apperr.CodeForbidden) {
		t.Fatalf("AI 模型键应被拒绝，实际 %v", err)
	}
}

func TestSettingsUpdateRequiresSuperuser(t *testing.T) {
	svc, _ := newSettingsService(t)
	ctx := context.Background()
	if _, err := svc.Update(ctx, normalUser(), model.SettingRegistrationOpen, "true"); !apperr.Is(err, apperr.CodeForbidden) {
		t.Fatalf("普通用户应被拒绝，实际 %v", err)
	}
}

// 库里存在目录没登记的键时必须照实显示为只读：
// 藏起来会让人以为"设置就这几个"，而排查时最费时间的就是这种行。
func TestSettingsListSurfacesUnregisteredKeys(t *testing.T) {
	svc, repos := newSettingsService(t)
	ctx := context.Background()
	if err := repos.Settings.Set(ctx, "legacy_flag", "on", "历史遗留", 0); err != nil {
		t.Fatalf("预置数据失败: %v", err)
	}

	items, err := svc.List(ctx, adminUser())
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	var found bool
	for _, item := range items {
		if item.Key != "legacy_flag" {
			continue
		}
		found = true
		if item.Editable {
			t.Fatal("未登记的键必须是只读")
		}
		if item.Value != "on" || item.Description != "历史遗留" {
			t.Fatalf("未登记键应原样展示，实际 %+v", item)
		}
	}
	if !found {
		t.Fatal("未登记的键不应被隐藏")
	}
}

// ClaimOnce 写的初始化标记要能被看见，但绝不能被改。
// 这条同时守住"设置接口不能破坏首次初始化的并发保护"。
func TestSettingsListShowsSetupMarkerReadOnly(t *testing.T) {
	svc, repos := newSettingsService(t)
	ctx := context.Background()
	claimed, err := repos.Settings.ClaimOnce(ctx, setupMarkerKey, "2024-01-01T00:00:00Z")
	if err != nil || !claimed {
		t.Fatalf("预置初始化标记失败: claimed=%v err=%v", claimed, err)
	}

	items, err := svc.List(ctx, adminUser())
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	var found bool
	for _, item := range items {
		if item.Key == setupMarkerKey {
			found = true
			if item.Editable {
				t.Fatal("初始化标记必须只读")
			}
			if item.Value != "2024-01-01T00:00:00Z" {
				t.Fatalf("标记值应原样展示，实际 %q", item.Value)
			}
		}
	}
	if !found {
		t.Fatal("初始化标记应出现在列表里")
	}

	// 再抢一次仍然是 false：说明接口没把这行删掉或改坏。
	claimed, err = repos.Settings.ClaimOnce(ctx, setupMarkerKey, "2025-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("二次抢占失败: %v", err)
	}
	if claimed {
		t.Fatal("初始化标记仍在，ClaimOnce 应继续返回 false")
	}
}
