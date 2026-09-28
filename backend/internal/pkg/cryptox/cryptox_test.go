package cryptox

import (
	"errors"
	"strings"
	"testing"
)

func mustCipher(t *testing.T, secret string) *Cipher {
	t.Helper()
	c, err := NewCipher(secret)
	if err != nil {
		t.Fatalf("NewCipher 失败: %v", err)
	}
	return c
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	c := mustCipher(t, "test-secret")

	cases := []string{
		"sk-1234567890abcdef",
		"短",
		"包含中文与符号 !@#$%^&*()_+-=[]{}|;':\",./<>?",
		strings.Repeat("a", 4096),
	}
	for _, plain := range cases {
		enc, err := c.Encrypt(plain)
		if err != nil {
			t.Fatalf("加密 %q 失败: %v", plain, err)
		}
		if enc == plain {
			t.Fatalf("密文不应等于明文: %q", plain)
		}
		got, err := c.Decrypt(enc)
		if err != nil {
			t.Fatalf("解密失败: %v", err)
		}
		if got != plain {
			t.Fatalf("往返不一致: %q -> %q", plain, got)
		}
	}
}

// 同一明文两次加密必须得到不同密文（nonce 随机），
// 否则可以通过比对密文判断两个供应商是否用了同一把密钥。
func TestEncryptIsNonDeterministic(t *testing.T) {
	c := mustCipher(t, "s")
	a, _ := c.Encrypt("same-value")
	b, _ := c.Encrypt("same-value")
	if a == b {
		t.Fatal("相同明文两次加密应产生不同密文")
	}
}

func TestEncryptEmpty(t *testing.T) {
	c := mustCipher(t, "s")
	enc, err := c.Encrypt("")
	if err != nil || enc != "" {
		t.Fatalf("空值应原样返回空串，实际 %q %v", enc, err)
	}
	got, err := c.Decrypt("")
	if err != nil || got != "" {
		t.Fatalf("空密文应返回空串，实际 %q %v", got, err)
	}
}

// 换一把主密钥后旧密文必须解不开，而不是解出垃圾数据。
func TestDecryptWithWrongKeyFails(t *testing.T) {
	enc, _ := mustCipher(t, "secret-a").Encrypt("sk-real-key")

	_, err := mustCipher(t, "secret-b").Decrypt(enc)
	if !errors.Is(err, ErrInvalidCiphertext) {
		t.Fatalf("换密钥后应解密失败，实际 %v", err)
	}
}

// GCM 带认证标签：篡改密文必须被发现。
func TestDecryptDetectsTampering(t *testing.T) {
	c := mustCipher(t, "s")
	enc, _ := c.Encrypt("sk-1234567890")

	// 翻转 base64 中间的一个字符。
	b := []byte(enc)
	mid := len(b) / 2
	if b[mid] == 'A' {
		b[mid] = 'B'
	} else {
		b[mid] = 'A'
	}

	if _, err := c.Decrypt(string(b)); !errors.Is(err, ErrInvalidCiphertext) {
		t.Fatalf("篡改后的密文应解密失败，实际 %v", err)
	}
}

func TestDecryptRejectsGarbage(t *testing.T) {
	c := mustCipher(t, "s")
	for _, bad := range []string{"not-base64!!!", "YWJj", "===="} {
		if _, err := c.Decrypt(bad); !errors.Is(err, ErrInvalidCiphertext) {
			t.Fatalf("%q 应被拒绝，实际 %v", bad, err)
		}
	}
}

func TestNewCipherRejectsEmptySecret(t *testing.T) {
	for _, s := range []string{"", "   "} {
		if _, err := NewCipher(s); err == nil {
			t.Fatalf("空密钥 %q 应报错", s)
		}
	}
}

// 不同主密钥派生出不同密钥，即使原文只差一点。
func TestDifferentSecretsProduceDifferentKeys(t *testing.T) {
	a := mustCipher(t, "secret")
	b := mustCipher(t, "secret ")
	enc, _ := a.Encrypt("value")
	if _, err := b.Decrypt(enc); err == nil {
		t.Fatal("仅尾部空格的差异也应导致解密失败")
	}
}

func TestMask(t *testing.T) {
	cases := map[string]string{
		"":                    "",
		"abc":                 "***",
		"12345678":            "********",
		"sk-1234567890ab":     "sk******ab",
		"sk-abcdefghijklmnop": "sk-abc…mnop",
	}
	for in, want := range cases {
		if got := Mask(in); got != want {
			t.Fatalf("Mask(%q) = %q，期望 %q", in, got, want)
		}
	}
}

// 掩码必须能被识别出来，否则前端回传掩码会覆盖掉真实密钥——
// 这是参考实现里真实存在的隐患。
func TestIsMasked(t *testing.T) {
	masked := []string{"****", "sk-abc…mnop", "sk******ab", " *** "}
	for _, s := range masked {
		if !IsMasked(s) {
			t.Fatalf("%q 应被识别为掩码", s)
		}
	}
	real := []string{"", "sk-1234567890abcdef", "AIzaSyD-abcdefghijklmnop"}
	for _, s := range real {
		if IsMasked(s) {
			t.Fatalf("%q 不应被识别为掩码", s)
		}
	}
}

// 掩码后的值再加密解密也能往返（用于"未修改则沿用旧值"的场景）。
func TestMaskedValueSurvivesRoundTrip(t *testing.T) {
	c := mustCipher(t, "s")
	masked := Mask("sk-original-key-value")
	enc, err := c.Encrypt(masked)
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	got, err := c.Decrypt(enc)
	if err != nil || got != masked {
		t.Fatalf("往返失败: %q %v", got, err)
	}
}
