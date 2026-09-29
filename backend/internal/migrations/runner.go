package migrations

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"gorm.io/gorm"
)

// 账本表：记录哪些迁移已经执行过。
//
// 为什么单独一张表而不是查系统表反推：SQLite 与 MySQL 都没有一张
// "按执行顺序排好的 DDL 历史"表可以查，而"这个库现在是什么结构"
// 恰恰是必须能回答的问题。
const ledgerTable = "schema_migrations"

// ledgerRow 账本的一行。
type ledgerRow struct {
	Version   int       `gorm:"primaryKey;autoIncrement:false"`
	Name      string    `gorm:"size:128;not null"`
	AppliedAt time.Time `gorm:"not null"`
	// Checksum 记下执行当时这条迁移内容的哈希。
	// 改动一条**已经执行过**的迁移会让校验和对不上，runner 直接拒绝——
	// 静默跳过更危险：线上库停在旧结构，而代码以为自己是新的。
	Checksum string `gorm:"size:64;not null"`
}

func (ledgerRow) TableName() string { return ledgerTable }

// Status 一条迁移的当前状态，供 status 子命令输出。
type Status struct {
	Version   int
	Name      string
	Applied   bool
	AppliedAt time.Time
	// Dirty 为真表示账本里的校验和与当前代码里的不一致。
	// 这种情况下不要继续跑迁移，先弄清楚这条迁移为什么被改过。
	Dirty bool
}

// Runner 执行迁移。
type Runner struct {
	db      *gorm.DB
	dialect string
	all     []Migration
}

// NewRunner 构造 runner 并确保账本表存在。
func NewRunner(db *gorm.DB) (*Runner, error) {
	return NewRunnerWith(db, All())
}

// NewRunnerWith 用给定迁移列表构造 runner（测试用）。
func NewRunnerWith(db *gorm.DB, all []Migration) (*Runner, error) {
	r := &Runner{db: db, dialect: db.Dialector.Name(), all: all}
	if err := r.ensureLedger(); err != nil {
		return nil, err
	}
	return r, nil
}

// ensureLedger 建账本表。
func (r *Runner) ensureLedger() error {
	return r.db.AutoMigrate(&ledgerRow{})
}

func (r *Runner) ledger() (map[int]ledgerRow, error) {
	var rows []ledgerRow
	if err := r.db.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("读取迁移账本失败: %w", err)
	}
	out := make(map[int]ledgerRow, len(rows))
	for _, row := range rows {
		out[row.Version] = row
	}
	return out, nil
}

// Verify 校验所有已执行迁移的校验和。
//
// 任何一条对不上就整体拒绝：此时"继续跑"是危险的——
// 后续迁移建立在某个已经被改过的前置结构上，结果不可预期。
func (r *Runner) Verify(ctx context.Context) error {
	rows, err := r.ledger()
	if err != nil {
		return err
	}
	var problems []string
	for version, row := range rows {
		m := r.byVersion(version)
		if m == nil {
			// 账本里有当前代码里不存在的版本：多半是回退了代码。
			problems = append(problems, fmt.Sprintf(
				"版本 %04d(%s) 在当前代码里不存在，可能是部署了旧版本代码", version, row.Name))
			continue
		}
		if got := m.Checksum(); got != row.Checksum {
			problems = append(problems, fmt.Sprintf(
				"版本 %04d(%s) 的内容在执行后被改动过（账本 %s，当前 %s）",
				version, m.Name, short(row.Checksum), short(got)))
		}
	}
	if len(problems) > 0 {
		return &ChecksumError{Problems: problems}
	}
	return nil
}

// ChecksumError 校验和不一致。
type ChecksumError struct{ Problems []string }

func (e *ChecksumError) Error() string {
	return "迁移账本与当前代码不一致，已拒绝继续执行：\n  - " + joinLines(e.Problems)
}

// Up 执行到目标版本（<=0 表示执行全部）。
//
// 已经在账本里的迁移会被跳过；这是重跑不炸的关键。
func (r *Runner) Up(ctx context.Context, target int) ([]Migration, error) {
	if err := r.Verify(ctx); err != nil {
		return nil, err
	}
	rows, err := r.ledger()
	if err != nil {
		return nil, err
	}

	var applied []Migration
	for _, m := range r.all {
		if target > 0 && m.Version > target {
			break
		}
		if _, done := rows[m.Version]; done {
			continue
		}
		if err := r.apply(ctx, m); err != nil {
			return applied, fmt.Errorf("执行迁移 %s 失败: %w", m.Label(), err)
		}
		applied = append(applied, m)
	}
	return applied, nil
}

