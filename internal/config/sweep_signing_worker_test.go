package config

import (
	"testing"
	"time"
)

func TestLoadSweepSigningWorker(t *testing.T) {
	clearSweepSigningEnvironment(t)
	t.Setenv("GATEWAY_DATABASE_URL", "postgres://gateway:test@localhost/gateway")
	t.Setenv("GATEWAY_TRON_NETWORK", "tron-nile")
	t.Setenv("GATEWAY_SWEEP_TRON_SOLIDITY_NODE_URL", "https://nile.trongrid.io")
	t.Setenv("GATEWAY_SWEEP_SIGNER_URL", "https://signer.example")
	t.Setenv("GATEWAY_SWEEP_SIGNER_BEARER_TOKEN", "secret")
	t.Setenv("GATEWAY_SWEEP_SIGNER_MAX_FEE_LIMIT", "100000000")
	t.Setenv("GATEWAY_SWEEP_SIGNING_WORKER_ID", "sweep-signer-1")
	config, err := LoadSweepSigningWorker()
	if err != nil {
		t.Fatalf("LoadSweepSigningWorker() error = %v", err)
	}
	if config.WorkerID != "sweep-signer-1" || config.OperationTimeout != 25*time.Second ||
		config.LeaseDuration != 90*time.Second || config.SignerMaxLifetime != 10*time.Minute ||
		config.NodeMaxResponseBytes != 2<<20 || config.SignerMaxResponseBytes != 2<<20 {
		t.Fatalf("LoadSweepSigningWorker() = %+v", config)
	}
}

func TestLoadSweepSigningWorkerRejectsUnsafeConfiguration(t *testing.T) {
	for _, test := range []struct {
		name  string
		env   string
		value string
	}{
		{name: "主网未确认", env: "GATEWAY_TRON_NETWORK", value: "tron-mainnet"},
		{name: "租约过短", env: "GATEWAY_SWEEP_SIGNING_LEASE_DURATION", value: "25s"},
		{name: "响应过大", env: "GATEWAY_SWEEP_SIGNER_MAX_RESPONSE_BYTES", value: "16777217"},
	} {
		t.Run(test.name, func(t *testing.T) {
			clearSweepSigningEnvironment(t)
			t.Setenv("GATEWAY_DATABASE_URL", "postgres://gateway:test@localhost/gateway")
			t.Setenv("GATEWAY_TRON_NETWORK", "tron-nile")
			t.Setenv("GATEWAY_SWEEP_TRON_SOLIDITY_NODE_URL", "https://nile.trongrid.io")
			t.Setenv("GATEWAY_SWEEP_SIGNER_URL", "https://signer.example")
			t.Setenv("GATEWAY_SWEEP_SIGNER_BEARER_TOKEN", "secret")
			t.Setenv("GATEWAY_SWEEP_SIGNER_MAX_FEE_LIMIT", "100000000")
			t.Setenv(test.env, test.value)
			if _, err := LoadSweepSigningWorker(); err == nil {
				t.Fatal("LoadSweepSigningWorker() 未拒绝无效配置")
			}
		})
	}
}

func clearSweepSigningEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"GATEWAY_DATABASE_URL", "GATEWAY_TRON_NETWORK", "GATEWAY_TRON_MAINNET_ENABLED",
		"GATEWAY_SWEEP_TRON_SOLIDITY_NODE_URL", "GATEWAY_SWEEP_NODE_MAX_RESPONSE_BYTES",
		"GATEWAY_SWEEP_SIGNER_URL", "GATEWAY_SWEEP_SIGNER_BEARER_TOKEN",
		"GATEWAY_SWEEP_SIGNER_MAX_FEE_LIMIT", "GATEWAY_SWEEP_SIGNER_MAX_TRANSACTION_LIFETIME",
		"GATEWAY_SWEEP_SIGNER_MAX_RESPONSE_BYTES", "GATEWAY_SWEEP_SIGNER_CA_FILE",
		"GATEWAY_SWEEP_SIGNER_CLIENT_CERT_FILE", "GATEWAY_SWEEP_SIGNER_CLIENT_KEY_FILE",
		"GATEWAY_SWEEP_SIGNING_WORKER_ID", "GATEWAY_SWEEP_SIGNING_OPERATION_TIMEOUT",
		"GATEWAY_SWEEP_SIGNING_LEASE_DURATION", "GATEWAY_SWEEP_SIGNING_IDLE_INTERVAL",
		"GATEWAY_SWEEP_SIGNING_RETRY_MIN", "GATEWAY_SWEEP_SIGNING_RETRY_MAX", "GATEWAY_TRON_API_KEY",
	} {
		t.Setenv(name, "")
	}
}
