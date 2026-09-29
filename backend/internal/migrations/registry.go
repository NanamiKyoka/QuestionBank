package migrations

import (
	"context"
	"fmt"
	"sort"

	"gorm.io/gorm"

	"github.com/NanamiKyoka/questionmind/internal/model"
)

// all 有序迁移表。
//
// 顺序即执行顺序。新迁移只能**追加**：已经发布过的版本号与内容都不许改，
// 改了会被校验和检测出来并拒绝启动。
var all = []Migration{
	baseline,
}

// All 返回迁移表的一份副本。
func All() []Migration {
	out := make([]Migration, len(all))
	copy(out, all)
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out
}

// baseline 表结构的基线。
//
// 它没有手写的 CREATE TABLE 脚本，而是调用 AutoMigrate 建表。原因很实际：
// 26 张表的 DDL 要写两遍（SQLite 与 MySQL 语法并不完全一致），
// 而且模型本来就在演进期，手抄一份 DDL 必然会与模型漂移。
// 代价是这条迁移不提供 down —— 基线之后的结构变更才由新迁移负责。
//
// 关键在于它把 AutoMigrate **收编进版本体系**了：
//
//   - 空库：建表 + 记 v1；
//   - 已有库（老版本靠 AutoMigrate 建过表）：AutoMigrate 是幂等的，
//     跑一遍什么也不会做，然后记 v1 —— 这就是"基线化"。
//
// 从此 AutoMigrate 不再在每次启动时裸跑，"这个库是什么结构"
// 有了唯一的、可回答的来源。
var baseline = Migration{
	Version: 1,
	Name:    "baseline",

	Body: `建立全部基础表结构。首次引入版本化迁移时执行。
既有部署的库在这里被"基线化"：AutoMigrate 幂等，不会重复建表，
只是把当前结构登记为 v1。此后不再有裸跑的 AutoMigrate。`,

	NoDownReason: "基线不可回滚：回滚它等于删掉全部业务数据。",

	// Tables 冻结基线所覆盖的表集合。
	//
	// 这张清单的作用是**挡住**"基线跟着模型一起变"这种偷懒：
	// 模型以后新增的表不由基线负责，必须写新迁移。
	// 执行时会核对，确保模型没在基线之后悄悄长出新表。
	Tables: baselineTables(),

	Up: func(ctx context.Context, db *gorm.DB) error {
		// 建**除基线清单之外**的表。
		//
		// 正常情况下这个集合是空的：模型若在基线之后新增了表，
		// 说明作者忘了写新迁移。此时必须报错而不是顺手建掉——
		// 顺手建掉会让"缺了一条迁移"这件事永远消失在账本里。
		extra := tablesNotInBaseline(model.AllModels())
		if len(extra) > 0 {
			return fmt.Errorf(
				"模型里有 %d 张表不属于基线清单（%v）：请为它们各写一条新迁移，不要靠基线创建",
				len(extra), extra)
		}
		if err := db.AutoMigrate(model.AllModels()...); err != nil {
			return fmt.Errorf("建立基线表结构失败: %w", err)
		}
		return verifyBaselineTables(db)
	},
}

// verifyBaselineTables 核对基线承诺的表确实都在。
func verifyBaselineTables(db *gorm.DB) error {
	var missing []string
	for _, name := range baselineTables() {
		if !db.Migrator().HasTable(name) {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("基线执行后仍缺少这些表：%v", missing)
	}
	return nil
}

// tableNamesOf 从模型列表里取出表名。
func tableNamesOf(models []any) []string {
	out := make([]string, 0, len(models))
	for _, m := range models {
		if namer, ok := m.(interface{ TableName() string }); ok {
			out = append(out, namer.TableName())
			continue
		}
		// 理论上每个模型都实现了 TableName；真走到这里说明有人漏了，
		// 静默跳过只会让基线悄悄少一张表，所以这里明确失败。
		panic(fmt.Sprintf("模型 %T 没有实现 TableName()，无法登记到基线", m))
	}
	return out
}

// baselineTables 返回基线覆盖的表名清单。
func baselineTables() []string {
	names := tableNamesOf(model.AllModels())
	sort.Strings(names)
	return names
}

// tablesNotInBaseline 返回模型里有、基线清单里没有的表。
func tablesNotInBaseline(models []any) []string {
	inBaseline := make(map[string]bool, 32)
	for _, n := range baselineTables() {
		inBaseline[n] = true
	}
	var extra []string
	for _, n := range tableNamesOf(models) {
		if !inBaseline[n] {
			extra = append(extra, n)
		}
	}
	sort.Strings(extra)
	return extra
}
