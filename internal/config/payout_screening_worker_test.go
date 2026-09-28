package config

import (
	"testing"
	"time"
)

func TestLoadPayoutScreeningWorker(t *testing.T) {
	clearPayoutScreeningEnvironment(t)
	t.Setenv("GATEWAY_DATABASE_URL", "postgres://gateway:test@localhost/gateway")
	t.Setenv("GATEWAY_SCREENING_PROVIDER_URL", "https://screening.internal")
	t.Setenv("GATEWAY_SCREENING_PROVIDER_NAME", "internal-adapter")
	t.Setenv("GATEWAY_SCREENING_PROVIDER_BEARER_TOKEN", "secret")
	t.Setenv("GATEWAY_SCREENING_WORKER_ID", "screening-worker-1")
	config, err := LoadPayoutScreeningWorker()
	if err != nil {
		t.Fatalf("LoadPayoutScreeningWorker() error = %v", err)
	}
	if config.RequestTimeout != 10*time.Second || config.OperationTimeout != 15*time.Second ||
		config.LeaseDuration != time.Minute || config.MaxResultValidity != 24*time.Hour ||
		config.MaxResponseBytes != 64<<10 || config.WorkerID != "screening-worker-1" {
		t.Fatalf("LoadPayoutScreeningWorker() = %+v", config)
	}
}

func TestLoadPayoutScreeningWorkerRejectsUnsafeRelationships(t *testing.T) {
	for _, test := range []struct {
		name  string
		env   string
		value string
	}{
		{name: "操作超时短于请求", env: "GATEWAY_SCREENING_OPERATION_TIMEOUT", value: "5s"},
		{name: "租约过短", env: "GATEWAY_SCREENING_LEASE_DURATION", value: "15s"},
		{name: "结果有效期过长", env: "GATEWAY_SCREENING_MAX_RESULT_VALIDITY", value: "744h"},
		{name: "响应过大", env: "GATEWAY_SCREENING_MAX_RESPONSE_BYTES", value: "1048577"},
	} {
		t.Run(test.name, func(t *testing.T) {
			clearPayoutScreeningEnvironment(t)
			t.Setenv("GATEWAY_DATABASE_URL", "postgres://gateway:test@localhost/gateway")
			t.Setenv("GATEWAY_SCREENING_PROVIDER_URL", "https://screening.internal")
			t.Setenv("GATEWAY_SCREENING_PROVIDER_NAME", "internal-adapter")
			t.Setenv("GATEWAY_SCREENING_PROVIDER_BEARER_TOKEN", "secret")
			t.Setenv(test.env, test.value)
			if _, err := LoadPayoutScreeningWorker(); err == nil {
				t.Fatal("LoadPayoutScreeningWorker() 未拒绝无效配置")
			}
		})
	}
}

func clearPayoutScreeningEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"GATEWAY_DATABASE_URL", "GATEWAY_SCREENING_PROVIDER_URL", "GATEWAY_SCREENING_PROVIDER_NAME",
		"GATEWAY_SCREENING_PROVIDER_BEARER_TOKEN", "GATEWAY_SCREENING_REQUEST_TIMEOUT",
		"GATEWAY_SCREENING_MAX_RESPONSE_BYTES", "GATEWAY_SCREENING_MAX_RESULT_VALIDITY",
		"GATEWAY_SCREENING_WORKER_ID", "GATEWAY_SCREENING_OPERATION_TIMEOUT",
		"GATEWAY_SCREENING_LEASE_DURATION", "GATEWAY_SCREENING_IDLE_INTERVAL",
		"GATEWAY_SCREENING_RETRY_MIN", "GATEWAY_SCREENING_RETRY_MAX",
	} {
		t.Setenv(name, "")
	}
}
