package config

import (
	"testing"
	"time"
)

func TestLoadPayoutSigningWorker(t *testing.T) {
	clearPayoutSigningEnvironment(t)
	t.Setenv("GATEWAY_DATABASE_URL", "postgres://gateway:test@localhost/gateway")
	t.Setenv("GATEWAY_PAYOUT_SIGNER_URL", "https://signer.example")
	t.Setenv("GATEWAY_PAYOUT_SIGNER_BEARER_TOKEN", "secret")
	t.Setenv("GATEWAY_PAYOUT_SIGNING_WORKER_ID", "signer-worker-1")
	config, err := LoadPayoutSigningWorker()
	if err != nil {
		t.Fatalf("LoadPayoutSigningWorker() error = %v", err)
	}
	if config.OperationTimeout != 20*time.Second || config.LeaseDuration != time.Minute ||
		config.SignerMaxResponseBytes != 2<<20 || config.WorkerID != "signer-worker-1" {
		t.Fatalf("LoadPayoutSigningWorker() = %+v", config)
	}
}

func TestLoadPayoutSigningWorkerRejectsUnsafeRelationships(t *testing.T) {
	for _, test := range []struct {
		name  string
		env   string
		value string
	}{
		{name: "租约过短", env: "GATEWAY_PAYOUT_SIGNING_LEASE_DURATION", value: "20s"},
		{name: "退避倒置", env: "GATEWAY_PAYOUT_SIGNING_RETRY_MAX", value: "500ms"},
		{name: "响应过大", env: "GATEWAY_PAYOUT_SIGNER_MAX_RESPONSE_BYTES", value: "16777217"},
	} {
		t.Run(test.name, func(t *testing.T) {
			clearPayoutSigningEnvironment(t)
			t.Setenv("GATEWAY_DATABASE_URL", "postgres://gateway:test@localhost/gateway")
			t.Setenv("GATEWAY_PAYOUT_SIGNER_URL", "https://signer.example")
			t.Setenv("GATEWAY_PAYOUT_SIGNER_BEARER_TOKEN", "secret")
			t.Setenv(test.env, test.value)
			if _, err := LoadPayoutSigningWorker(); err == nil {
				t.Fatal("LoadPayoutSigningWorker() 未拒绝无效配置")
			}
		})
	}
}

func clearPayoutSigningEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"GATEWAY_DATABASE_URL", "GATEWAY_PAYOUT_SIGNER_URL", "GATEWAY_PAYOUT_SIGNER_BEARER_TOKEN",
		"GATEWAY_PAYOUT_SIGNING_WORKER_ID", "GATEWAY_PAYOUT_SIGNER_MAX_RESPONSE_BYTES",
		"GATEWAY_PAYOUT_SIGNING_OPERATION_TIMEOUT", "GATEWAY_PAYOUT_SIGNING_LEASE_DURATION",
		"GATEWAY_PAYOUT_SIGNING_IDLE_INTERVAL", "GATEWAY_PAYOUT_SIGNING_RETRY_MIN",
		"GATEWAY_PAYOUT_SIGNING_RETRY_MAX",
	} {
		t.Setenv(name, "")
	}
}
