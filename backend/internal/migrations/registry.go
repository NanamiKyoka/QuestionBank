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
	modelCapabilities,
	importTaskIdempotency,
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
	// 执行时基线只建清单内的表，模型多出来的表会在启动时被
	// CheckModelDrift 拦下来，而不是被顺手建掉。
	Tables: baselineTables(),

	Up: func(ctx context.Context, db *gorm.DB) error {
		// 只建**基线清单之内**的表。清单之外的模型说明作者还没写新迁移，
		// 在这里顺手建掉会让"缺了一条迁移"永远消失在账本里，
		// 而代码已经按"该表由新迁移创建"假设过了。
		if err := db.AutoMigrate(baselineModels()...); err != nil {
			return fmt.Errorf("建立基线表结构失败: %w", err)
		}
		return verifyBaselineTables(db)
	},
}

// modelCapabilities 让一个模型可以同时具备多种能力。
//
// 列名保持 model_type 不变、只放宽长度，是刻意的：
// 既有行存的是 "text" / "vision" / "embedding" 这样的单值，
// 在新的解析逻辑下**语义完全不变**——单值集合就是只具备那一项。
// 于是这条迁移不需要搬数据，SQLite 上甚至是空操作（它忽略 VARCHAR 长度），
// 真正需要 ALTER 的只有会严格执行长度的 MySQL。
//
// 改成位掩码或 JSON 数组看起来更"正规"，但两者都要搬数据，
// 而且位掩码的位序没有可读性，一次 UPDATE 就能把既有行的含义悄悄改掉。
var modelCapabilities = Migration{
	Version: 2,
	Name:    "model_capabilities",

	Body: `放宽 ai_models.model_type 的列宽（16 → 32），使其能存下能力集合
（如 "text,vision"）。既有行仍是 "text"/"vision"/"embedding" 单值，
在新解析逻辑下语义不变，因此无需搬数据。SQLite 忽略 VARCHAR 长度，脚本为空操作。`,

	SQL: map[string]string{
		DialectMySQL: `ALTER TABLE ai_models MODIFY COLUMN model_type VARCHAR(32) NOT NULL;`,
	},
	DownSQL: map[string]string{
		DialectMySQL: `ALTER TABLE ai_models MODIFY COLUMN model_type VARCHAR(16) NOT NULL;`,
	},

	NoDownReason: "",
}

