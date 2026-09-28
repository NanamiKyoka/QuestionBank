// Package cryptox 提供敏感字段的对称加密。
//
// 用途：AI 供应商的 API Key 必须加密落库。明文存储意味着一次数据库泄露
// （备份文件、误提交的 db 文件）就等于把所有厂商的额度一起送出去。
//
// 算法选择 AES-256-GCM：带认证标签，密文被篡改会解密失败而不是解出乱码，
// 避免"改了密文导致程序拿到垃圾密钥继续请求"这种难以排查的情况。
package cryptox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ErrInvalidCiphertext 密文格式非法或已被篡改。
var ErrInvalidCiphertext = errors.New("cryptox: 密文无效")

// Cipher 加解密器。
type Cipher struct {
	aead cipher.AEAD
}

// NewCipher 由主密钥派生出加密器。
//
// 用 SHA-256 而不是直接用密钥原文：主密钥来自配置文件，长度不可控，
// 而 AES-256 要求恰好 32 字节。加盐前缀是为了让"字段加密"与
// "JWT 签名"即使共用同一个 secret 也不会互相削弱。
func NewCipher(secret string) (*Cipher, error) {
	if strings.TrimSpace(secret) == "" {
		return nil, errors.New("cryptox: 主密钥不能为空")
	}
	key := sha256.Sum256([]byte("questionmind:field-encryption:" + secret))

	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, fmt.Errorf("cryptox: 初始化 AES 失败: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("cryptox: 初始化 GCM 失败: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

// Encrypt 加密并返回 base64(nonce || ciphertext)。
func (c *Cipher) Encrypt(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("cryptox: 生成随机数失败: %w", err)
	}
	// nonce 直接拼在密文前面：GCM 要求每次加密使用不同的 nonce，
	// 随机生成后随密文一起存储是标准做法。
	sealed := c.aead.Seal(nonce, nonce, []byte(plain), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt 解密。
func (c *Cipher) Decrypt(encoded string) (string, error) {
	if encoded == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", ErrInvalidCiphertext
	}
	ns := c.aead.NonceSize()
	if len(raw) < ns {
		return "", ErrInvalidCiphertext
	}
	plain, err := c.aead.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		// 不把底层错误抛出去：它会泄露"密钥不对"还是"密文被改"的信息。
		return "", ErrInvalidCiphertext
	}
	return string(plain), nil
}

// Mask 生成用于界面展示的掩码。
//
// 只保留首尾少量字符：既能让管理员认出是哪把 Key，
// 又不至于因为界面上能看到完整值而泄露。
func Mask(plain string) string {
	s := strings.TrimSpace(plain)
	if s == "" {
		return ""
	}
	runes := []rune(s)
	switch {
	case len(runes) <= 8:
		return strings.Repeat("*", len(runes))
	case len(runes) <= 16:
		return string(runes[:2]) + strings.Repeat("*", 6) + string(runes[len(runes)-2:])
	default:
		return string(runes[:6]) + "…" + string(runes[len(runes)-4:])
	}
}

// IsMasked 判断一个值是否是掩码（用于"没改就沿用原值"的判断）。
//
// 前端编辑供应商时会把掩码原样回传，服务端必须能识别出
// "这不是新密钥，别拿它覆盖真实密钥"。
func IsMasked(v string) bool {
	s := strings.TrimSpace(v)
	if s == "" {
		return false
	}
	return strings.ContainsAny(s, "*…") ||
		strings.HasPrefix(s, "****")
}
