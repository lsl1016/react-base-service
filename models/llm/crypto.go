// Package model - 凭据静态加密：DB 中存储的 API Key 用 AES-256-GCM 加密落库。
//
// 密钥来源环境变量 LLM_SECRET_KEY（任意长度字符串，内部 sha256 派生 32 字节密钥）；
// 未配置时明文落库（兼容存量环境，行为与历史版本一致）。密文统一带 "enc:v1:" 前缀，
// 读取时按前缀识别：无前缀按存量明文原样返回，保证灰度迁移双向兼容。
package model

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"os"
	"strings"
	"sync"
)

// encryptedSecretPrefix 密文前缀；同时是版本标识（v1=AES-256-GCM）。
const encryptedSecretPrefix = "enc:v1:"

// secretKeyEnvName 存放加密主密钥的环境变量名。
const secretKeyEnvName = "LLM_SECRET_KEY"

var (
	secretCipherOnce  sync.Once
	secretCipherBlock cipher.Block
)

// secretBlock 惰性构建 AES 块；未配置 LLM_SECRET_KEY 时返回 nil（明文模式）。
func secretBlock() cipher.Block {
	secretCipherOnce.Do(func() {
		raw := strings.TrimSpace(os.Getenv(secretKeyEnvName))
		if raw == "" {
			return
		}
		key := sha256.Sum256([]byte(raw))
		block, err := aes.NewCipher(key[:])
		if err != nil {
			return
		}
		secretCipherBlock = block
	})
	return secretCipherBlock
}

// EncryptSecret 加密敏感字段落库；未配置主密钥时原样返回（明文模式）。
func EncryptSecret(plain string) string {
	plain = strings.TrimSpace(plain)
	if plain == "" {
		return ""
	}
	block := secretBlock()
	if block == nil {
		return plain
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return plain
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return plain
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plain), nil)
	return encryptedSecretPrefix + base64.StdEncoding.EncodeToString(sealed)
}

// DecryptSecret 还原敏感字段；非 enc:v1: 前缀（存量明文）原样返回。
func DecryptSecret(stored string) string {
	stored = strings.TrimSpace(stored)
	if !strings.HasPrefix(stored, encryptedSecretPrefix) {
		return stored
	}
	block := secretBlock()
	if block == nil {
		// 有密文但本进程未配置主密钥：无法解密，返回空避免把密文当明文 key 传给上游。
		return ""
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return ""
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(stored, encryptedSecretPrefix))
	if err != nil {
		return ""
	}
	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return ""
	}
	plain, err := gcm.Open(nil, data[:nonceSize], data[nonceSize:], nil)
	if err != nil {
		return ""
	}
	return string(plain)
}

// EncryptAPIKey 是 EncryptSecret 在用户模型 API Key 上的语义别名（便于调用方阅读）。
func EncryptAPIKey(plain string) string { return EncryptSecret(plain) }

// DecryptAPIKey 是 DecryptSecret 在用户模型 API Key 上的语义别名。
func DecryptAPIKey(stored string) string { return DecryptSecret(stored) }
