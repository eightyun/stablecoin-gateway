package config

import (
	"testing"
	"time"
)

func TestLoadSweepExecutionWorker(t *testing.T) {
	clearSweepExecutionEnvironment(t)
	t.Setenv("GATEWAY_DATABASE_URL", "postgres://gateway:test@localhost/gateway")
	t.Setenv("GATEWAY_SWEEP_TRON_FULL_NODE_URL", "https://full.example")
	t.Setenv("GATEWAY_SWEEP_TRON_SOLIDITY_NODE_URL", "https://solidity.example")
	t.Setenv("GATEWAY_TRON_NETWORK", "tron-nile")
	t.Setenv("GATEWAY_SWEEP_EXECUTION_WORKER_ID", "sweep-execution-1")
	config, err := LoadSweepExecutionWorker()
	if err != nil {
		t.Fatalf("LoadSweepExecutionWorker() error = %v", err)
	}
	if config.ConfirmationInterval != 10*time.Second || config.LeaseDuration != time.Minute ||
		config.WorkerID != "sweep-execution-1" || config.Network != "tron-nile" ||
		config.NodeMaxResponseBytes != 2<<20 || config.BroadcastMaxResponseBytes != 64<<10 {
		t.Fatalf("LoadSweepExecutionWorker() = %+v", config)
	}
}

func TestLoadSweepExecutionWorkerRejectsUnsafeMainnetDefault(t *testing.T) {
	clearSweepExecutionEnvironment(t)
	t.Setenv("GATEWAY_DATABASE_URL", "postgres://gateway:test@localhost/gateway")
	t.Setenv("GATEWAY_SWEEP_TRON_FULL_NODE_URL", "https://full.example")
	t.Setenv("GATEWAY_SWEEP_TRON_SOLIDITY_NODE_URL", "https://solidity.example")
	t.Setenv("GATEWAY_TRON_NETWORK", "tron-mainnet")
	if _, err := LoadSweepExecutionWorker(); err == nil {
		t.Fatal("LoadSweepExecutionWorker() 应拒绝未显式启用的主网")
	}
}

func TestLoadSweepExecutionWorkerRejectsShortLease(t *testing.T) {
	clearSweepExecutionEnvironment(t)
	t.Setenv("GATEWAY_DATABASE_URL", "postgres://gateway:test@localhost/gateway")
	t.Setenv("GATEWAY_SWEEP_TRON_FULL_NODE_URL", "https://full.example")
	t.Setenv("GATEWAY_SWEEP_TRON_SOLIDITY_NODE_URL", "https://solidity.example")
	t.Setenv("GATEWAY_TRON_NETWORK", "tron-nile")
	t.Setenv("GATEWAY_SWEEP_EXECUTION_OPERATION_TIMEOUT", "20s")
	t.Setenv("GATEWAY_SWEEP_EXECUTION_LEASE_DURATION", "20s")
	if _, err := LoadSweepExecutionWorker(); err == nil {
		t.Fatal("LoadSweepExecutionWorker() 应拒绝不足以覆盖操作超时的租约")
	}
}

func clearSweepExecutionEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"GATEWAY_DATABASE_URL", "GATEWAY_SWEEP_TRON_FULL_NODE_URL",
		"GATEWAY_SWEEP_TRON_SOLIDITY_NODE_URL", "GATEWAY_TRON_NETWORK",
		"GATEWAY_TRON_API_KEY", "GATEWAY_TRON_MAINNET_ENABLED",
		"GATEWAY_SWEEP_EXECUTION_WORKER_ID", "GATEWAY_SWEEP_NODE_MAX_RESPONSE_BYTES",
		"GATEWAY_SWEEP_BROADCAST_MAX_RESPONSE_BYTES", "GATEWAY_SWEEP_EXECUTION_OPERATION_TIMEOUT",
		"GATEWAY_SWEEP_EXECUTION_LEASE_DURATION", "GATEWAY_SWEEP_CONFIRMATION_INTERVAL",
		"GATEWAY_SWEEP_EXECUTION_IDLE_INTERVAL", "GATEWAY_SWEEP_EXECUTION_RETRY_MIN",
		"GATEWAY_SWEEP_EXECUTION_RETRY_MAX",
	} {
		t.Setenv(name, "")
	}
}
