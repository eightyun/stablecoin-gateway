package config

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestLoadWebhookWorker(t *testing.T) {
	clearWebhookEnvironment(t)
	t.Setenv("GATEWAY_DATABASE_URL", "postgres://gateway:test@localhost/gateway")
	key := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("w", 32)))
	t.Setenv("GATEWAY_WEBHOOK_SECRET_ENCRYPTION_KEYS", `{"v1":"`+key+`"}`)
	t.Setenv("GATEWAY_WEBHOOK_SECRET_ACTIVE_VERSION", "v1")
	config, err := LoadWebhookWorker()
	if err != nil {
		t.Fatalf("LoadWebhookWorker() error = %v", err)
	}
	if config.BatchSize != 10 || config.MaxAttempts != 8 || config.RequestTimeout != 10*time.Second ||
		config.AllowPrivateNetworks || len(config.EncryptionKeys["v1"]) != 32 {
		t.Fatalf("LoadWebhookWorker() = %+v", config)
	}
}

func TestLoadWebhookWorkerRejectsUnsafeRelationships(t *testing.T) {
	for _, test := range []struct {
		name  string
		env   string
		value string
	}{
		{name: "租约短于操作", env: "GATEWAY_WEBHOOK_LEASE_DURATION", value: "30s"},
		{name: "退避倒置", env: "GATEWAY_WEBHOOK_MAX_BACKOFF", value: "1s"},
		{name: "响应体过大", env: "GATEWAY_WEBHOOK_MAX_RESPONSE_BODY_BYTES", value: "1048577"},
	} {
		t.Run(test.name, func(t *testing.T) {
			clearWebhookEnvironment(t)
			t.Setenv("GATEWAY_DATABASE_URL", "postgres://gateway:test@localhost/gateway")
			key := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("w", 32)))
			t.Setenv("GATEWAY_WEBHOOK_SECRET_ENCRYPTION_KEYS", `{"v1":"`+key+`"}`)
			t.Setenv("GATEWAY_WEBHOOK_SECRET_ACTIVE_VERSION", "v1")
			t.Setenv(test.env, test.value)
			if _, err := LoadWebhookWorker(); err == nil {
				t.Fatal("LoadWebhookWorker() 未拒绝无效配置")
			}
		})
	}
}

func clearWebhookEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"GATEWAY_DATABASE_URL", "GATEWAY_WEBHOOK_SECRET_ENCRYPTION_KEYS",
		"GATEWAY_WEBHOOK_SECRET_ACTIVE_VERSION", "GATEWAY_WEBHOOK_WORKER_ID",
		"GATEWAY_WEBHOOK_BATCH_SIZE", "GATEWAY_WEBHOOK_LEASE_DURATION",
		"GATEWAY_WEBHOOK_MAX_ATTEMPTS", "GATEWAY_WEBHOOK_BASE_BACKOFF",
		"GATEWAY_WEBHOOK_MAX_BACKOFF", "GATEWAY_WEBHOOK_OPERATION_TIMEOUT",
		"GATEWAY_WEBHOOK_IDLE_INTERVAL", "GATEWAY_WEBHOOK_RETRY_MIN",
		"GATEWAY_WEBHOOK_RETRY_MAX", "GATEWAY_WEBHOOK_REQUEST_TIMEOUT",
		"GATEWAY_WEBHOOK_MAX_RESPONSE_BODY_BYTES", "GATEWAY_WEBHOOK_ALLOW_PRIVATE_NETWORKS",
	} {
		t.Setenv(name, "")
	}
}
