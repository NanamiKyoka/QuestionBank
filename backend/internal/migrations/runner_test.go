package migrations

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
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

// head 当前代码里的最高版本。
//
// 测试断言"最新版本"而不是写死 1：往迁移表里追加一条是常事，
// 而把版本号抄进每个用例只会让它们在下次追加时集体红掉。
func head() int {
	list := All()
	return list[len(list)-1].Version
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
	if len(applied) != len(All()) || applied[0].Version != 1 {
		t.Fatalf("应执行全部 %d 条迁移，实际 %d", len(All()), len(applied))
	}
	if !gdb.Migrator().HasTable("questions") {
		t.Fatal("基线之后 questions 表应当存在")
	}
	if !gdb.Migrator().HasTable(ledgerTable) {
		t.Fatal("账本表应当存在")
	}
	cur, err := r.Current(context.Background())
	if err != nil || cur != head() {
		t.Fatalf("当前版本应为 %d，实际 %d err=%v", head(), cur, err)
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
	if err != nil || cur != head() {
		t.Fatalf("基线化后当前版本应为 %d，实际 %d err=%v", head(), cur, err)
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

// 基线清单必须是冻结的字面量，并且与模型一一对应。
//
// 两条方向都要查：清单里有而模型没有的表建不出来；
// 模型有而清单没有的表则说明有人改了模型却没写迁移。
func TestBaselineListMatchesModels(t *testing.T) {
	inModels := map[string]bool{}
	for _, name := range tableNamesOf(model.AllModels()) {
		inModels[name] = true
	}
	for _, name := range baselineTables() {
		if !inModels[name] {
			t.Fatalf("基线清单里的 %s 没有任何模型，实现它的那条迁移一定会建不出这张表", name)
		}
	}
	if len(baselineTableNames) != len(baselineTables()) {
		t.Fatal("baselineTables 必须返回清单的完整副本")
	}
}

// 迁移漏写必须被发现，而不是被基线顺手建掉。
func TestModelDriftIsDetected(t *testing.T) {
	if err := CheckModelDrift(); err != nil {
		t.Fatalf("当前模型都应有迁移负责: %v", err)
	}
	// 账本表由 runner 自己建，不属于任何迁移 —— 拿它验证这条检查真的会响。
	if got := tablesWithoutMigration(append(model.AllModels(), &ledgerRow{})); len(got) == 0 {
		t.Fatal("没有迁移负责的表应当被报出来")
	}
}

// 基线只建清单内的表：模型多出来的表不会被顺手建掉。
func TestBaselineOnlyCreatesItsOwnTables(t *testing.T) {
	names := map[string]bool{}
	for _, m := range baselineModels() {
		names[tableName(m)] = true
	}
	if len(names) != len(baselineTableNames) {
		t.Fatalf("基线应覆盖全部 %d 张表，实际 %d 张", len(baselineTableNames), len(names))
	}
}

// 关闭自动迁移的部署靠它拿到版本校验。
func TestCheckHeadRejectsUnmigratedDatabase(t *testing.T) {
	gdb := newDB(t)
	if err := CheckHead(gdb); !errors.Is(err, ErrNotInitialized) {
		t.Fatalf("空库必须被拒绝，实际: %v", err)
	}

	// 只有账本、没有基线结构：版本落后于代码。
	if _, err := NewRunner(gdb); err != nil {
		t.Fatalf("构造 runner 失败: %v", err)
	}
	if err := gdb.Create(&ledgerRow{
		Version: 1, Name: All()[0].Name, AppliedAt: time.Now(), Checksum: "x",
	}).Error; err != nil {
		t.Fatalf("写入账本失败: %v", err)
	}
	if head() == 1 {
		t.Fatal("前置条件失效：只有一条迁移时，这个用例测不出版本落后")
	}
	if err := CheckHead(gdb); err == nil {
		t.Fatal("结构未建立时必须拒绝启动")
	}
}

func TestCheckHeadAcceptsFullyMigratedDatabase(t *testing.T) {
	gdb := newDB(t)
	r, err := NewRunner(gdb)
	if err != nil {
		t.Fatalf("构造 runner 失败: %v", err)
	}
	if _, err := r.Up(context.Background(), 0); err != nil {
		t.Fatalf("执行迁移失败: %v", err)
	}
	if err := CheckHead(gdb); err != nil {
		t.Fatalf("迁移到 head 的库应当通过校验: %v", err)
	}
}

// 账本说已迁移、表却不在，说明结构与账本对不上，必须拒绝服务。
func TestCheckHeadRejectsLedgerWithoutTables(t *testing.T) {
	gdb := newDB(t)
	r, err := NewRunner(gdb)
	if err != nil {
		t.Fatalf("构造 runner 失败: %v", err)
	}
	if _, err := r.Up(context.Background(), 0); err != nil {
		t.Fatalf("执行迁移失败: %v", err)
	}
	if err := gdb.Migrator().DropTable("questions"); err != nil {
		t.Fatalf("删除 questions 失败: %v", err)
	}
	if err := CheckHead(gdb); err == nil {
		t.Fatal("表缺失时必须拒绝启动")
	}
}

// 校验和不一致时 Verify 与 CheckHead 都要报出来，且顺序稳定。
func TestVerifyLedgerProblemsAreDeterministic(t *testing.T) {
	list := All()
	rows := map[int]ledgerRow{
		2: {Version: 2, Name: "future", Checksum: "x"},
		1: {Version: 1, Name: list[0].Name, Checksum: "deadbeef"},
	}
	first := verifyLedger(list, rows)
	second := verifyLedger(list, rows)
	if len(first) != 2 {
		t.Fatalf("两条问题都应报出，实际 %d: %v", len(first), first)
	}
	if strings.Join(first, "|") != strings.Join(second, "|") {
		t.Fatal("同一份坏账本必须给出同一份清单")
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
