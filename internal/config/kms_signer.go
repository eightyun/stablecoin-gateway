package config

import (
	"fmt"
	"math/big"
	"os"
	"strings"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/chain/tron"
)

const (
	defaultKMSSignerHTTPAddr         = "127.0.0.1:9443"
	defaultKMSSignerFeeLimit         = int64(100_000_000)
	defaultKMSSignerLifetime         = 10 * time.Minute
	defaultKMSSignerMaxRequestBytes  = int64(64 << 10)
	defaultKMSSignerMaxResponseBytes = int64(2 << 20)
)

// KMSSignerConfig 保存生产 signer 的数据库、AWS KMS、策略和服务配置。
type KMSSignerConfig struct {
	DatabaseURL            string
	HTTPAddr               string
	TLSCertFile            string
	TLSKeyFile             string
	BearerToken            string
	Network                string
	FullNodeURL            string
	NodeAPIKey             string
	OwnerAddress           string
	AllowedContracts       []string
	MaxAmount              string
	FeeLimit               int64
	MaxTransactionLifetime time.Duration
	MaxRequestBodyBytes    int64
	NodeMaxResponseBytes   int64
	ReadHeaderTimeout      time.Duration
	ShutdownTimeout        time.Duration
	AWSRegion              string
	AWSKMSKeyID            string
}

