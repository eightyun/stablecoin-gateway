package config

import (
	"testing"
	"time"
)

func TestLoadDepositWorker(t *testing.T) {
	clearDepositWorkerEnvironment(t)
	t.Setenv("GATEWAY_DATABASE_URL", "postgres://gateway:test@localhost/gateway")
	config, err := LoadDepositWorker()
	if err != nil {
		t.Fatalf("LoadDepositWorker() error = %v", err)
	}
	if config.OperationTimeout != 10*time.Second || config.IdleInterval != time.Second ||
		config.ExpireInterval != 30*time.Second || config.ExpireBatchSize != 100 {
		t.Fatalf("LoadDepositWorker() = %+v", config)
	}
}

func TestLoadDepositWorkerRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "缺少数据库", key: "GATEWAY_DATABASE_URL", value: ""},
		{name: "任务超时无效", key: "GATEWAY_DEPOSIT_OPERATION_TIMEOUT", value: "0s"},
		{name: "批次过大", key: "GATEWAY_DEPOSIT_EXPIRE_BATCH_SIZE", value: "1001"},
		{name: "退避上限过小", key: "GATEWAY_DEPOSIT_RETRY_MAX", value: "500ms"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clearDepositWorkerEnvironment(t)
			t.Setenv("GATEWAY_DATABASE_URL", "postgres://gateway:test@localhost/gateway")
			t.Setenv(test.key, test.value)
			if _, err := LoadDepositWorker(); err == nil {
				t.Fatal("LoadDepositWorker() 未拒绝无效配置")
			}
		})
	}
}

func clearDepositWorkerEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"GATEWAY_DATABASE_URL", "GATEWAY_DEPOSIT_OPERATION_TIMEOUT", "GATEWAY_DEPOSIT_IDLE_INTERVAL",
		"GATEWAY_DEPOSIT_RETRY_MIN", "GATEWAY_DEPOSIT_RETRY_MAX", "GATEWAY_DEPOSIT_EXPIRE_INTERVAL",
		"GATEWAY_DEPOSIT_EXPIRE_BATCH_SIZE",
	} {
		t.Setenv(key, "")
	}
}
