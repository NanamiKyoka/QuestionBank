package migrations

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"time"
)

// 迁移互斥锁表。
//
// 为什么锁放在数据库里：Up 是"读账本 → 执行 → 写账本"，两个副本同时启动
// 会把同一条迁移各跑一遍（DDL 撞车、账本互相覆盖，中间状态不可预期）。
// 而副本可能跑在不同容器、不同主机上，它们共享的只有数据库——
// 文件锁和进程锁都覆盖不到那种部署。
const lockTable = "schema_migration_lock"

// lockID 锁表里唯一一行的主键。整表只有这一行。
const lockID = 1

// lockTTL 锁的最长持有时间。
//
// 正常路径下持有者跑完就释放；进程被杀时锁会留在库里，超过 TTL 的锁
// 视为可抢占——否则下一个副本会被一把没人释放的锁永久挡住。
const lockTTL = 5 * time.Minute

// lockRow 锁表的一行：owner 非空即"有人正在跑迁移"。
type lockRow struct {
	ID         int       `gorm:"primaryKey;autoIncrement:false"`
	Owner      string    `gorm:"size:64;not null"`
	AcquiredAt time.Time `gorm:"not null"`
}

func (lockRow) TableName() string { return lockTable }

// migrationLock 一次持锁。
type migrationLock struct {
	runner *Runner
	owner  string
}

// lock 抢迁移锁。
//
// 抢锁是一条带条件的 UPDATE（owner 为空、或已超过 TTL 才允许改写），
// 数据库保证同一条 UPDATE 只会让一个会话改到这一行——互斥就靠这一句，
// 不依赖"先读后写"，因此不存在检查与占用之间的窗口。
func (r *Runner) lock(ctx context.Context) (*migrationLock, error) {
	if err := r.ensureLockRow(); err != nil {
		return nil, err
	}

	owner := newLockOwner()
	now := time.Now().UTC()
	res := r.db.WithContext(ctx).Model(&lockRow{}).
		Where("id = ? AND (owner = '' OR acquired_at < ?)", lockID, now.Add(-lockTTL)).
		Updates(map[string]any{"owner": owner, "acquired_at": now})
	if res.Error != nil {
		return nil, fmt.Errorf("获取迁移锁失败: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		var held lockRow
		if err := r.db.WithContext(ctx).First(&held, lockID).Error; err != nil {
			return nil, fmt.Errorf("获取迁移锁失败: %w", err)
		}
		return nil, fmt.Errorf(
			"另一个进程正在执行迁移（%s，开始于 %s）：等它结束后再启动",
			held.Owner, held.AcquiredAt.Local().Format("2006-01-02 15:04:05"))
	}
	return &migrationLock{runner: r, owner: owner}, nil
}

// Touch 续期。逐条迁移之间调用，免得一条耗时较长的迁移把持有者
// 判成"已经崩溃"，锁被别的副本抢走。
func (l *migrationLock) Touch(ctx context.Context) {
	if err := l.runner.db.WithContext(ctx).Model(&lockRow{}).
		Where("id = ? AND owner = ?", lockID, l.owner).
		Update("acquired_at", time.Now().UTC()).Error; err != nil {
		slog.Warn("续迁移锁失败", "error", err)
	}
}

// Release 释放锁。
//
// 只释放自己那把：如果锁已经被 TTL 判定过期、被别的副本接管，
// 这条 WHERE 匹配不到任何行，不会把别人的锁放掉。
func (l *migrationLock) Release() {
	if err := l.runner.db.Model(&lockRow{}).
		Where("id = ? AND owner = ?", lockID, l.owner).
		Update("owner", "").Error; err != nil {
		slog.Warn("释放迁移锁失败", "error", err)
	}
}

// ensureLockRow 确保锁表与锁行存在。
//
// 两个副本可能同时走到这里：谁先建成建好都行，输掉的那个重查一次即可。
// 这是与账本表同级别的 runner 基础设施，因此同样由 runner 自己建，
// 而不进版本化迁移（否则"建锁表"本身就要先抢锁）。
func (r *Runner) ensureLockRow() error {
	if !r.db.Migrator().HasTable(&lockRow{}) {
		if err := r.db.AutoMigrate(&lockRow{}); err != nil && !r.db.Migrator().HasTable(&lockRow{}) {
			return fmt.Errorf("建迁移锁表失败: %w", err)
		}
	}

	var n int64
	if err := r.db.Model(&lockRow{}).Where("id = ?", lockID).Count(&n).Error; err != nil {
		return fmt.Errorf("读迁移锁失败: %w", err)
	}
	if n > 0 {
		return nil
	}
	if err := r.db.Create(&lockRow{ID: lockID, Owner: "", AcquiredAt: time.Now().UTC()}).Error; err != nil {
		// 并发初始化时另一个副本先把这一行建好了。
		if retryErr := r.db.Model(&lockRow{}).Where("id = ?", lockID).Count(&n).Error; retryErr == nil && n > 0 {
			return nil
		}
		return fmt.Errorf("初始化迁移锁失败: %w", err)
	}
	return nil
}

// newLockOwner 生成持有者标识：主机名 + 进程号 + 随机后缀，
// 出问题时能直接告诉运维是哪个副本占着锁。
func newLockOwner() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown"
	}
	if len(host) > 32 {
		host = host[:32]
	}
	return fmt.Sprintf("%s:%d:%04x", host, os.Getpid(), rand.Intn(1<<16))
}
