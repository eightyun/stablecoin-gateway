// Package merchantauth 提供商户 API Key 加密、签名验证和防重放能力。
package merchantauth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
)

var (
	ErrInvalidKeyring = errors.New("API Key 加密密钥环无效")
	ErrUnknownVersion = errors.New("API Key 加密密钥版本不存在")
	ErrInvalidSecret  = errors.New("API Secret 无效")
)

// EncryptedSecret 是加密后的 API Secret。
type EncryptedSecret struct {
	Ciphertext []byte
	Nonce      []byte
	Version    string
}

// Keyring 使用带版本的 AES-256-GCM 主密钥加密 API Secret。
type Keyring struct {
	activeVersion string
	aeads         map[string]cipher.AEAD
}

// NewKeyring 创建密钥环。每个主密钥必须恰好为 32 字节。
func NewKeyring(keys map[string][]byte, activeVersion string) (*Keyring, error) {
	activeVersion = strings.TrimSpace(activeVersion)
	if activeVersion == "" || len(keys) == 0 {
		return nil, ErrInvalidKeyring
	}
	aeads := make(map[string]cipher.AEAD, len(keys))
	for version, key := range keys {
		version = strings.TrimSpace(version)
		if version == "" || len(key) != 32 {
			return nil, ErrInvalidKeyring
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			return nil, fmt.Errorf("创建 API Key 加密器: %w", err)
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, fmt.Errorf("创建 API Key GCM: %w", err)
		}
		aeads[version] = aead
	}
	if _, exists := aeads[activeVersion]; !exists {
		return nil, ErrInvalidKeyring
	}
	return &Keyring{activeVersion: activeVersion, aeads: aeads}, nil
}

// Encrypt 使用当前主密钥加密 Secret，并将公开 Key ID 绑定为附加认证数据。
func (keyring *Keyring) Encrypt(keyID string, secret []byte) (EncryptedSecret, error) {
	if strings.TrimSpace(keyID) == "" || len(secret) < 16 {
		return EncryptedSecret{}, ErrInvalidSecret
	}
	aead := keyring.aeads[keyring.activeVersion]
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return EncryptedSecret{}, fmt.Errorf("生成 API Secret nonce: %w", err)
	}
	ciphertext := aead.Seal(nil, nonce, secret, []byte(keyID))
	return EncryptedSecret{Ciphertext: ciphertext, Nonce: nonce, Version: keyring.activeVersion}, nil
}

// Decrypt 解密并校验绑定到公开 Key ID 的 Secret。
func (keyring *Keyring) Decrypt(keyID string, encrypted EncryptedSecret) ([]byte, error) {
	aead, exists := keyring.aeads[encrypted.Version]
	if !exists {
		return nil, ErrUnknownVersion
	}
	if len(encrypted.Nonce) != aead.NonceSize() || len(encrypted.Ciphertext) < aead.Overhead() {
		return nil, ErrInvalidSecret
	}
	plaintext, err := aead.Open(nil, encrypted.Nonce, encrypted.Ciphertext, []byte(keyID))
	if err != nil {
		return nil, fmt.Errorf("校验 API Secret 密文: %w", ErrInvalidSecret)
	}
	return plaintext, nil
}
