package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	defaultSweepSigningOperationTimeout = 25 * time.Second
	defaultSweepSigningLeaseDuration    = 90 * time.Second
	defaultSweepSigningIdleInterval     = time.Second
	defaultSweepSigningRetryMin         = time.Second
	defaultSweepSigningRetryMax         = 30 * time.Second
	defaultSweepSignerMaxResponseBytes  = 2 << 20
	defaultSweepSignerMaxLifetime       = 10 * time.Minute
	defaultSweepNodeMaxResponseBytes    = 2 << 20
)

// SweepSigningWorkerConfig 保存归集余额预检和隔离签名配置。
type SweepSigningWorkerConfig struct {
	DatabaseURL            string
	WorkerID               string
	Network                string
	SolidityNodeURL        string
	NodeAPIKey             string
	NodeMaxResponseBytes   int64
	SignerURL              string
	SignerBearerToken      string
	SignerMaxFeeLimit      int64
	SignerMaxLifetime      time.Duration
	SignerMaxResponseBytes int64
	SignerCAFile           string
	SignerClientCertFile   string
	SignerClientKeyFile    string
	OperationTimeout       time.Duration
	LeaseDuration          time.Duration
	IdleInterval           time.Duration
	RetryMin               time.Duration
	RetryMax               time.Duration
}

// LoadSweepSigningWorker 加载归集签名进程配置。
func LoadSweepSigningWorker() (SweepSigningWorkerConfig, error) {
	databaseURL, err := requiredEnv("GATEWAY_DATABASE_URL")
	if err != nil {
		return SweepSigningWorkerConfig{}, err
	}
	network, err := requiredEnv("GATEWAY_TRON_NETWORK")
	if err != nil {
		return SweepSigningWorkerConfig{}, err
	}
	network, err = validatedTRONNetwork(network)
	if err != nil {
		return SweepSigningWorkerConfig{}, err
	}
	mainnetEnabled, err := boolEnv("GATEWAY_TRON_MAINNET_ENABLED", false)
	if err != nil {
		return SweepSigningWorkerConfig{}, err
	}
	if network == "tron-mainnet" && !mainnetEnabled {
		return SweepSigningWorkerConfig{}, fmt.Errorf("tron-mainnet 必须显式设置 GATEWAY_TRON_MAINNET_ENABLED=true")
	}
	solidityNodeURL, err := requiredEnv("GATEWAY_SWEEP_TRON_SOLIDITY_NODE_URL")
	if err != nil {
		return SweepSigningWorkerConfig{}, err
	}
	signerURL, err := requiredEnv("GATEWAY_SWEEP_SIGNER_URL")
	if err != nil {
		return SweepSigningWorkerConfig{}, err
	}
	signerToken, err := requiredEnv("GATEWAY_SWEEP_SIGNER_BEARER_TOKEN")
	if err != nil {
		return SweepSigningWorkerConfig{}, err
	}
	maxFeeLimit, err := positiveInt64Env("GATEWAY_SWEEP_SIGNER_MAX_FEE_LIMIT", true)
	if err != nil {
		return SweepSigningWorkerConfig{}, err
	}
	maxLifetime, err := durationFromEnv("GATEWAY_SWEEP_SIGNER_MAX_TRANSACTION_LIFETIME", defaultSweepSignerMaxLifetime)
	if err != nil {
		return SweepSigningWorkerConfig{}, err
	}
	operationTimeout, err := durationFromEnv("GATEWAY_SWEEP_SIGNING_OPERATION_TIMEOUT", defaultSweepSigningOperationTimeout)
	if err != nil {
		return SweepSigningWorkerConfig{}, err
	}
	leaseDuration, err := durationFromEnv("GATEWAY_SWEEP_SIGNING_LEASE_DURATION", defaultSweepSigningLeaseDuration)
	if err != nil {
		return SweepSigningWorkerConfig{}, err
	}
	idleInterval, err := durationFromEnv("GATEWAY_SWEEP_SIGNING_IDLE_INTERVAL", defaultSweepSigningIdleInterval)
	if err != nil {
		return SweepSigningWorkerConfig{}, err
	}
	retryMin, err := durationFromEnv("GATEWAY_SWEEP_SIGNING_RETRY_MIN", defaultSweepSigningRetryMin)
	if err != nil {
		return SweepSigningWorkerConfig{}, err
	}
	retryMax, err := durationFromEnv("GATEWAY_SWEEP_SIGNING_RETRY_MAX", defaultSweepSigningRetryMax)
	if err != nil {
		return SweepSigningWorkerConfig{}, err
	}
	signerMaxResponseBytes, err := optionalBoundedInt64(
		"GATEWAY_SWEEP_SIGNER_MAX_RESPONSE_BYTES", defaultSweepSignerMaxResponseBytes, 1024, 16<<20,
	)
	if err != nil {
		return SweepSigningWorkerConfig{}, err
	}
	nodeMaxResponseBytes, err := optionalBoundedInt64(
		"GATEWAY_SWEEP_NODE_MAX_RESPONSE_BYTES", defaultSweepNodeMaxResponseBytes, 1024, 16<<20,
	)
	if err != nil {
		return SweepSigningWorkerConfig{}, err
	}
	if leaseDuration <= operationTimeout+5*time.Second || retryMax < retryMin {
		return SweepSigningWorkerConfig{}, fmt.Errorf("归集签名 Worker 配置关系无效")
	}
	clientCert := strings.TrimSpace(os.Getenv("GATEWAY_SWEEP_SIGNER_CLIENT_CERT_FILE"))
	clientKey := strings.TrimSpace(os.Getenv("GATEWAY_SWEEP_SIGNER_CLIENT_KEY_FILE"))
	if (clientCert == "") != (clientKey == "") {
		return SweepSigningWorkerConfig{}, fmt.Errorf("归集 signer 客户端证书和私钥必须同时配置")
	}
	workerID := strings.TrimSpace(os.Getenv("GATEWAY_SWEEP_SIGNING_WORKER_ID"))
	if workerID == "" {
		workerID = defaultWorkerID()
	}
	if len(workerID) > 128 {
		return SweepSigningWorkerConfig{}, fmt.Errorf("GATEWAY_SWEEP_SIGNING_WORKER_ID 不能超过 128 字符")
	}
	return SweepSigningWorkerConfig{
		DatabaseURL: databaseURL, WorkerID: workerID, Network: network,
		SolidityNodeURL: solidityNodeURL, NodeAPIKey: strings.TrimSpace(os.Getenv("GATEWAY_TRON_API_KEY")),
		NodeMaxResponseBytes: nodeMaxResponseBytes, SignerURL: signerURL,
		SignerBearerToken: signerToken, SignerMaxFeeLimit: maxFeeLimit,
		SignerMaxLifetime: maxLifetime, SignerMaxResponseBytes: signerMaxResponseBytes,
		SignerCAFile:         strings.TrimSpace(os.Getenv("GATEWAY_SWEEP_SIGNER_CA_FILE")),
		SignerClientCertFile: clientCert, SignerClientKeyFile: clientKey,
		OperationTimeout: operationTimeout, LeaseDuration: leaseDuration,
		IdleInterval: idleInterval, RetryMin: retryMin, RetryMax: retryMax,
	}, nil
}
