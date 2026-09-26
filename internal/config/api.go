package config

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	defaultAPIMaxRequestBodyBytes int64 = 1 << 20
	maxAPIMaxRequestBodyBytes     int64 = 16 << 20
	defaultAPIAuthClockSkew             = 5 * time.Minute
)

// APIConfig 保存商户 HTTP API 的运行配置。
type APIConfig struct {
	Config
	DatabaseURL          string
	APIKeyEncryptionKeys map[string][]byte
	APIKeyActiveVersion  string
	MaxRequestBodyBytes  int64
	AuthClockSkew        time.Duration
}

// LoadAPI 加载商户 API 配置。加密密钥使用 JSON 对象，值为 32 字节密钥的 Base64。
func LoadAPI() (APIConfig, error) {
	base, err := Load()
	if err != nil {
		return APIConfig{}, err
	}
	databaseURL, err := requiredEnv("GATEWAY_DATABASE_URL")
	if err != nil {
		return APIConfig{}, err
	}
	keys, err := encryptionKeysFromEnv("GATEWAY_API_KEY_ENCRYPTION_KEYS")
	if err != nil {
		return APIConfig{}, err
	}
	activeVersion, err := requiredEnv("GATEWAY_API_KEY_ACTIVE_VERSION")
	if err != nil {
		return APIConfig{}, err
	}
	if _, exists := keys[activeVersion]; !exists {
		return APIConfig{}, fmt.Errorf("GATEWAY_API_KEY_ACTIVE_VERSION 不在密钥环中")
	}
	maxRequestBodyBytes := defaultAPIMaxRequestBodyBytes
	if value, err := positiveInt64Env("GATEWAY_API_MAX_REQUEST_BODY_BYTES", false); err != nil {
		return APIConfig{}, err
	} else if value > 0 {
		maxRequestBodyBytes = value
	}
	if maxRequestBodyBytes > maxAPIMaxRequestBodyBytes {
		return APIConfig{}, fmt.Errorf("GATEWAY_API_MAX_REQUEST_BODY_BYTES 不能超过 %d", maxAPIMaxRequestBodyBytes)
	}
	authClockSkew, err := durationFromEnv("GATEWAY_API_AUTH_CLOCK_SKEW", defaultAPIAuthClockSkew)
	if err != nil {
		return APIConfig{}, err
	}
	if authClockSkew > time.Hour {
		return APIConfig{}, fmt.Errorf("GATEWAY_API_AUTH_CLOCK_SKEW 不能超过 1h")
	}
	return APIConfig{
		Config: base, DatabaseURL: databaseURL, APIKeyEncryptionKeys: keys,
		APIKeyActiveVersion: activeVersion, MaxRequestBodyBytes: maxRequestBodyBytes,
		AuthClockSkew: authClockSkew,
	}, nil
}

func encryptionKeysFromEnv(name string) (map[string][]byte, error) {
	raw, err := requiredEnv(name)
	if err != nil {
		return nil, err
	}
	var encoded map[string]string
	if err := json.Unmarshal([]byte(raw), &encoded); err != nil || len(encoded) == 0 {
		return nil, fmt.Errorf("%s 必须是非空 JSON 对象", name)
	}
	keys := make(map[string][]byte, len(encoded))
	for version, value := range encoded {
		version = strings.TrimSpace(version)
		decoded, decodeErr := base64.StdEncoding.DecodeString(value)
		if version == "" || decodeErr != nil || len(decoded) != 32 {
			return nil, fmt.Errorf("配置 %s 中的密钥 %q 必须是 32 字节 Base64", name, version)
		}
		keys[version] = decoded
	}
	return keys, nil
}
