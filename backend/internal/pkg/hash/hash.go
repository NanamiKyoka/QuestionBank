// Package hash 封装密码哈希。
//
// 使用 bcrypt 而非 MD5/SHA：bcrypt 自带盐值且计算代价可调，
// 能显著抬高离线爆破的成本。
//
// cost 取 12（约 250ms/次）而不是库默认的 10：登录接口已经由
// RateLimit 限过流，多出来的这 200ms 用户感知不到；而对离线爆破者，
// 每个 hash 慢 2.5 倍等于同样时间里少猜 60% 的口令。
// 存量哈希不受影响——cost 编码在 hash 本身里，Verify 会照旧按它算。
package hash

import (
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// cost 代价因子。见包注释里为什么是 12 而不是 bcrypt.DefaultCost。
const cost = 12

// Password 生成密码哈希。
func Password(plain string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(plain), cost)
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
//
// 固定哈希自带 cost=10，比真实校验略快一点点；差值在毫秒级，
// 落在 bcrypt 自身的抖动里，不足以被分辨出来。
func DummyVerify() {
	// 一个固定的合法 bcrypt 哈希，仅用于消耗时间。
	const dummy = "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"
	_ = bcrypt.CompareHashAndPassword([]byte(dummy), []byte("dummy-password"))
}
