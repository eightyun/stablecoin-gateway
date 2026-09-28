package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	defaultScreeningRequestTimeout   = 10 * time.Second
	defaultScreeningMaxResponseBytes = 64 << 10
	defaultScreeningMaxValidity      = 24 * time.Hour
	defaultScreeningOperationTimeout = 15 * time.Second
	defaultScreeningLeaseDuration    = time.Minute
	defaultScreeningIdleInterval     = time.Second
	defaultScreeningRetryMin         = time.Second
	defaultScreeningRetryMax         = 30 * time.Second
)

// PayoutScreeningWorkerConfig 保存出款地址筛查 Worker 与 Provider 配置。
type PayoutScreeningWorkerConfig struct {
	DatabaseURL       string
	WorkerID          string
	ProviderURL       string
	ProviderName      string
	ProviderToken     string
	RequestTimeout    time.Duration
	MaxResponseBytes  int64
	MaxResultValidity time.Duration
	OperationTimeout  time.Duration
	LeaseDuration     time.Duration
	IdleInterval      time.Duration
	RetryMin          time.Duration
	RetryMax          time.Duration
}

// LoadPayoutScreeningWorker 加载出款地址筛查 Worker 配置。
func LoadPayoutScreeningWorker() (PayoutScreeningWorkerConfig, error) {
	databaseURL, err := requiredEnv("GATEWAY_DATABASE_URL")
	if err != nil {
		return PayoutScreeningWorkerConfig{}, err
	}
	providerURL, err := requiredEnv("GATEWAY_SCREENING_PROVIDER_URL")
	if err != nil {
		return PayoutScreeningWorkerConfig{}, err
	}
	providerName, err := requiredEnv("GATEWAY_SCREENING_PROVIDER_NAME")
	if err != nil {
		return PayoutScreeningWorkerConfig{}, err
	}
	if len(providerName) > 128 {
		return PayoutScreeningWorkerConfig{}, fmt.Errorf("GATEWAY_SCREENING_PROVIDER_NAME 不能超过 128 字符")
	}
	providerToken, err := requiredEnv("GATEWAY_SCREENING_PROVIDER_BEARER_TOKEN")
	if err != nil {
		return PayoutScreeningWorkerConfig{}, err
	}
	requestTimeout, err := durationFromEnv("GATEWAY_SCREENING_REQUEST_TIMEOUT", defaultScreeningRequestTimeout)
	if err != nil {
		return PayoutScreeningWorkerConfig{}, err
	}
	maxResultValidity, err := durationFromEnv("GATEWAY_SCREENING_MAX_RESULT_VALIDITY", defaultScreeningMaxValidity)
	if err != nil {
		return PayoutScreeningWorkerConfig{}, err
	}
	operationTimeout, err := durationFromEnv("GATEWAY_SCREENING_OPERATION_TIMEOUT", defaultScreeningOperationTimeout)
	if err != nil {
		return PayoutScreeningWorkerConfig{}, err
	}
	leaseDuration, err := durationFromEnv("GATEWAY_SCREENING_LEASE_DURATION", defaultScreeningLeaseDuration)
	if err != nil {
		return PayoutScreeningWorkerConfig{}, err
	}
	idleInterval, err := durationFromEnv("GATEWAY_SCREENING_IDLE_INTERVAL", defaultScreeningIdleInterval)
	if err != nil {
		return PayoutScreeningWorkerConfig{}, err
	}
	retryMin, err := durationFromEnv("GATEWAY_SCREENING_RETRY_MIN", defaultScreeningRetryMin)
	if err != nil {
		return PayoutScreeningWorkerConfig{}, err
	}
	retryMax, err := durationFromEnv("GATEWAY_SCREENING_RETRY_MAX", defaultScreeningRetryMax)
	if err != nil {
		return PayoutScreeningWorkerConfig{}, err
	}
	maxResponseBytes := int64(defaultScreeningMaxResponseBytes)
	if value, parseErr := positiveInt64Env("GATEWAY_SCREENING_MAX_RESPONSE_BYTES", false); parseErr != nil {
		return PayoutScreeningWorkerConfig{}, parseErr
	} else if value > 0 {
		maxResponseBytes = value
	}
	if operationTimeout < requestTimeout || leaseDuration <= operationTimeout+5*time.Second ||
		retryMax < retryMin || maxResponseBytes > 1<<20 || maxResultValidity <= time.Minute ||
		maxResultValidity > 30*24*time.Hour {
		return PayoutScreeningWorkerConfig{}, fmt.Errorf("出款筛查 Worker 配置关系无效")
	}
	workerID := strings.TrimSpace(os.Getenv("GATEWAY_SCREENING_WORKER_ID"))
	if workerID == "" {
		workerID = defaultWorkerID()
	}
	if len(workerID) > 128 {
		return PayoutScreeningWorkerConfig{}, fmt.Errorf("GATEWAY_SCREENING_WORKER_ID 不能超过 128 字符")
	}
	return PayoutScreeningWorkerConfig{
		DatabaseURL: databaseURL, WorkerID: workerID, ProviderURL: providerURL,
		ProviderName: providerName, ProviderToken: providerToken,
		RequestTimeout: requestTimeout, MaxResponseBytes: maxResponseBytes,
		MaxResultValidity: maxResultValidity, OperationTimeout: operationTimeout,
		LeaseDuration: leaseDuration, IdleInterval: idleInterval,
		RetryMin: retryMin, RetryMax: retryMax,
	}, nil
}
