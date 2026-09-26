package config

import (
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultIndexerStepTimeout = 45 * time.Second
	defaultIndexerLease       = time.Minute
	defaultIndexerIdle        = 3 * time.Second
	defaultIndexerRetryMin    = time.Second
	defaultIndexerRetryMax    = 30 * time.Second
)

// IndexerConfig 保存独立 TRON 扫描进程的配置。
type IndexerConfig struct {
	DatabaseURL      string
	NodeURL          string
	NodeAPIKey       string
	Network          string
	Contract         string
	StartHeight      int64
	AnchorHash       string
	WorkerID         string
	MaxResponseBytes int64
	StepTimeout      time.Duration
	LeaseDuration    time.Duration
	IdleInterval     time.Duration
	RetryMin         time.Duration
	RetryMax         time.Duration
}

// LoadIndexer 从环境变量加载扫描进程配置。资金相关锚点不提供默认值。
func LoadIndexer() (IndexerConfig, error) {
	stepTimeout, err := durationFromEnv("GATEWAY_INDEXER_STEP_TIMEOUT", defaultIndexerStepTimeout)
	if err != nil {
		return IndexerConfig{}, err
	}
	leaseDuration, err := durationFromEnv("GATEWAY_INDEXER_LEASE_DURATION", defaultIndexerLease)
	if err != nil {
		return IndexerConfig{}, err
	}
	idleInterval, err := durationFromEnv("GATEWAY_INDEXER_IDLE_INTERVAL", defaultIndexerIdle)
	if err != nil {
		return IndexerConfig{}, err
	}
	retryMin, err := durationFromEnv("GATEWAY_INDEXER_RETRY_MIN", defaultIndexerRetryMin)
	if err != nil {
		return IndexerConfig{}, err
	}
	retryMax, err := durationFromEnv("GATEWAY_INDEXER_RETRY_MAX", defaultIndexerRetryMax)
	if err != nil {
		return IndexerConfig{}, err
	}
	if leaseDuration <= stepTimeout+5*time.Second {
		return IndexerConfig{}, fmt.Errorf("GATEWAY_INDEXER_LEASE_DURATION 必须比 GATEWAY_INDEXER_STEP_TIMEOUT 至少长 5s")
	}
	if retryMax < retryMin {
		return IndexerConfig{}, fmt.Errorf("GATEWAY_INDEXER_RETRY_MAX 不能小于 GATEWAY_INDEXER_RETRY_MIN")
	}

	databaseURL, err := requiredEnv("GATEWAY_DATABASE_URL")
	if err != nil {
		return IndexerConfig{}, err
	}
	nodeURL, err := requiredEnv("GATEWAY_TRON_NODE_URL")
	if err != nil {
		return IndexerConfig{}, err
	}
	network, err := requiredEnv("GATEWAY_TRON_NETWORK")
	if err != nil {
		return IndexerConfig{}, err
	}
	contractRaw, err := requiredEnv("GATEWAY_TRON_CONTRACT")
	if err != nil {
		return IndexerConfig{}, err
	}
	contract, err := normalizedHex("GATEWAY_TRON_CONTRACT", contractRaw, 42, "41")
	if err != nil {
		return IndexerConfig{}, err
	}
	anchorRaw, err := requiredEnv("GATEWAY_TRON_ANCHOR_HASH")
	if err != nil {
		return IndexerConfig{}, err
	}
	anchorHash, err := normalizedHex("GATEWAY_TRON_ANCHOR_HASH", anchorRaw, 64, "")
	if err != nil {
		return IndexerConfig{}, err
	}
	startHeight, err := positiveInt64Env("GATEWAY_TRON_START_HEIGHT", true)
	if err != nil {
		return IndexerConfig{}, err
	}
	maxResponseBytes, err := positiveInt64Env("GATEWAY_TRON_MAX_RESPONSE_BYTES", false)
	if err != nil {
		return IndexerConfig{}, err
	}

	workerID := strings.TrimSpace(os.Getenv("GATEWAY_INDEXER_WORKER_ID"))
	if workerID == "" {
		workerID = defaultWorkerID()
	}
	return IndexerConfig{
		DatabaseURL:      databaseURL,
		NodeURL:          nodeURL,
		NodeAPIKey:       os.Getenv("GATEWAY_TRON_API_KEY"),
		Network:          network,
		Contract:         contract,
		StartHeight:      startHeight,
		AnchorHash:       anchorHash,
		WorkerID:         workerID,
		MaxResponseBytes: maxResponseBytes,
		StepTimeout:      stepTimeout,
		LeaseDuration:    leaseDuration,
		IdleInterval:     idleInterval,
		RetryMin:         retryMin,
		RetryMax:         retryMax,
	}, nil
}

func requiredEnv(name string) (string, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return "", fmt.Errorf("缺少配置 %s", name)
	}
	return value, nil
}

func positiveInt64Env(name string, required bool) (int64, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" && !required {
		return 0, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("配置 %s 必须是正整数", name)
	}
	return value, nil
}

func normalizedHex(name, value string, length int, prefix string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) != length || !strings.HasPrefix(value, prefix) {
		return "", fmt.Errorf("配置 %s 不是规范十六进制值", name)
	}
	if _, err := hex.DecodeString(value); err != nil {
		return "", fmt.Errorf("配置 %s 不是规范十六进制值", name)
	}
	return value, nil
}

func defaultWorkerID() string {
	hostname, err := os.Hostname()
	if err != nil || strings.TrimSpace(hostname) == "" {
		hostname = "gateway-indexer"
	}
	return fmt.Sprintf("%s-%d", hostname, os.Getpid())
}