// LoadKMSSigner 加载生产 KMS signer 配置；主网必须额外显式启用。
func LoadKMSSigner() (KMSSignerConfig, error) {
	requiredNames := []string{
		"GATEWAY_KMS_SIGNER_DATABASE_URL", "GATEWAY_KMS_SIGNER_TLS_CERT_FILE",
		"GATEWAY_KMS_SIGNER_TLS_KEY_FILE", "GATEWAY_KMS_SIGNER_BEARER_TOKEN",
		"GATEWAY_KMS_SIGNER_NETWORK", "GATEWAY_KMS_SIGNER_FULL_NODE_URL",
		"GATEWAY_KMS_SIGNER_OWNER_ADDRESS", "GATEWAY_KMS_SIGNER_CONTRACTS",
		"GATEWAY_KMS_SIGNER_MAX_AMOUNT", "GATEWAY_KMS_SIGNER_AWS_REGION",
		"GATEWAY_KMS_SIGNER_AWS_KEY_ID",
	}
	values := make(map[string]string, len(requiredNames))
	for _, name := range requiredNames {
		value, err := requiredEnv(name)
		if err != nil {
			return KMSSignerConfig{}, err
		}
		values[name] = value
	}
	network := values["GATEWAY_KMS_SIGNER_NETWORK"]
	if network != "tron-mainnet" && network != "tron-nile" && network != "tron-shasta" {
		return KMSSignerConfig{}, fmt.Errorf("GATEWAY_KMS_SIGNER_NETWORK 不受支持")
	}
	if network == "tron-mainnet" && strings.TrimSpace(os.Getenv("GATEWAY_KMS_SIGNER_ALLOW_MAINNET")) != "true" {
		return KMSSignerConfig{}, fmt.Errorf("主网 signer 必须显式设置 GATEWAY_KMS_SIGNER_ALLOW_MAINNET=true")
	}
	owner, err := tron.NormalizeAddressBase58(values["GATEWAY_KMS_SIGNER_OWNER_ADDRESS"])
	if err != nil {
		return KMSSignerConfig{}, fmt.Errorf("GATEWAY_KMS_SIGNER_OWNER_ADDRESS: %w", err)
	}
	contracts, err := normalizedContracts(values["GATEWAY_KMS_SIGNER_CONTRACTS"])
	if err != nil {
		return KMSSignerConfig{}, fmt.Errorf("GATEWAY_KMS_SIGNER_CONTRACTS: %w", err)
	}
	maxAmount := values["GATEWAY_KMS_SIGNER_MAX_AMOUNT"]
	parsedMaxAmount, ok := new(big.Int).SetString(maxAmount, 10)
	if !ok || parsedMaxAmount.Sign() <= 0 || parsedMaxAmount.BitLen() > 256 || parsedMaxAmount.Text(10) != maxAmount {
		return KMSSignerConfig{}, fmt.Errorf("GATEWAY_KMS_SIGNER_MAX_AMOUNT 必须是规范正整数")
	}
	feeLimit := defaultKMSSignerFeeLimit
	if value, parseErr := positiveInt64Env("GATEWAY_KMS_SIGNER_FEE_LIMIT", false); parseErr != nil {
		return KMSSignerConfig{}, parseErr
	} else if value > 0 {
		feeLimit = value
	}
	maxLifetime, err := durationFromEnv("GATEWAY_KMS_SIGNER_MAX_TRANSACTION_LIFETIME", defaultKMSSignerLifetime)
	if err != nil {
		return KMSSignerConfig{}, err
	}
	maxRequestBytes, err := optionalBoundedInt64("GATEWAY_KMS_SIGNER_MAX_REQUEST_BODY_BYTES", defaultKMSSignerMaxRequestBytes, 1024, 1<<20)
	if err != nil {
		return KMSSignerConfig{}, err
	}
	maxResponseBytes, err := optionalBoundedInt64("GATEWAY_KMS_SIGNER_NODE_MAX_RESPONSE_BYTES", defaultKMSSignerMaxResponseBytes, 1024, 16<<20)
	if err != nil {
		return KMSSignerConfig{}, err
	}
	readHeaderTimeout, err := durationFromEnv("GATEWAY_KMS_SIGNER_READ_HEADER_TIMEOUT", defaultReadHeaderTimeout)
	if err != nil {
		return KMSSignerConfig{}, err
	}
	shutdownTimeout, err := durationFromEnv("GATEWAY_KMS_SIGNER_SHUTDOWN_TIMEOUT", defaultShutdownTimeout)
	if err != nil {
		return KMSSignerConfig{}, err
	}
	httpAddr := strings.TrimSpace(os.Getenv("GATEWAY_KMS_SIGNER_HTTP_ADDR"))
	if httpAddr == "" {
		httpAddr = defaultKMSSignerHTTPAddr
	}
	return KMSSignerConfig{
		DatabaseURL: values["GATEWAY_KMS_SIGNER_DATABASE_URL"], HTTPAddr: httpAddr,
		TLSCertFile: values["GATEWAY_KMS_SIGNER_TLS_CERT_FILE"], TLSKeyFile: values["GATEWAY_KMS_SIGNER_TLS_KEY_FILE"],
		BearerToken: values["GATEWAY_KMS_SIGNER_BEARER_TOKEN"], Network: network,
		FullNodeURL: values["GATEWAY_KMS_SIGNER_FULL_NODE_URL"], NodeAPIKey: os.Getenv("GATEWAY_KMS_SIGNER_NODE_API_KEY"),
		OwnerAddress: owner, AllowedContracts: contracts, MaxAmount: maxAmount, FeeLimit: feeLimit,
		MaxTransactionLifetime: maxLifetime, MaxRequestBodyBytes: maxRequestBytes,
		NodeMaxResponseBytes: maxResponseBytes, ReadHeaderTimeout: readHeaderTimeout,
		ShutdownTimeout: shutdownTimeout, AWSRegion: values["GATEWAY_KMS_SIGNER_AWS_REGION"],
		AWSKMSKeyID: values["GATEWAY_KMS_SIGNER_AWS_KEY_ID"],
	}, nil
}

func normalizedContracts(raw string) ([]string, error) {
	values := strings.Split(raw, ",")
	contracts := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		contract, err := tron.NormalizeAddressBase58(strings.TrimSpace(value))
		if err != nil {
			return nil, err
		}
		if _, exists := seen[contract]; exists {
			continue
		}
		seen[contract] = struct{}{}
		contracts = append(contracts, contract)
	}
	if len(contracts) == 0 {
		return nil, fmt.Errorf("至少需要一个合约")
	}
	return contracts, nil
}
