// Package identity 生成系统内部使用的不可预测标识。
package identity

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
)

var ErrInvalidTokenSize = errors.New("随机令牌长度无效")

// NewUUID 返回使用系统安全随机源生成的 UUID v4。
func NewUUID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("生成 UUID: %w", err)
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value)
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], nil
}

// NewToken 返回使用系统安全随机源生成的 URL-safe 随机令牌。
func NewToken(size int) (string, error) {
	if size < 16 || size > 128 {
		return "", ErrInvalidTokenSize
	}
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("生成随机令牌: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

// ValidUUID 判断字符串是否为规范的小写或大写 UUID 文本。
func ValidUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	compact := value[:8] + value[9:13] + value[14:18] + value[19:23] + value[24:]
	_, err := hex.DecodeString(compact)
	return err == nil
}
