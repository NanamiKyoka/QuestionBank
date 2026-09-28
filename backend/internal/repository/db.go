package repository

import (
	"context"
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// 事务在 Go 里最容易写歪的地方：service 需要跨多个 repository 保证原子性，
// 但 repository 又各自持有 *gorm.DB。这里用「事务放进 context」的方式解决——
// repository 每次取连接时先看 context 里有没有事务，有就用事务，没有就用默认连接。
//
// 好处：
//   - service 层完全看不到 gorm，却能组合出跨仓储的事务；
//   - repository 方法签名保持干净（不用到处传 tx 参数）；
//   - 忘记开事务时行为退化为"每条语句独立提交"，与不加事务的预期一致。
//
// 用法：
//
//	err := repos.Tx.Within(ctx, func(ctx context.Context) error {
//	    if err := repos.Questions.Create(ctx, q); err != nil { return err }
//	    return repos.Questions.SetTags(ctx, q.ID, tagIDs)
//	})

type txKey struct{}

// WithTx 把事务放入 context。
func WithTx(ctx context.Context, tx *gorm.DB) context.Context {
	return context.WithValue(ctx, txKey{}, tx)
}

// TxFrom 取出 context 中的事务，不存在返回 nil。
func TxFrom(ctx context.Context) *gorm.DB {
	if tx, ok := ctx.Value(txKey{}).(*gorm.DB); ok {
		return tx
	}
	return nil
}

// conn 返回本次操作应使用的连接：优先事务，其次仓储默认连接。
// 所有 repository 方法都必须通过它取连接，否则事务会失效。
func conn(ctx context.Context, fallback *gorm.DB) *gorm.DB {
	if tx := TxFrom(ctx); tx != nil {
		return tx.WithContext(ctx)
	}
	return fallback.WithContext(ctx)
}

// TxManager 事务管理器。由 router 注入各 service。
type TxManager struct {
	db *gorm.DB
}

// NewTxManager 构造。
func NewTxManager(db *gorm.DB) *TxManager { return &TxManager{db: db} }

// Within 在事务中执行 fn。
//
// 若 fn 返回 error 则回滚；若 fn 内部 panic 也会回滚并把 panic 继续抛出，
// 不会出现"panic 了但事务已提交"的半成品数据。
func (t *TxManager) Within(ctx context.Context, fn func(ctx context.Context) error) error {
	err := t.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(WithTx(ctx, tx))
	})
	if err != nil {
		return fmt.Errorf("事务执行失败: %w", err)
	}
	return nil
}

// DB 暴露底层连接，仅供需要方言判断等少数场景使用。service 层不应直接使用。
func (t *TxManager) DB() *gorm.DB { return t.db }

// escapeLike 转义 LIKE 通配符。
//
// 不转义的话，用户在搜索框里输入 "%" 就变成了"匹配一切"：
// 既是正确性问题（搜索结果不对），也是一条廉价的性能攻击路径（全表扫描）。
func escapeLike(s string) string {
	if !strings.ContainsAny(s, `%_\`) {
		return s
	}
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
