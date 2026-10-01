// Package db 负责数据库连接的建立、连接池调优与建表迁移。
//
// 双数据库支持：
//   - SQLite 用 github.com/glebarez/sqlite（基于 modernc.org/sqlite 的纯 Go 实现），
//     因此 CGO_ENABLED=0 也能编译，交叉编译到任何平台都不需要 C 工具链——
//     这是"单二进制交付"目标的前提。
//   - MySQL 用官方 gorm.io/driver/mysql。
package db

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
	"gorm.io/gorm/schema"

	"github.com/NanamiKyoka/questionmind/internal/config"
	"github.com/NanamiKyoka/questionmind/internal/migrations"
)

// sqliteMaxOpenConns SQLite 连接池上限。
//
// 刻意是一个很小的数字：SQLite 的并发瓶颈只在写，读本来就该放开；
// 这个值要解决的是"请求之间互相排队"，不是"要多大吞吐"。
const sqliteMaxOpenConns = 4

// Open 建立数据库连接。
func Open(cfg *config.Config) (*gorm.DB, error) {
	gormCfg := &gorm.Config{
		// 时间一律以 UTC 写入。这一步很关键：
		// 若用本地时间，跨时区部署或夏令时切换时会出现难以排查的排序错乱。
		NowFunc: func() time.Time { return time.Now().UTC() },
		NamingStrategy: schema.NamingStrategy{
			// 表名用复数（users 而非 user），与 migration SQL 保持一致。
			SingularTable: false,
		},
		Logger: newLogger(cfg),
		// 关闭默认事务可以提升性能，但会让批量写入失去原子性。
		// 这里保持开启：题库写入的正确性优先于这点性能。
		SkipDefaultTransaction: false,
	}

	var dialector gorm.Dialector
	switch cfg.Database.Driver {
	case "mysql":
		dialector = mysql.Open(cfg.Database.DSN)
	case "sqlite":
		dialector = sqlite.Open(cfg.Database.DSN)
	default:
		return nil, fmt.Errorf("不支持的数据库驱动: %s", cfg.Database.Driver)
	}

	gdb, err := gorm.Open(dialector, gormCfg)
	if err != nil {
		return nil, fmt.Errorf("连接数据库失败: %w", err)
	}

	sqlDB, err := gdb.DB()
	if err != nil {
		return nil, fmt.Errorf("获取底层连接池失败: %w", err)
	}

	if cfg.IsSQLite() {
		// SQLite 是"单写多读"模型（WAL + busy_timeout 已在 DSN 里开启，
		// 见 config.finalize）：读本来就不该和写互相阻塞。
		//
		// 曾经这里钉死 SetMaxOpenConns(1)，理由是"限制连接数能减少 SQLITE_BUSY"。
		// 代价却远大于收益——连接池只有一条连接，意味着**所有**请求共用它：
		// 一条慢查询或一个 SSE 长连接占住它，整个服务（题库、对话、向量检索、
		// 审计写入）一起排队。写冲突本来就由 busy_timeout 兜底，
		// 多开几条连接并不会让写更频繁地撞车，却能让读真正并发起来。
		//
		// 现在的取舍：
		//   - 上限取 sqliteMaxOpenConns（4）。SQLite 的并发瓶颈只在写，
		//     而写路径本身有事务与乐观锁保护；4 条连接足以让读请求排队之外
		//     还有余量，又不至于把文件锁的争抢放大到不可控。
		//   - 空闲连接数与上限相同，避免高并发后频繁重建连接
		//     （重建一次 SQLite 连接要重新解析 DSN 里的 pragma）。
		//   - **不设置** ConnMaxLifetime：SQLite 无网络往返，
		//     连接不会像 MySQL 那样被服务端断开，按期重建纯属无谓开销。
		// 若将来部署为多进程共享同一个 .db 文件，仍应先解决进程级互斥
		//（见 G18：双进程打开同一个 SQLite 文件），而不是继续调这个数字。
		sqlDB.SetMaxOpenConns(sqliteMaxOpenConns)
		sqlDB.SetMaxIdleConns(sqliteMaxOpenConns)
	} else {
		sqlDB.SetMaxOpenConns(cfg.Database.MaxOpenConns)
		sqlDB.SetMaxIdleConns(cfg.Database.MaxIdleConns)
		sqlDB.SetConnMaxLifetime(cfg.Database.ConnMaxLifetime)
	}

	if err := sqlDB.Ping(); err != nil {
		return nil, fmt.Errorf("数据库连通性检查失败: %w", err)
	}

	slog.Info("数据库已连接",
		"driver", cfg.Database.Driver,
		"dialect", gdb.Dialector.Name(),
	)
	return gdb, nil
}

