package service

import (
	"context"
	"sync/atomic"
)

// SetupState 记录"系统是否还没初始化"。
//
// 为什么缓存而不是每次请求都查一次用户表：
// 这个判断会挂在**每一个** API 请求上（未初始化时统一返回 503）。
// 每请求一次 COUNT(*) 既浪费又会在启动瞬间堆积。
//
// 一致性策略：
//   - 进程启动时刷新一次；
//   - 初始化向导成功后立即置为 false；
//   - 对外只读，因此用 atomic 即可，不需要锁。
type SetupState struct {
	required atomic.Bool
}

// NewSetupState 构造，默认认为需要初始化（fail-safe）。
func NewSetupState() *SetupState {
	s := &SetupState{}
	s.required.Store(true)
	return s
}

// Required 是否仍处于未初始化状态。
func (s *SetupState) Required() bool { return s.required.Load() }

// Set 显式设置状态。
func (s *SetupState) Set(required bool) { s.required.Store(required) }

// Refresh 从数据库重新判定。
func (s *SetupState) Refresh(ctx context.Context, users interface {
	Count(context.Context) (int64, error)
}) error {
	n, err := users.Count(ctx)
	if err != nil {
		// 判定失败时保守处理：保持"需要初始化"，避免在数据库异常时
		// 误把系统当成已初始化而放行请求。
		s.required.Store(true)
		return err
	}
	s.required.Store(n == 0)
	return nil
}
