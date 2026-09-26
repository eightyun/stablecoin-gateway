package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	defaultPayoutExecutionOperationTimeout = 20 * time.Second
	defaultPayoutExecutionLeaseDuration    = time.Minute
	defaultPayoutConfirmationInterval      = 10 * time.Second
	defaultPayoutExecutionIdleInterval     = time.Second
	defaultPayoutExecutionRetryMin         = time.Second
	defaultPayoutExecutionRetryMax         = 30 * time.Second
	defaultPayoutNodeMaxResponseBytes      = 2 << 20
)

// PayoutExecutionWorkerConfig 保存出款广播和固化确认配置。
type PayoutExecutionWorkerConfig struct {
	DatabaseURL          string
	WorkerID             string
	Network              string
	FullNodeURL          string
	SolidityNodeURL      string
	NodeAPIKey           string
	NodeMaxResponseBytes int64
	OperationTimeout     time.Duration
	LeaseDuration        time.Duration
	ConfirmationInterval time.Duration
	IdleInterval         time.Duration
	RetryMin             time.Duration
	RetryMax             time.Duration
}

// LoadPayoutExecutionWorker 加载出款执行 Worker 配置。
func LoadPayoutExecutionWorker() (PayoutExecutionWorkerConfig, error) {
	databaseURL, err := requiredEnv("GATEWAY_DATABASE_URL")
	if err != nil {
		return PayoutExecutionWorkerConfig{}, err
	}
	fullNodeURL, err := requiredEnv("GATEWAY_PAYOUT_TRON_FULL_NODE_URL")
	if err != nil {
		return PayoutExecutionWorkerConfig{}, err
	}
	solidityNodeURL, err := requiredEnv("GATEWAY_PAYOUT_TRON_SOLIDITY_NODE_URL")
	if err != nil {
		return PayoutExecutionWorkerConfig{}, err
	}
	network, err := requiredEnv("GATEWAY_TRON_NETWORK")
	if err != nil {
		return PayoutExecutionWorkerConfig{}, err
	}
	network, err = validatedTRONNetwork(network)
	if err != nil {
		return PayoutExecutionWorkerConfig{}, err
	}
	mainnetEnabled, err := boolEnv("GATEWAY_TRON_MAINNET_ENABLED", false)
	if err != nil {
		return PayoutExecutionWorkerConfig{}, err
	}
	if network == "tron-mainnet" && !mainnetEnabled {
		return PayoutExecutionWorkerConfig{}, fmt.Errorf("tron-mainnet 必须显式设置 GATEWAY_TRON_MAINNET_ENABLED=true")
	}
	operationTimeout, err := durationFromEnv("GATEWAY_PAYOUT_EXECUTION_OPERATION_TIMEOUT", defaultPayoutExecutionOperationTimeout)
	if err != nil {
		return PayoutExecutionWorkerConfig{}, err
	}
	leaseDuration, err := durationFromEnv("GATEWAY_PAYOUT_EXECUTION_LEASE_DURATION", defaultPayoutExecutionLeaseDuration)
	if err != nil {
		return PayoutExecutionWorkerConfig{}, err
	}
	confirmationInterval, err := durationFromEnv("GATEWAY_PAYOUT_CONFIRMATION_INTERVAL", defaultPayoutConfirmationInterval)
	if err != nil {
		return PayoutExecutionWorkerConfig{}, err
	}
	idleInterval, err := durationFromEnv("GATEWAY_PAYOUT_EXECUTION_IDLE_INTERVAL", defaultPayoutExecutionIdleInterval)
	if err != nil {
		return PayoutExecutionWorkerConfig{}, err
	}
	retryMin, err := durationFromEnv("GATEWAY_PAYOUT_EXECUTION_RETRY_MIN", defaultPayoutExecutionRetryMin)
	if err != nil {
		return PayoutExecutionWorkerConfig{}, err
	}
	retryMax, err := durationFromEnv("GATEWAY_PAYOUT_EXECUTION_RETRY_MAX", defaultPayoutExecutionRetryMax)
	if err != nil {
		return PayoutExecutionWorkerConfig{}, err
	}
	maxResponseBytes := int64(defaultPayoutNodeMaxResponseBytes)
	if value, parseErr := positiveInt64Env("GATEWAY_PAYOUT_NODE_MAX_RESPONSE_BYTES", false); parseErr != nil {
		return PayoutExecutionWorkerConfig{}, parseErr
	} else if value > 0 {
		maxResponseBytes = value
	}
	if leaseDuration <= operationTimeout+5*time.Second || retryMax < retryMin || maxResponseBytes > 16<<20 {
		return PayoutExecutionWorkerConfig{}, fmt.Errorf("出款执行 Worker 配置关系无效")
	}
	workerID := strings.TrimSpace(os.Getenv("GATEWAY_PAYOUT_EXECUTION_WORKER_ID"))
	if workerID == "" {
		workerID = defaultWorkerID()
	}
	if len(workerID) > 128 {
		return PayoutExecutionWorkerConfig{}, fmt.Errorf("GATEWAY_PAYOUT_EXECUTION_WORKER_ID 不能超过 128 字符")
	}
	return PayoutExecutionWorkerConfig{
		DatabaseURL: databaseURL, WorkerID: workerID, Network: network,
		FullNodeURL: fullNodeURL, SolidityNodeURL: solidityNodeURL,
		NodeAPIKey:           strings.TrimSpace(os.Getenv("GATEWAY_TRON_API_KEY")),
		NodeMaxResponseBytes: maxResponseBytes, OperationTimeout: operationTimeout,
		LeaseDuration: leaseDuration, ConfirmationInterval: confirmationInterval,
		IdleInterval: idleInterval, RetryMin: retryMin, RetryMax: retryMax,
	}, nil
}

func validatedTRONNetwork(value string) (string, error) {
	value = strings.TrimSpace(value)
	switch value {
	case "tron-nile", "tron-shasta", "tron-mainnet":
		return value, nil
	default:
		return "", fmt.Errorf("GATEWAY_TRON_NETWORK 必须是 tron-nile、tron-shasta 或 tron-mainnet")
	}
}