// importTaskIdempotency 为既有库补上提交幂等所需的列与唯一索引。
//
// v1 基线是在幂等键上线前冻结的，v2 只改了 AI 模型能力列。因此已经执行过
// v1/v2 的数据库不会因为模型里新增 IdempotencyKey 而自动补列，而“仅录题”
// 的成功路径会自动提交：提交请求一旦带上 idempotency_key，查询就报
// “no such column”，前端只看到笼统的请求失败。
//
// 这里用 Migrator 先判断再加，而不是写 ALTER TABLE：
//   - 空库由 v1 基线直接建出完整字段，v3 必须成为空操作；
//   - 老库缺的是列，MySQL 与 SQLite 的 ALTER 语法又不相同；
//   - 手工修复过的库可能只有列没有唯一索引，索引也要校验一次。
var importTaskIdempotency = Migration{
	Version: 3,
	Name:    "import_task_idempotency_key",

	Body: `为 import_tasks 补齐 idempotency_key 列与 (user_id, idempotency_key) 唯一索引。
空库基线已含该结构，本迁移应成为空操作；v1/v2 老库则在这里补齐，
避免“仅录题”自动入库时因列缺失返回 500。`,

	Tables: []string{"import_tasks"},

	NoDownReason: "幂等键在回滚期间产生的重复提交无法安全辨认；删除该列会让后续重试重新具备重复入库风险。",

	Up: func(ctx context.Context, db *gorm.DB) error {
		const field = "IdempotencyKey"
		if !db.Migrator().HasColumn(&model.ImportTask{}, field) {
			if err := db.Migrator().AddColumn(&model.ImportTask{}, field); err != nil {
				return fmt.Errorf("补充 import_tasks.%s 失败: %w", field, err)
			}
		}
		if !db.Migrator().HasIndex(&model.ImportTask{}, "idx_import_user_idem") {
			if err := db.Migrator().CreateIndex(&model.ImportTask{}, "idx_import_user_idem"); err != nil {
				return fmt.Errorf("建立 import_tasks 幂等唯一索引失败: %w", err)
			}
		}
		return nil
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

// tableName 取一个模型的表名。
func tableName(m any) string {
	if namer, ok := m.(interface{ TableName() string }); ok {
		return namer.TableName()
	}
	// 理论上每个模型都实现了 TableName；真走到这里说明有人漏了，
	// 静默跳过只会让基线悄悄少一张表，所以这里明确失败。
	panic(fmt.Sprintf("模型 %T 没有实现 TableName()，无法登记到迁移账本", m))
}

// tableNamesOf 从模型列表里取出表名。
func tableNamesOf(models []any) []string {
	out := make([]string, 0, len(models))
	for _, m := range models {
		out = append(out, tableName(m))
	}
	return out
}

// baselineTableNames 基线覆盖的表名。
//
// 这是一份**手写冻结**的字面量，而不是 tableNamesOf(model.AllModels())。
// 从模型求值会让漂移守卫退化成恒真式：模型加一张表、清单跟着多一张，
// 于是"这张表还没有迁移"这件事永远不可能被发现——守卫形同虚设。
//
// 因此往这份清单里加表名，等于承认基线要为它负责，那正是它要防的事。
// 新表走自己的迁移，并在那条迁移的 Tables 里登记。
var baselineTableNames = []string{
	"activity_logs",
	"agent_runs",
	"agent_steps",
	"ai_models",
	"ai_providers",
	"chat_messages",
	"chat_sessions",
	"import_tasks",
	"knowledge_points",
	"paper_events",
	"paper_folders",
	"paper_nodes",
	"paper_versions",
	"papers",
	"prompt_templates",
	"question_knowledge_points",
	"question_tags",
	"questions",
	"review_logs",
	"subject_members",
	"subjects",
	"system_settings",
	"tag_categories",
	"tags",
	"users",
	"vector_docs",
}

// baselineTables 返回基线覆盖的表名清单。
func baselineTables() []string {
	out := make([]string, len(baselineTableNames))
	copy(out, baselineTableNames)
	sort.Strings(out)
	return out
}

// baselineModels 挑出基线清单覆盖的模型，保留模型注册顺序
// （被引用的表必须排在引用方之前，外键才不会建失败）。
func baselineModels() []any {
	in := make(map[string]bool, len(baselineTableNames))
	for _, n := range baselineTableNames {
		in[n] = true
	}
	out := make([]any, 0, len(in))
	for _, m := range model.AllModels() {
		if in[tableName(m)] {
			out = append(out, m)
		}
	}
	return out
}

// coveredTables 返回所有迁移（含基线）声明负责的表。
func coveredTables() map[string]bool {
	out := map[string]bool{}
	for _, m := range All() {
		for _, name := range m.Tables {
			out[name] = true
		}
	}
	return out
}

// tablesWithoutMigration 返回模型里有、却没有任何迁移声明负责的表。
func tablesWithoutMigration(models []any) []string {
	in := coveredTables()
	var extra []string
	for _, n := range tableNamesOf(models) {
		if !in[n] {
			extra = append(extra, n)
		}
	}
	sort.Strings(extra)
	return extra
}

// CheckModelDrift 校验每张业务表都有迁移负责。
//
// 与 AutoMigrate 的区别正是这条存在的理由：AutoMigrate 永远追得上模型，
// 于是"这个库是什么结构"无人负责，升级脚本也无处可写。
// 模型新增的表而没人写迁移时，这里在启动阶段就失败，
// 错误信息直接告诉作者该补哪条迁移。
func CheckModelDrift() error {
	extra := tablesWithoutMigration(model.AllModels())
	if len(extra) > 0 {
		return fmt.Errorf(
			"模型里有 %d 张表没有任何迁移负责（%v）：请为它们各写一条新迁移，并在该迁移的 Tables 里登记",
			len(extra), extra)
	}
	return nil
}
