package config

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestLoadAPI(t *testing.T) {
	clearAPIEnvironment(t)
	t.Setenv("GATEWAY_DATABASE_URL", "postgres://gateway:test@localhost/gateway")
	key := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
	t.Setenv("GATEWAY_API_KEY_ENCRYPTION_KEYS", `{"v1":"`+key+`"}`)
	t.Setenv("GATEWAY_API_KEY_ACTIVE_VERSION", "v1")
	config, err := LoadAPI()
	if err != nil {
		t.Fatalf("LoadAPI() error = %v", err)
	}
	if len(config.APIKeyEncryptionKeys["v1"]) != 32 || config.MaxRequestBodyBytes != 1<<20 ||
		config.AuthClockSkew != 5*time.Minute {
		t.Fatalf("LoadAPI() = %+v", config)
	}
}

func TestLoadAPIRejectsInvalidSecurityConfiguration(t *testing.T) {
	tests := []struct {
		name    string
		keys    string
		version string
	}{
		{name: "无密钥", keys: "", version: "v1"},
		{name: "密钥过短", keys: `{"v1":"a2V5"}`, version: "v1"},
		{name: "活动版本不存在", keys: `{"v1":"` + base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))) + `"}`, version: "v2"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clearAPIEnvironment(t)
			t.Setenv("GATEWAY_DATABASE_URL", "postgres://gateway:test@localhost/gateway")
			t.Setenv("GATEWAY_API_KEY_ENCRYPTION_KEYS", test.keys)
			t.Setenv("GATEWAY_API_KEY_ACTIVE_VERSION", test.version)
			if _, err := LoadAPI(); err == nil {
				t.Fatal("LoadAPI() 未拒绝无效配置")
			}
		})
	}
}

func TestLoadAPIRejectsExcessiveRequestLimits(t *testing.T) {
	for _, test := range []struct {
		name  string
		env   string
		value string
	}{
		{name: "请求体过大", env: "GATEWAY_API_MAX_REQUEST_BODY_BYTES", value: "16777217"},
		{name: "鉴权时间窗过大", env: "GATEWAY_API_AUTH_CLOCK_SKEW", value: "61m"},
	} {
		t.Run(test.name, func(t *testing.T) {
			clearAPIEnvironment(t)
			t.Setenv("GATEWAY_DATABASE_URL", "postgres://gateway:test@localhost/gateway")
			key := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
			t.Setenv("GATEWAY_API_KEY_ENCRYPTION_KEYS", `{"v1":"`+key+`"}`)
			t.Setenv("GATEWAY_API_KEY_ACTIVE_VERSION", "v1")
			t.Setenv(test.env, test.value)
			if _, err := LoadAPI(); err == nil {
				t.Fatal("LoadAPI() 未拒绝过大的安全配置")
			}
		})
	}
}

func clearAPIEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"GATEWAY_DATABASE_URL", "GATEWAY_API_KEY_ENCRYPTION_KEYS", "GATEWAY_API_KEY_ACTIVE_VERSION",
		"GATEWAY_API_MAX_REQUEST_BODY_BYTES", "GATEWAY_API_AUTH_CLOCK_SKEW",
	} {
		t.Setenv(key, "")
	}
}
