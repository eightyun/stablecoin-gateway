package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	defaultSweepExecutionOperationTimeout = 20 * time.Second
	defaultSweepExecutionLeaseDuration    = time.Minute
	defaultSweepConfirmationInterval      = 10 * time.Second
	defaultSweepExecutionIdleInterval     = time.Second
	defaultSweepExecutionRetryMin         = time.Second
	defaultSweepExecutionRetryMax         = 30 * time.Second
	defaultSweepBroadcastMaxResponseBytes = 64 << 10
)

// SweepExecutionWorkerConfig 保存归集广播和固化确认配置。
type SweepExecutionWorkerConfig struct {
	DatabaseURL               string
	WorkerID                  string
	Network                   string
	FullNodeURL               string
	SolidityNodeURL           string
	NodeAPIKey                string
	NodeMaxResponseBytes      int64
	BroadcastMaxResponseBytes int64
	OperationTimeout          time.Duration
	LeaseDuration             time.Duration
	ConfirmationInterval      time.Duration
	IdleInterval              time.Duration
	RetryMin                  time.Duration
	RetryMax                  time.Duration
}

// LoadSweepExecutionWorker 加载归集执行 Worker 配置。
func LoadSweepExecutionWorker() (SweepExecutionWorkerConfig, error) {
	databaseURL, err := requiredEnv("GATEWAY_DATABASE_URL")
	if err != nil {
		return SweepExecutionWorkerConfig{}, err
	}
	fullNodeURL, err := requiredEnv("GATEWAY_SWEEP_TRON_FULL_NODE_URL")
	if err != nil {
		return SweepExecutionWorkerConfig{}, err
	}
	solidityNodeURL, err := requiredEnv("GATEWAY_SWEEP_TRON_SOLIDITY_NODE_URL")
	if err != nil {
		return SweepExecutionWorkerConfig{}, err
	}
	network, err := requiredEnv("GATEWAY_TRON_NETWORK")
	if err != nil {
		return SweepExecutionWorkerConfig{}, err
	}
	network, err = validatedTRONNetwork(network)
	if err != nil {
		return SweepExecutionWorkerConfig{}, err
	}
	mainnetEnabled, err := boolEnv("GATEWAY_TRON_MAINNET_ENABLED", false)
	if err != nil {
		return SweepExecutionWorkerConfig{}, err
	}
	if network == "tron-mainnet" && !mainnetEnabled {
		return SweepExecutionWorkerConfig{}, fmt.Errorf("tron-mainnet 必须显式设置 GATEWAY_TRON_MAINNET_ENABLED=true")
	}
	operationTimeout, err := durationFromEnv(
		"GATEWAY_SWEEP_EXECUTION_OPERATION_TIMEOUT", defaultSweepExecutionOperationTimeout,
	)
	if err != nil {
		return SweepExecutionWorkerConfig{}, err
	}
	leaseDuration, err := durationFromEnv(
		"GATEWAY_SWEEP_EXECUTION_LEASE_DURATION", defaultSweepExecutionLeaseDuration,
	)
	if err != nil {
		return SweepExecutionWorkerConfig{}, err
	}
	confirmationInterval, err := durationFromEnv(
		"GATEWAY_SWEEP_CONFIRMATION_INTERVAL", defaultSweepConfirmationInterval,
	)
	if err != nil {
		return SweepExecutionWorkerConfig{}, err
	}
	idleInterval, err := durationFromEnv(
		"GATEWAY_SWEEP_EXECUTION_IDLE_INTERVAL", defaultSweepExecutionIdleInterval,
	)
	if err != nil {
		return SweepExecutionWorkerConfig{}, err
	}
	retryMin, err := durationFromEnv("GATEWAY_SWEEP_EXECUTION_RETRY_MIN", defaultSweepExecutionRetryMin)
	if err != nil {
		return SweepExecutionWorkerConfig{}, err
	}
	retryMax, err := durationFromEnv("GATEWAY_SWEEP_EXECUTION_RETRY_MAX", defaultSweepExecutionRetryMax)
	if err != nil {
		return SweepExecutionWorkerConfig{}, err
	}
	nodeMaxResponseBytes, err := optionalBoundedInt64(
		"GATEWAY_SWEEP_NODE_MAX_RESPONSE_BYTES", defaultSweepNodeMaxResponseBytes, 1024, 16<<20,
	)
	if err != nil {
		return SweepExecutionWorkerConfig{}, err
	}
	broadcastMaxResponseBytes, err := optionalBoundedInt64(
		"GATEWAY_SWEEP_BROADCAST_MAX_RESPONSE_BYTES", defaultSweepBroadcastMaxResponseBytes, 1024, 1<<20,
	)
	if err != nil {
		return SweepExecutionWorkerConfig{}, err
	}
	if leaseDuration <= operationTimeout+5*time.Second || retryMax < retryMin {
		return SweepExecutionWorkerConfig{}, fmt.Errorf("归集执行 Worker 配置关系无效")
	}
	workerID := strings.TrimSpace(os.Getenv("GATEWAY_SWEEP_EXECUTION_WORKER_ID"))
	if workerID == "" {
		workerID = defaultWorkerID()
	}
	if len(workerID) > 128 {
		return SweepExecutionWorkerConfig{}, fmt.Errorf("GATEWAY_SWEEP_EXECUTION_WORKER_ID 不能超过 128 字符")
	}
	return SweepExecutionWorkerConfig{
		DatabaseURL: databaseURL, WorkerID: workerID, Network: network,
		FullNodeURL: fullNodeURL, SolidityNodeURL: solidityNodeURL,
		NodeAPIKey:           strings.TrimSpace(os.Getenv("GATEWAY_TRON_API_KEY")),
		NodeMaxResponseBytes: nodeMaxResponseBytes, BroadcastMaxResponseBytes: broadcastMaxResponseBytes,
		OperationTimeout: operationTimeout, LeaseDuration: leaseDuration,
		ConfirmationInterval: confirmationInterval, IdleInterval: idleInterval,
		RetryMin: retryMin, RetryMax: retryMax,
	}, nil
}
