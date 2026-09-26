package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultWebhookBatchSize            = 10
	defaultWebhookLeaseDuration        = 3 * time.Minute
	defaultWebhookMaxAttempts          = 8
	defaultWebhookBaseBackoff          = 5 * time.Second
	defaultWebhookMaxBackoff           = time.Hour
	defaultWebhookOperationTimeout     = 2 * time.Minute
	defaultWebhookIdleInterval         = time.Second
	defaultWebhookRetryMin             = time.Second
	defaultWebhookRetryMax             = 30 * time.Second
	defaultWebhookRequestTimeout       = 10 * time.Second
	defaultWebhookMaxResponseBodyBytes = 64 << 10
)

// WebhookSecretsConfig 保存 Webhook 签名 Secret 的加密配置。
type WebhookSecretsConfig struct {
	DatabaseURL      string
	EncryptionKeys   map[string][]byte
	ActiveKeyVersion string
}

// WebhookWorkerConfig 保存 Webhook Worker 的运行配置。
type WebhookWorkerConfig struct {
	WebhookSecretsConfig
	WorkerID             string
	BatchSize            int
	LeaseDuration        time.Duration
	MaxAttempts          int
	BaseBackoff          time.Duration
	MaxBackoff           time.Duration
	OperationTimeout     time.Duration
	IdleInterval         time.Duration
	RetryMin             time.Duration
	RetryMax             time.Duration
	RequestTimeout       time.Duration
	MaxResponseBodyBytes int64
	AllowPrivateNetworks bool
}

// LoadWebhookSecrets 加载端点管理所需的配置。
func LoadWebhookSecrets() (WebhookSecretsConfig, error) {
	databaseURL, err := requiredEnv("GATEWAY_DATABASE_URL")
	if err != nil {
		return WebhookSecretsConfig{}, err
	}
	keys, err := encryptionKeysFromEnv("GATEWAY_WEBHOOK_SECRET_ENCRYPTION_KEYS")
	if err != nil {
		return WebhookSecretsConfig{}, err
	}
	activeVersion, err := requiredEnv("GATEWAY_WEBHOOK_SECRET_ACTIVE_VERSION")
	if err != nil {
		return WebhookSecretsConfig{}, err
	}
	if _, exists := keys[activeVersion]; !exists {
		return WebhookSecretsConfig{}, fmt.Errorf("GATEWAY_WEBHOOK_SECRET_ACTIVE_VERSION 不在密钥环中")
	}
	return WebhookSecretsConfig{
		DatabaseURL: databaseURL, EncryptionKeys: keys, ActiveKeyVersion: activeVersion,
	}, nil
}

// LoadWebhookWorker 加载 Webhook Worker 配置。
func LoadWebhookWorker() (WebhookWorkerConfig, error) {
	secrets, err := LoadWebhookSecrets()
	if err != nil {
		return WebhookWorkerConfig{}, err
	}
	batchSize, err := boundedIntEnv("GATEWAY_WEBHOOK_BATCH_SIZE", defaultWebhookBatchSize, 1, 100)
	if err != nil {
		return WebhookWorkerConfig{}, err
	}
	maxAttempts, err := boundedIntEnv("GATEWAY_WEBHOOK_MAX_ATTEMPTS", defaultWebhookMaxAttempts, 1, 100)
	if err != nil {
		return WebhookWorkerConfig{}, err
	}
	leaseDuration, err := durationFromEnv("GATEWAY_WEBHOOK_LEASE_DURATION", defaultWebhookLeaseDuration)
	if err != nil {
		return WebhookWorkerConfig{}, err
	}
	baseBackoff, err := durationFromEnv("GATEWAY_WEBHOOK_BASE_BACKOFF", defaultWebhookBaseBackoff)
	if err != nil {
		return WebhookWorkerConfig{}, err
	}
	maxBackoff, err := durationFromEnv("GATEWAY_WEBHOOK_MAX_BACKOFF", defaultWebhookMaxBackoff)
	if err != nil {
		return WebhookWorkerConfig{}, err
	}
	operationTimeout, err := durationFromEnv("GATEWAY_WEBHOOK_OPERATION_TIMEOUT", defaultWebhookOperationTimeout)
	if err != nil {
		return WebhookWorkerConfig{}, err
	}
	idleInterval, err := durationFromEnv("GATEWAY_WEBHOOK_IDLE_INTERVAL", defaultWebhookIdleInterval)
	if err != nil {
		return WebhookWorkerConfig{}, err
	}
	retryMin, err := durationFromEnv("GATEWAY_WEBHOOK_RETRY_MIN", defaultWebhookRetryMin)
	if err != nil {
		return WebhookWorkerConfig{}, err
	}
	retryMax, err := durationFromEnv("GATEWAY_WEBHOOK_RETRY_MAX", defaultWebhookRetryMax)
	if err != nil {
		return WebhookWorkerConfig{}, err
	}
	requestTimeout, err := durationFromEnv("GATEWAY_WEBHOOK_REQUEST_TIMEOUT", defaultWebhookRequestTimeout)
	if err != nil {
		return WebhookWorkerConfig{}, err
	}
	maxResponseBodyBytes := int64(defaultWebhookMaxResponseBodyBytes)
	if value, parseErr := positiveInt64Env("GATEWAY_WEBHOOK_MAX_RESPONSE_BODY_BYTES", false); parseErr != nil {
		return WebhookWorkerConfig{}, parseErr
	} else if value > 0 {
		maxResponseBodyBytes = value
	}
	allowPrivateNetworks, err := boolEnv("GATEWAY_WEBHOOK_ALLOW_PRIVATE_NETWORKS", false)
	if err != nil {
		return WebhookWorkerConfig{}, err
	}
	if maxBackoff < baseBackoff || retryMax < retryMin || leaseDuration <= operationTimeout+5*time.Second ||
		operationTimeout < requestTimeout || maxResponseBodyBytes > 1<<20 {
		return WebhookWorkerConfig{}, fmt.Errorf("Webhook Worker 配置关系无效")
	}
	workerID := strings.TrimSpace(os.Getenv("GATEWAY_WEBHOOK_WORKER_ID"))
	if workerID == "" {
		workerID = defaultWorkerID()
	}
	return WebhookWorkerConfig{
		WebhookSecretsConfig: secrets, WorkerID: workerID, BatchSize: batchSize,
		LeaseDuration: leaseDuration, MaxAttempts: maxAttempts, BaseBackoff: baseBackoff,
		MaxBackoff: maxBackoff, OperationTimeout: operationTimeout, IdleInterval: idleInterval,
		RetryMin: retryMin, RetryMax: retryMax, RequestTimeout: requestTimeout,
		MaxResponseBodyBytes: maxResponseBodyBytes, AllowPrivateNetworks: allowPrivateNetworks,
	}, nil
}

func boundedIntEnv(name string, fallback, minimum, maximum int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < minimum || value > maximum {
		return 0, fmt.Errorf("配置 %s 必须在 %d 到 %d 之间", name, minimum, maximum)
	}
	return value, nil
}

func boolEnv(name string, fallback bool) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("配置 %s 必须是布尔值", name)
	}
	return value, nil
}
