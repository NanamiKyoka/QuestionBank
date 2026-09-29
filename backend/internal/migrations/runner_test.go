package migrations

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	sqlite "github.com/glebarez/sqlite"

	"github.com/NanamiKyoka/questionmind/internal/model"
)

func newDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "m.db")), &gorm.Config{
		Logger: logger.Discard,
	})
	if err != nil {
		t.Fatalf("开测试库失败: %v", err)
	}
	if sqlDB, err := gdb.DB(); err == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	return gdb
}

// 空库跑一次迁移：表建起来，账本记下版本。
func TestUpOnEmptyDatabase(t *testing.T) {
	gdb := newDB(t)
	r, err := NewRunner(gdb)
	if err != nil {
		t.Fatalf("构造 runner 失败: %v", err)
	}
	applied, err := r.Up(context.Background(), 0)
	if err != nil {
		t.Fatalf("执行迁移失败: %v", err)
	}
	if len(applied) != 1 || applied[0].Version != 1 {
		t.Fatalf("应执行 1 条基线，实际 %d", len(applied))
	}
	if !gdb.Migrator().HasTable("questions") {
		t.Fatal("基线之后 questions 表应当存在")
	}
	if !gdb.Migrator().HasTable(ledgerTable) {
		t.Fatal("账本表应当存在")
	}
	cur, err := r.Current(context.Background())
	if err != nil || cur != 1 {
		t.Fatalf("当前版本应为 1，实际 %d err=%v", cur, err)
	}
}

// 重跑必须是空操作。这是"每次启动都能安全调用"的前提。
func TestUpIsIdempotent(t *testing.T) {
	gdb := newDB(t)
	r, err := NewRunner(gdb)
	if err != nil {
		t.Fatalf("构造 runner 失败: %v", err)
	}
	ctx := context.Background()
	if _, err := r.Up(ctx, 0); err != nil {
		t.Fatalf("首次迁移失败: %v", err)
	}
	applied, err := r.Up(ctx, 0)
	if err != nil {
		t.Fatalf("重跑不应失败: %v", err)
	}
	if len(applied) != 0 {
		t.Fatalf("重跑应无事可做，实际又执行了 %d 条", len(applied))
	}
}

// 既有部署的库已经被裸 AutoMigrate 建过表，缺的是账本。
// 这时必须**基线化**而不是去 CREATE TABLE 撞车。
func TestUpBaselinesAnAlreadyAutoMigratedDatabase(t *testing.T) {
	gdb := newDB(t)
	// 模拟老版本：启动时裸跑 AutoMigrate。
	if err := gdb.AutoMigrate(model.AllModels()...); err != nil {
		t.Fatalf("预置 AutoMigrate 失败: %v", err)
	}
	if gdb.Migrator().HasTable(ledgerTable) {
		t.Fatal("前置条件错误：老库里不该有账本表")
	}

	r, err := NewRunner(gdb)
	if err != nil {
		t.Fatalf("构造 runner 失败: %v", err)
	}
	ctx := context.Background()
	if _, err := r.Up(ctx, 0); err != nil {
		t.Fatalf("基线化失败（这类库必须能被平滑接管）: %v", err)
	}
	cur, err := r.Current(ctx)
	if err != nil || cur != 1 {
		t.Fatalf("基线化后当前版本应为 1，实际 %d err=%v", cur, err)
	}
}

// 改动一条已执行的迁移必须被拒绝，而不是静默跳过。
func TestUpRefusesWhenAppliedMigrationWasEdited(t *testing.T) {
	gdb := newDB(t)
	r, err := NewRunner(gdb)
	if err != nil {
		t.Fatalf("构造 runner 失败: %v", err)
	}
	ctx := context.Background()
	if _, err := r.Up(ctx, 0); err != nil {
		t.Fatalf("首次迁移失败: %v", err)
	}

	// 模拟"有人在 v1 之后改了它的内容"：直接改账本里的校验和。
	if err := gdb.Model(&ledgerRow{}).Where("version = ?", 1).
		Update("checksum", "deadbeefdeadbeef").Error; err != nil {
		t.Fatalf("篡改校验和失败: %v", err)
	}

	if _, err := r.Up(ctx, 0); err == nil {
		t.Fatal("账本与代码不一致时必须拒绝执行")
	}
	if err := r.Verify(ctx); err == nil {
		t.Fatal("Verify 应当报出不一致")
	}
}

// 账本里有当前代码里不存在的版本 = 部署了旧代码，必须报出来。
func TestStatusFlagsUnknownVersion(t *testing.T) {
	gdb := newDB(t)
	r, err := NewRunner(gdb)
	if err != nil {
		t.Fatalf("构造 runner 失败: %v", err)
	}
	ctx := context.Background()
	if _, err := r.Up(ctx, 0); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	if err := gdb.Create(&ledgerRow{
		Version: 99, Name: "from_the_future", AppliedAt: time.Now(), Checksum: "x",
	}).Error; err != nil {
		t.Fatalf("插入未来版本失败: %v", err)
	}

	sts, err := r.Status(ctx)
	if err != nil {
		t.Fatalf("读取状态失败: %v", err)
	}
	var flagged bool
	for _, s := range sts {
		if s.Version == 99 && s.Dirty {
			flagged = true
		}
	}
	if !flagged {
		t.Fatal("账本里多出来的版本必须在 status 里被标记")
	}
	if err := r.Verify(ctx); err == nil {
		t.Fatal("未知版本应当让 Verify 失败")
	}
}

// 基线必须冻结：模型长出新表而没人写迁移时，要报错而不是顺手建掉。
func TestBaselineRefusesToAbsorbNewTables(t *testing.T) {
	if got := tablesNotInBaseline(append(model.AllModels(), &ledgerRow{})); len(got) == 0 {
		t.Fatal("账本表不属于基线清单，应当被识别为多出来的表")
	}
	if extra := tablesNotInBaseline(model.AllModels()); len(extra) != 0 {
		t.Fatalf("当前模型应全部落在基线清单内，多出：%v", extra)
	}
}

// 版本号必须唯一且从 1 开始连续。
func TestRegistryIsOrderedAndUnique(t *testing.T) {
	list := All()
	if len(list) == 0 {
		t.Fatal("迁移表不能为空")
	}
	seen := map[int]bool{}
	for i, m := range list {
		if m.Version != i+1 {
			t.Fatalf("版本号必须从 1 起连续，第 %d 项是 %d", i, m.Version)
		}
		if seen[m.Version] {
			t.Fatalf("版本号重复：%d", m.Version)
		}
		seen[m.Version] = true
		if m.Name == "" {
			t.Fatalf("版本 %d 缺少名字", m.Version)
		}
		if m.Body == "" {
			t.Fatalf("版本 %d 缺少 Body：校验和靠它，改动必须伴随描述变化", m.Version)
		}
	}
}
