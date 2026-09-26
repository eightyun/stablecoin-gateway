package config

import (
	"testing"
	"time"
)

func TestLoadPayoutExecutionWorker(t *testing.T) {
	clearPayoutExecutionEnvironment(t)
	t.Setenv("GATEWAY_DATABASE_URL", "postgres://gateway:test@localhost/gateway")
	t.Setenv("GATEWAY_PAYOUT_TRON_FULL_NODE_URL", "https://full.example")
	t.Setenv("GATEWAY_PAYOUT_TRON_SOLIDITY_NODE_URL", "https://solidity.example")
	t.Setenv("GATEWAY_TRON_NETWORK", "tron-nile")
	t.Setenv("GATEWAY_PAYOUT_EXECUTION_WORKER_ID", "execution-1")
	config, err := LoadPayoutExecutionWorker()
	if err != nil {
		t.Fatalf("LoadPayoutExecutionWorker() error = %v", err)
	}
	if config.ConfirmationInterval != 10*time.Second || config.LeaseDuration != time.Minute ||
		config.WorkerID != "execution-1" || config.Network != "tron-nile" {
		t.Fatalf("LoadPayoutExecutionWorker() = %+v", config)
	}
}

func TestLoadPayoutExecutionWorkerRejectsLeaseShorterThanOperation(t *testing.T) {
	clearPayoutExecutionEnvironment(t)
	t.Setenv("GATEWAY_DATABASE_URL", "postgres://gateway:test@localhost/gateway")
	t.Setenv("GATEWAY_PAYOUT_TRON_FULL_NODE_URL", "https://full.example")
	t.Setenv("GATEWAY_PAYOUT_TRON_SOLIDITY_NODE_URL", "https://solidity.example")
	t.Setenv("GATEWAY_TRON_NETWORK", "tron-nile")
	t.Setenv("GATEWAY_PAYOUT_EXECUTION_OPERATION_TIMEOUT", "20s")
	t.Setenv("GATEWAY_PAYOUT_EXECUTION_LEASE_DURATION", "20s")
	if _, err := LoadPayoutExecutionWorker(); err == nil {
		t.Fatal("LoadPayoutExecutionWorker() 应拒绝不足以覆盖操作超时的租约")
	}
}

func clearPayoutExecutionEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"GATEWAY_DATABASE_URL", "GATEWAY_PAYOUT_TRON_FULL_NODE_URL",
		"GATEWAY_PAYOUT_TRON_SOLIDITY_NODE_URL", "GATEWAY_TRON_NETWORK", "GATEWAY_TRON_API_KEY",
		"GATEWAY_PAYOUT_EXECUTION_WORKER_ID", "GATEWAY_PAYOUT_NODE_MAX_RESPONSE_BYTES",
		"GATEWAY_PAYOUT_EXECUTION_OPERATION_TIMEOUT", "GATEWAY_PAYOUT_EXECUTION_LEASE_DURATION",
		"GATEWAY_PAYOUT_CONFIRMATION_INTERVAL", "GATEWAY_PAYOUT_EXECUTION_IDLE_INTERVAL",
		"GATEWAY_PAYOUT_EXECUTION_RETRY_MIN", "GATEWAY_PAYOUT_EXECUTION_RETRY_MAX",
	} {
		t.Setenv(name, "")
	}
}
