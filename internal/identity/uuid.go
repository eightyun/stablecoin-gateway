// Package identity 生成系统内部使用的不可预测标识。
package identity

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

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
