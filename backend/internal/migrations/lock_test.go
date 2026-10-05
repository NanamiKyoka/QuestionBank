package migrations

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

// 另一个副本正占着锁时，Up 必须直接拒绝，而不是把迁移再跑一遍。
func TestUpRefusesWhileAnotherRunnerHoldsTheLock(t *testing.T) {
	gdb := newDB(t)
	r, err := NewRunner(gdb)
	if err != nil {
		t.Fatalf("构造 runner 失败: %v", err)
	}
	ctx := context.Background()

	// 锁表与锁行由 runner 自己建，这里改写成"另一个副本正在跑迁移"。
	if err := r.ensureLockRow(); err != nil {
		t.Fatalf("建锁表失败: %v", err)
	}
	if err := gdb.Model(&lockRow{}).Where("id = ?", lockID).
		Updates(map[string]any{
			"owner":       "other-host:4242:0abc",
			"acquired_at": time.Now().UTC(),
		}).Error; err != nil {
		t.Fatalf("预置锁失败: %v", err)
	}

	if _, err := r.Up(ctx, 0); err == nil || !strings.Contains(err.Error(), "另一个进程") {
		t.Fatalf("锁被占用时应拒绝执行，实际 %v", err)
	}

	// 对方释放后就能继续。
	if err := gdb.Model(&lockRow{}).Where("id = ?", lockID).Update("owner", "").Error; err != nil {
		t.Fatalf("释放锁失败: %v", err)
	}
	if _, err := r.Up(ctx, 0); err != nil {
		t.Fatalf("锁释放后应当能执行: %v", err)
	}
}

// 持有者崩溃留下的锁不能永远挡住启动：超过 TTL 就该被接管。
func TestUpTakesOverStaleLock(t *testing.T) {
	gdb := newDB(t)
	r, err := NewRunner(gdb)
	if err != nil {
		t.Fatalf("构造 runner 失败: %v", err)
	}

	// 锁表与锁行由 runner 自己建，这里改写成一个崩溃副本留下的过期锁。
	if err := r.ensureLockRow(); err != nil {
		t.Fatalf("建锁表失败: %v", err)
	}
	if err := gdb.Model(&lockRow{}).Where("id = ?", lockID).
		Updates(map[string]any{
			"owner":       "crashed-host:1:0000",
			"acquired_at": time.Now().UTC().Add(-lockTTL - time.Minute),
		}).Error; err != nil {
		t.Fatalf("预置过期锁失败: %v", err)
	}

	if _, err := r.Up(context.Background(), 0); err != nil {
		t.Fatalf("过期的锁应当被接管，实际 %v", err)
	}
}

// 跑完必须释放锁，否则同一个副本的下一次启动会被自己挡住。
func TestUpReleasesLockAfterRun(t *testing.T) {
	gdb := newDB(t)
	r, err := NewRunner(gdb)
	if err != nil {
		t.Fatalf("构造 runner 失败: %v", err)
	}
	ctx := context.Background()

	if _, err := r.Up(ctx, 0); err != nil {
		t.Fatalf("执行迁移失败: %v", err)
	}
	var held lockRow
	if err := gdb.First(&held, lockID).Error; err != nil {
		t.Fatalf("读取锁行失败: %v", err)
	}
	if held.Owner != "" {
		t.Fatalf("迁移结束后锁应被释放，实际由 %q 持有", held.Owner)
	}
	// 幂等重跑的前提是能再次拿到锁。
	if _, err := r.Up(ctx, 0); err != nil {
		t.Fatalf("重跑应当能拿到锁: %v", err)
	}
}

// 锁必须覆盖整段临界区，而不只是抢在迁移之前：
// 在一条迁移的执行过程中，另一个副本同样抢不到锁。
func TestLockHoldsForWholeMigrationRun(t *testing.T) {
	gdb := newDB(t)

	var (
		runner   *Runner
		innerErr error
	)
	probe := Migration{
		Version: 1,
		Name:    "lock_probe",
		Body:    "测试用迁移：在执行期间尝试抢同一把锁",
		SQL:     map[string]string{dialectAny: "SELECT 1;"},
		// 必须脱离事务：钩子里要发起一次并发写（抢锁），
		// 而事务内同一个库的写会先撞上 SQLite 的锁，测不出"锁被占用"。
		NoTx: true,
		Down: func(context.Context, *gorm.DB) error {
			return nil
		},
		Up: func(ctx context.Context, _ *gorm.DB) error {
			// 同一时刻的第二个副本：必须被拒之门外。
			other, err := runner.lock(ctx)
			if err == nil {
				other.Release()
				innerErr = errors.New("迁移执行期间锁被抢走了")
				return nil
			}
			innerErr = err
			return nil
		},
	}

	var err error
	runner, err = NewRunnerWith(gdb, []Migration{probe})
	if err != nil {
		t.Fatalf("构造 runner 失败: %v", err)
	}
	if _, err := runner.Up(context.Background(), 0); err != nil {
		t.Fatalf("执行迁移失败: %v", err)
	}

	if innerErr == nil {
		t.Fatal("迁移执行期间应当拿不到第二把锁")
	}
	if !strings.Contains(innerErr.Error(), "另一个进程") {
		t.Fatalf("抢锁失败的信息应指向另一个进程，实际 %v", innerErr)
	}
}
