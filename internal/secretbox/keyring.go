// Package secretbox 提供带版本的应用层密钥加密能力。
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
)

var (
	ErrInvalidKeyring = errors.New("加密密钥环无效")
	ErrUnknownVersion = errors.New("加密密钥版本不存在")
	ErrInvalidSecret  = errors.New("待加密数据无效")
)

// Encrypted 是带密钥版本的认证密文。
type Encrypted struct {
	Ciphertext []byte
	Nonce      []byte
	Version    string
}

// Keyring 使用 AES-256-GCM 加密小型敏感数据。
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
		if _, exists := aeads[version]; exists {
			return nil, ErrInvalidKeyring
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			return nil, fmt.Errorf("创建 AES 加密器: %w", err)
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, fmt.Errorf("创建 GCM 加密器: %w", err)
		}
		aeads[version] = aead
	}
	if _, exists := aeads[activeVersion]; !exists {
		return nil, ErrInvalidKeyring
	}
	return &Keyring{activeVersion: activeVersion, aeads: aeads}, nil
}

// Encrypt 使用活动主密钥加密数据，并绑定调用方提供的附加认证数据。
func (keyring *Keyring) Encrypt(aad string, secret []byte) (Encrypted, error) {
	if strings.TrimSpace(aad) == "" || len(secret) < 16 {
		return Encrypted{}, ErrInvalidSecret
	}
	aead := keyring.aeads[keyring.activeVersion]
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return Encrypted{}, fmt.Errorf("生成 GCM nonce: %w", err)
	}
	ciphertext := aead.Seal(nil, nonce, secret, []byte(aad))
	return Encrypted{Ciphertext: ciphertext, Nonce: nonce, Version: keyring.activeVersion}, nil
}

// Decrypt 解密数据并校验附加认证数据。
func (keyring *Keyring) Decrypt(aad string, encrypted Encrypted) ([]byte, error) {
	aead, exists := keyring.aeads[encrypted.Version]
	if !exists {
		return nil, ErrUnknownVersion
	}
	if len(encrypted.Nonce) != aead.NonceSize() || len(encrypted.Ciphertext) < aead.Overhead() {
		return nil, ErrInvalidSecret
	}
	plaintext, err := aead.Open(nil, encrypted.Nonce, encrypted.Ciphertext, []byte(aad))
	if err != nil {
		return nil, fmt.Errorf("校验认证密文: %w", ErrInvalidSecret)
	}
	return plaintext, nil
}
