// Package jwtx 封装 JWT 的签发与校验。
//
// 刻意保持 Claims 精简：只放 user_id，不放角色。
// 理由是角色属于学科作用域、随时可能被管理员改动，一旦写进 token 就会出现
// "改了权限但旧 token 仍然生效"的安全窗口。每次请求都用 user_id 现查用户，
// 代价是一次带索引的主键查询，换来权限实时生效。
package jwtx

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// 对外暴露的错误语义，便于中间件区分"过期"与"无效"。
var (
	ErrExpired = errors.New("token 已过期")
	ErrInvalid = errors.New("token 无效")
)

// Claims 自定义声明。
type Claims struct {
	jwt.RegisteredClaims
}

// Manager 签发/校验器。
type Manager struct {
	secret []byte
	issuer string
	ttl    time.Duration
}

// NewManager 构造签发器。
func NewManager(secret, issuer string, ttl time.Duration) *Manager {
	return &Manager{secret: []byte(secret), issuer: issuer, ttl: ttl}
}

// TTL 返回令牌有效期，供接口返回给前端做倒计时。
func (m *Manager) TTL() time.Duration { return m.ttl }

// Generate 为指定用户签发访问令牌，同时返回过期时间。
func (m *Manager) Generate(userID uint) (string, time.Time, error) {
	now := time.Now().UTC()
	expires := now.Add(m.ttl)

	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   strconv.FormatUint(uint64(userID), 10),
			Issuer:    m.issuer,
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expires),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(m.secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("签发 token 失败: %w", err)
	}
	return signed, expires, nil
}

// Parse 校验令牌并取出用户 ID。
func (m *Manager) Parse(tokenString string) (uint, error) {
	claims := &Claims{}
	_, err := jwt.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (any, error) {
		// 必须校验签名算法，否则攻击者可把 alg 改成 none 绕过验签。
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("非预期的签名算法: %v", t.Header["alg"])
		}
		return m.secret, nil
	}, jwt.WithIssuer(m.issuer), jwt.WithExpirationRequired())

	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return 0, ErrExpired
		}
		return 0, ErrInvalid
	}

	id, err := strconv.ParseUint(claims.Subject, 10, 64)
	if err != nil || id == 0 {
		return 0, ErrInvalid
	}
	return uint(id), nil
}
