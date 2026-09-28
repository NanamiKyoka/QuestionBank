// Package hash 封装密码哈希。
//
// 使用 bcrypt 而非 MD5/SHA：bcrypt 自带盐值且计算代价可调，
// 能显著抬高离线爆破的成本。cost 取 bcrypt.DefaultCost(10)，
// 在"约 50ms/次"的安全性与登录延迟之间取平衡。
package hash

import (
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// Password 生成密码哈希。
func Password(plain string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("密码哈希失败: %w", err)
	}
	return string(b), nil
}

// Verify 校验密码。bcrypt 内部是恒定时间比较，不存在时序侧信道。
func Verify(hashed, plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hashed), []byte(plain)) == nil
}

// DummyVerify 在用户不存在时也执行一次等价耗时的哈希运算，
// 避免通过"响应时间差"判断用户名是否存在（用户枚举）。
func DummyVerify() {
	// 一个固定的合法 bcrypt 哈希，仅用于消耗时间。
	const dummy = "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"
	_ = bcrypt.CompareHashAndPassword([]byte(dummy), []byte("dummy-password"))
}
