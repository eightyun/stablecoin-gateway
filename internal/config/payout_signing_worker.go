package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/chain/tron"
)

const (
	defaultPayoutSigningOperationTimeout = 20 * time.Second
	defaultPayoutSigningLeaseDuration    = time.Minute
	defaultPayoutSigningIdleInterval     = time.Second
	defaultPayoutSigningRetryMin         = time.Second
	defaultPayoutSigningRetryMax         = 30 * time.Second
	defaultPayoutSignerMaxResponseBytes  = 2 << 20
	defaultPayoutSignerMaxLifetime       = 10 * time.Minute
)

// PayoutSigningWorkerConfig 保存出款签名 Worker 与远程签名服务配置。
type PayoutSigningWorkerConfig struct {
	DatabaseURL            string
	WorkerID               string
	SignerURL              string
	SignerBearerToken      string
	SignerAddress          string
	SignerMaxFeeLimit      int64
	SignerMaxLifetime      time.Duration
	SignerMaxResponseBytes int64
	OperationTimeout       time.Duration
	LeaseDuration          time.Duration
	IdleInterval           time.Duration
	RetryMin               time.Duration
	RetryMax               time.Duration
}

// LoadPayoutSigningWorker 加载出款签名 Worker 配置。
func LoadPayoutSigningWorker() (PayoutSigningWorkerConfig, error) {
	databaseURL, err := requiredEnv("GATEWAY_DATABASE_URL")
	if err != nil {
		return PayoutSigningWorkerConfig{}, err
	}
	signerURL, err := requiredEnv("GATEWAY_PAYOUT_SIGNER_URL")
	if err != nil {
		return PayoutSigningWorkerConfig{}, err
	}
	signerToken, err := requiredEnv("GATEWAY_PAYOUT_SIGNER_BEARER_TOKEN")
	if err != nil {
		return PayoutSigningWorkerConfig{}, err
	}
	signerAddress, err := requiredEnv("GATEWAY_PAYOUT_SIGNER_ADDRESS")
	if err != nil {
		return PayoutSigningWorkerConfig{}, err
	}
	if _, err := tron.NormalizeAddressHex(signerAddress); err != nil {
		return PayoutSigningWorkerConfig{}, fmt.Errorf("GATEWAY_PAYOUT_SIGNER_ADDRESS: %w", err)
	}
	signerMaxFeeLimit, err := positiveInt64Env("GATEWAY_PAYOUT_SIGNER_MAX_FEE_LIMIT", true)
	if err != nil {
		return PayoutSigningWorkerConfig{}, err
	}
	signerMaxLifetime, err := durationFromEnv("GATEWAY_PAYOUT_SIGNER_MAX_TRANSACTION_LIFETIME", defaultPayoutSignerMaxLifetime)
	if err != nil {
		return PayoutSigningWorkerConfig{}, err
	}
	operationTimeout, err := durationFromEnv("GATEWAY_PAYOUT_SIGNING_OPERATION_TIMEOUT", defaultPayoutSigningOperationTimeout)
	if err != nil {
		return PayoutSigningWorkerConfig{}, err
	}
	leaseDuration, err := durationFromEnv("GATEWAY_PAYOUT_SIGNING_LEASE_DURATION", defaultPayoutSigningLeaseDuration)
	if err != nil {
		return PayoutSigningWorkerConfig{}, err
	}
	idleInterval, err := durationFromEnv("GATEWAY_PAYOUT_SIGNING_IDLE_INTERVAL", defaultPayoutSigningIdleInterval)
	if err != nil {
		return PayoutSigningWorkerConfig{}, err
	}
	retryMin, err := durationFromEnv("GATEWAY_PAYOUT_SIGNING_RETRY_MIN", defaultPayoutSigningRetryMin)
	if err != nil {
		return PayoutSigningWorkerConfig{}, err
	}
	retryMax, err := durationFromEnv("GATEWAY_PAYOUT_SIGNING_RETRY_MAX", defaultPayoutSigningRetryMax)
	if err != nil {
		return PayoutSigningWorkerConfig{}, err
	}
	maxResponseBytes := int64(defaultPayoutSignerMaxResponseBytes)
	if value, parseErr := positiveInt64Env("GATEWAY_PAYOUT_SIGNER_MAX_RESPONSE_BYTES", false); parseErr != nil {
		return PayoutSigningWorkerConfig{}, parseErr
	} else if value > 0 {
		maxResponseBytes = value
	}
	if leaseDuration <= operationTimeout+5*time.Second || retryMax < retryMin || maxResponseBytes > 16<<20 {
		return PayoutSigningWorkerConfig{}, fmt.Errorf("出款签名 Worker 配置关系无效")
	}
	workerID := strings.TrimSpace(os.Getenv("GATEWAY_PAYOUT_SIGNING_WORKER_ID"))
	if workerID == "" {
		workerID = defaultWorkerID()
	}
	if len(workerID) > 128 {
		return PayoutSigningWorkerConfig{}, fmt.Errorf("GATEWAY_PAYOUT_SIGNING_WORKER_ID 不能超过 128 字符")
	}
	return PayoutSigningWorkerConfig{
		DatabaseURL: databaseURL, WorkerID: workerID,
		SignerURL: signerURL, SignerBearerToken: signerToken,
		SignerAddress: signerAddress, SignerMaxFeeLimit: signerMaxFeeLimit,
		SignerMaxLifetime:      signerMaxLifetime,
		SignerMaxResponseBytes: maxResponseBytes, OperationTimeout: operationTimeout,
		LeaseDuration: leaseDuration, IdleInterval: idleInterval,
		RetryMin: retryMin, RetryMax: retryMax,
	}, nil
}