// Migrate 把数据库结构推进到当前代码要求的版本。
//
// 这里是**唯一**的建表入口：以前是每次启动裸跑一次 AutoMigrate，
// 现在改为跑版本化迁移。行为上的差别不是"更严谨"这种说法，
// 而是可以回答三个以前无法回答的问题：
//
//   - 这个库现在是什么结构？（看 schema_migrations 账本）
//   - 它上次升级到哪一版、什么时候、谁跑的？
//   - 有人改动了一条已经执行过的迁移吗？（校验和对不上会拒绝启动）
//
// 对**既有部署**没有任何行为变化：基线迁移内部就是一次 AutoMigrate，
// 它是幂等的，老库会被平滑登记为 v1。
// 从此之后模型的任何结构变更都必须写成新迁移，AutoMigrate 不再被调用。
func Migrate(gdb *gorm.DB) error {
	runner, err := migrations.NewRunner(gdb)
	if err != nil {
		return fmt.Errorf("初始化迁移器失败: %w", err)
	}
	applied, err := runner.Up(context.Background(), 0)
	if err != nil {
		return fmt.Errorf("执行数据库迁移失败: %w", err)
	}
	if len(applied) == 0 {
		cur, err := runner.Current(context.Background())
		if err != nil {
			return fmt.Errorf("读取当前迁移版本失败: %w", err)
		}
		slog.Info("数据库结构已是最新", "version", cur)
		return nil
	}
	labels := make([]string, 0, len(applied))
	for _, m := range applied {
		labels = append(labels, m.Label())
	}
	slog.Info("已执行数据库迁移", "migrations", labels)
	return nil
}

// VerifySchema 在关闭自动迁移的部署上核对数据库结构。
//
// 只读，不建表也不写账本——这些部署明确要求启动路径不碰结构，
// 但并不接受"因此不校验版本"：那等于让进程对着任意 schema 服务。
func VerifySchema(gdb *gorm.DB) error {
	return migrations.CheckHead(gdb)
}

// Close 关闭连接。
func Close(gdb *gorm.DB) error {
	sqlDB, err := gdb.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// Health 供健康检查接口使用。
func Health(gdb *gorm.DB) error {
	sqlDB, err := gdb.DB()
	if err != nil {
		return err
	}
	return sqlDB.Ping()
}

// newLogger 按配置的日志级别决定 SQL 日志的详细程度。
func newLogger(cfg *config.Config) gormlogger.Interface {
	level := gormlogger.Warn
	switch cfg.Log.Level {
	case "debug":
		level = gormlogger.Info
	case "warn":
		level = gormlogger.Warn
	case "error":
		level = gormlogger.Error
	}
	return gormlogger.New(
		// GORM 的日志接口要求一个实现了 Printf 的对象；
		// 这里包一层标准库 log，输出到 stderr，与 slog 的应用日志分开。
		stdLogger{},
		gormlogger.Config{
			SlowThreshold:             500 * time.Millisecond,
			LogLevel:                  level,
			IgnoreRecordNotFoundError: true, // RecordNotFound 是正常业务分支，不该刷屏
			Colorful:                  false,
		},
	)
}

// stdLogger 适配 GORM 的日志输出。
type stdLogger struct{}

func (stdLogger) Printf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[gorm] "+format+"\n", args...)
}
