package model

import (
	"os"
	"strings"
	"sync"
	"testing"
)

// 配置主密钥后加解密往返一致，且密文带 enc:v1: 前缀。
func TestEncryptDecryptSecretRoundTrip(t *testing.T) {
	t.Setenv("LLM_SECRET_KEY", "unit-test-master-secret")

	plain := "unit-test-api-key-0123456789"
	sealed := EncryptAPIKey(plain)
	if sealed == plain {
		t.Fatalf("expected ciphertext, got plaintext")
	}
	if !strings.HasPrefix(sealed, encryptedSecretPrefix) {
		t.Fatalf("ciphertext missing prefix: %q", sealed)
	}
	if got := DecryptAPIKey(sealed); got != plain {
		t.Fatalf("round trip = %q, want %q", got, plain)
	}

	// 同明文两次加密 nonce 不同，密文不同（防重放/对比攻击面）。
	if again := EncryptAPIKey(plain); again == sealed {
		t.Fatalf("expected per-call nonce, got identical ciphertext")
	}
}

// 未配置主密钥时保持明文兼容（存量环境行为不变）。
func TestEncryptSecretPlaintextFallback(t *testing.T) {
	os.Unsetenv("LLM_SECRET_KEY")
	resetSecretCipherForTest()

	plain := "legacy-plain-key"
	if got := EncryptAPIKey(plain); got != plain {
		t.Fatalf("plaintext mode should passthrough, got %q", got)
	}
	if got := DecryptAPIKey(plain); got != plain {
		t.Fatalf("legacy plaintext should passthrough, got %q", got)
	}
}

// 存量明文行在配置主密钥后仍可读取（灰度迁移兼容）。
func TestDecryptSecretLegacyPlaintext(t *testing.T) {
	t.Setenv("LLM_SECRET_KEY", "unit-test-master-secret")
	if got := DecryptAPIKey("legacy-plain-key"); got != "legacy-plain-key" {
		t.Fatalf("legacy plaintext = %q, want original", got)
	}
}

// 有密文但本进程没有主密钥：返回空串而不是把密文当明文外发。
func TestDecryptSecretWithoutKey(t *testing.T) {
	t.Setenv("LLM_SECRET_KEY", "unit-test-master-secret")
	resetSecretCipherForTest() // 先归零再加密，确保生成了真实密文
	sealed := EncryptAPIKey("some-secret")
	if !strings.HasPrefix(sealed, encryptedSecretPrefix) {
		t.Fatalf("expected ciphertext before unset, got %q", sealed)
	}
	os.Unsetenv("LLM_SECRET_KEY")
	resetSecretCipherForTest()
	if got := DecryptAPIKey(sealed); got != "" {
		t.Fatalf("ciphertext without key should decrypt to empty, got %q", got)
	}
}

// resetSecretCipherForTest 重置惰性初始化状态（变量同包可见；仅测试专用，非并发场景）。
func resetSecretCipherForTest() {
	secretCipherOnce = sync.Once{}
	secretCipherBlock = nil
}

// 坏密文（截断/非 base64）不 panic，返回空串。
func TestDecryptSecretCorruptInput(t *testing.T) {
	t.Setenv("LLM_SECRET_KEY", "unit-test-master-secret")
	if got := DecryptAPIKey(encryptedSecretPrefix + "not-base64!!!"); got != "" {
		t.Fatalf("corrupt ciphertext should return empty, got %q", got)
	}
	if got := DecryptAPIKey(encryptedSecretPrefix + "QUJD"); got != "" {
		t.Fatalf("truncated ciphertext should return empty, got %q", got)
	}
}
