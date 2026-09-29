package db

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/NanamiKyoka/questionmind/internal/config"
)

// 连接池只有一条连接时，任何一个慢查询或 SSE 长连接都会把整个服务堵死。
func TestOpenSQLiteUsesSmallSanePool(t *testing.T) {
	dir := t.TempDir()
	gdb, err := Open(&config.Config{
		App: config.AppConfig{DataDir: dir},
		Database: config.DatabaseConfig{
			Driver: "sqlite",
			DSN:    "file:" + filepath.Join(dir, "pool.db") + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)",
		},
		Log: config.LogConfig{Level: "error"},
	})
	if err != nil {
		t.Fatalf("打开 SQLite 失败: %v", err)
	}
	t.Cleanup(func() { _ = Close(gdb) })

	sqlDB, err := gdb.DB()
	if err != nil {
		t.Fatalf("取连接池失败: %v", err)
	}
	stats := sqlDB.Stats()
	if stats.MaxOpenConnections <= 1 {
		t.Fatalf("SQLite 连接池上限为 %d，读请求会被写请求整体串行化", stats.MaxOpenConnections)
	}
	if stats.MaxOpenConnections != sqliteMaxOpenConns {
		t.Fatalf("SQLite 连接池上限 = %d，期望 %d", stats.MaxOpenConnections, sqliteMaxOpenConns)
	}
}

// 回归测试：占住一条连接后，另一条查询必须还能立刻执行。
// 连接池被钉成 1 时这里必然超时——这正是"AI 流式对话期间题库接口一起卡住"的成因。
func TestSQLiteAllowsConcurrentQueries(t *testing.T) {
	dir := t.TempDir()
	gdb, err := Open(&config.Config{
		App: config.AppConfig{DataDir: dir},
		Database: config.DatabaseConfig{
			Driver: "sqlite",
			DSN:    "file:" + filepath.Join(dir, "conc.db") + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)",
		},
		Log: config.LogConfig{Level: "error"},
	})
	if err != nil {
		t.Fatalf("打开 SQLite 失败: %v", err)
	}
	t.Cleanup(func() { _ = Close(gdb) })

	sqlDB, err := gdb.DB()
	if err != nil {
		t.Fatalf("取连接池失败: %v", err)
	}
	ctx := context.Background()

	// 占住一条连接，模拟一个尚未结束的慢查询 / SSE 长连接。
	busy, err := sqlDB.Conn(ctx)
	if err != nil {
		t.Fatalf("取连接失败: %v", err)
	}
	defer busy.Close()

	done := make(chan error, 1)
	go func() {
		qctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		var n int
		done <- sqlDB.QueryRowContext(qctx, "SELECT COUNT(*) FROM sqlite_master").Scan(&n)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("并发查询失败: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("占住一条连接后其余查询全部阻塞：连接池被钉死为 1")
	}
}
