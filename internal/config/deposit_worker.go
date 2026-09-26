package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	defaultDepositOperationTimeout = 10 * time.Second
	defaultDepositIdle             = time.Second
	defaultDepositRetryMin         = time.Second
	defaultDepositRetryMax         = 30 * time.Second
	defaultDepositExpireInterval   = 30 * time.Second
	defaultDepositExpireBatchSize  = 100
)

// DepositWorkerConfig 保存独立充值匹配进程的配置。
type DepositWorkerConfig struct {
	DatabaseURL      string
	OperationTimeout time.Duration
	IdleInterval     time.Duration
	RetryMin         time.Duration
	RetryMax         time.Duration
	ExpireInterval   time.Duration
	ExpireBatchSize  int
}

// LoadDepositWorker 从环境变量加载充值匹配进程配置。
func LoadDepositWorker() (DepositWorkerConfig, error) {
	databaseURL, err := requiredEnv("GATEWAY_DATABASE_URL")
	if err != nil {
		return DepositWorkerConfig{}, err
	}
	operationTimeout, err := durationFromEnv("GATEWAY_DEPOSIT_OPERATION_TIMEOUT", defaultDepositOperationTimeout)
	if err != nil {
		return DepositWorkerConfig{}, err
	}
	idleInterval, err := durationFromEnv("GATEWAY_DEPOSIT_IDLE_INTERVAL", defaultDepositIdle)
	if err != nil {
		return DepositWorkerConfig{}, err
	}
	retryMin, err := durationFromEnv("GATEWAY_DEPOSIT_RETRY_MIN", defaultDepositRetryMin)
	if err != nil {
		return DepositWorkerConfig{}, err
	}
	retryMax, err := durationFromEnv("GATEWAY_DEPOSIT_RETRY_MAX", defaultDepositRetryMax)
	if err != nil {
		return DepositWorkerConfig{}, err
	}
	expireInterval, err := durationFromEnv("GATEWAY_DEPOSIT_EXPIRE_INTERVAL", defaultDepositExpireInterval)
	if err != nil {
		return DepositWorkerConfig{}, err
	}
	expireBatchSize := defaultDepositExpireBatchSize
	if strings.TrimSpace(os.Getenv("GATEWAY_DEPOSIT_EXPIRE_BATCH_SIZE")) != "" {
		value, parseErr := positiveInt64Env("GATEWAY_DEPOSIT_EXPIRE_BATCH_SIZE", true)
		if parseErr != nil {
			return DepositWorkerConfig{}, parseErr
		}
		if value > 1000 {
			return DepositWorkerConfig{}, fmt.Errorf("GATEWAY_DEPOSIT_EXPIRE_BATCH_SIZE 不能大于 1000")
		}
		expireBatchSize = int(value)
	}
	if retryMax < retryMin {
		return DepositWorkerConfig{}, fmt.Errorf("GATEWAY_DEPOSIT_RETRY_MAX 不能小于 GATEWAY_DEPOSIT_RETRY_MIN")
	}
	return DepositWorkerConfig{
		DatabaseURL: databaseURL, OperationTimeout: operationTimeout, IdleInterval: idleInterval,
		RetryMin: retryMin, RetryMax: retryMax, ExpireInterval: expireInterval, ExpireBatchSize: expireBatchSize,
	}, nil
}