// apply 执行单条迁移并写入账本。
func (r *Runner) apply(ctx context.Context, m Migration) error {
	run := func(tx *gorm.DB) error {
		if err := r.runScript(tx, m.SQL, m.script(r.dialect), "up"); err != nil {
			return err
		}
		if m.Up != nil {
			if err := m.Up(ctx, tx); err != nil {
				return err
			}
		}
		return tx.Create(&ledgerRow{
			Version:   m.Version,
			Name:      m.Name,
			AppliedAt: time.Now().UTC(),
			Checksum:  m.Checksum(),
		}).Error
	}

	if m.NoTx {
		// 事务外的迁移只能靠自己保证不留半成品，因此必须显式承认这一点。
		slog.Warn("该迁移声明了 NoTx，执行过程不受事务保护",
			"version", m.Version, "name", m.Name, "reason", m.NoDownReason)
		return run(r.db)
	}
	return r.db.Transaction(run)
}

// Down 回滚最近 steps 条。
func (r *Runner) Down(ctx context.Context, steps int) error {
	if err := r.Verify(ctx); err != nil {
		return err
	}
	rows, err := r.ledger()
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	versions := make([]int, 0, len(rows))
	for v := range rows {
		versions = append(versions, v)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(versions)))

	for i := 0; i < steps && i < len(versions); i++ {
		m := r.byVersion(versions[i])
		if m == nil {
			return fmt.Errorf("版本 %04d 在当前代码里不存在，无法回滚", versions[i])
		}
		if m.Down == nil && m.downScript(r.dialect) == "" {
			reason := m.NoDownReason
			if reason == "" {
				reason = "这条迁移没有提供回滚脚本"
			}
			return fmt.Errorf("迁移 %s 不可回滚：%s", m.Label(), reason)
		}
		if err := r.rollback(ctx, *m); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) rollback(ctx context.Context, m Migration) error {
	run := func(tx *gorm.DB) error {
		if m.Down != nil {
			if err := m.Down(ctx, tx); err != nil {
				return err
			}
		}
		if err := r.runScript(tx, m.DownSQL, m.downScript(r.dialect), "down"); err != nil {
			return err
		}
		return tx.Where("version = ?", m.Version).Delete(&ledgerRow{}).Error
	}
	if m.NoTx {
		return run(r.db)
	}
	return r.db.Transaction(run)
}

// runScript 逐条执行脚本。
//
// 拆分语句用的是 migration.go 里的 splitStatements：它会跳过
// -- 注释行，并处理 BEGIN...END 这类块语句的嵌套。
func (r *Runner) runScript(tx *gorm.DB, scripts map[string]string, script, dir string) error {
	if script == "" {
		return nil
	}
	stmts, err := splitStatements(script)
	if err != nil {
		return fmt.Errorf("拆分 %s 脚本失败: %w", dir, err)
	}
	for i, s := range stmts {
		if err := tx.Exec(s).Error; err != nil {
			return fmt.Errorf("第 %d 条语句失败（%.60s…）: %w", i+1, s, err)
		}
	}
	return nil
}

// Status 报告每条迁移的状态。
func (r *Runner) Status(ctx context.Context) ([]Status, error) {
	rows, err := r.ledger()
	if err != nil {
		return nil, err
	}
	out := make([]Status, 0, len(r.all))
	for _, m := range r.all {
		st := Status{Version: m.Version, Name: m.Name}
		if row, ok := rows[m.Version]; ok {
			st.Applied = true
			st.AppliedAt = row.AppliedAt
			st.Dirty = row.Checksum != m.Checksum()
		}
		out = append(out, st)
	}
	// 账本里有、代码里没有的版本也要报出来，否则 status 会显得"一切正常"。
	known := map[int]bool{}
	for _, m := range r.all {
		known[m.Version] = true
	}
	extra := make([]int, 0)
	for v := range rows {
		if !known[v] {
			extra = append(extra, v)
		}
	}
	sort.Ints(extra)
	for _, v := range extra {
		out = append(out, Status{Version: v, Name: rows[v].Name, Applied: true, Dirty: true})
	}
	return out, nil
}

// Current 返回已执行的最大版本号；空账本返回 0。
func (r *Runner) Current(ctx context.Context) (int, error) {
	rows, err := r.ledger()
	if err != nil {
		return 0, err
	}
	max := 0
	for v := range rows {
		if v > max {
			max = v
		}
	}
	return max, nil
}

func (r *Runner) byVersion(v int) *Migration {
	for i := range r.all {
		if r.all[i].Version == v {
			return &r.all[i]
		}
	}
	return nil
}

// ErrNotInitialized 表示数据库还没有完成任何迁移。
var ErrNotInitialized = errors.New("数据库尚未初始化")

func short(s string) string {
	if len(s) <= 12 {
		return s
	}
	return s[:12]
}

func joinLines(items []string) string {
	out := ""
	for i, s := range items {
		if i > 0 {
			out += "\n  - "
		}
		out += s
	}
	return out
}
