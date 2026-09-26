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
	defaultTestnetSignerHTTPAddr         = "127.0.0.1:9443"
	defaultTestnetSignerFeeLimit         = int64(100_000_000)
	defaultTestnetSignerLifetime         = 10 * time.Minute
	defaultTestnetSignerMaxRequestBytes  = int64(64 << 10)
	defaultTestnetSignerMaxResponseBytes = int64(2 << 20)
)

// TestnetSignerConfig 保存独立测试网 signer 的安全边界与服务配置。
type TestnetSignerConfig struct {
	HTTPAddr               string
	TLSCertFile            string
	TLSKeyFile             string
	PrivateKeyFile         string
	StoreDirectory         string
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
}

// LoadTestnetSigner 加载测试网 signer 配置，并硬性拒绝主网。
func LoadTestnetSigner() (TestnetSignerConfig, error) {
	requiredNames := []string{
		"GATEWAY_TESTNET_SIGNER_TLS_CERT_FILE", "GATEWAY_TESTNET_SIGNER_TLS_KEY_FILE",
		"GATEWAY_TESTNET_SIGNER_PRIVATE_KEY_FILE", "GATEWAY_TESTNET_SIGNER_STORE_DIR",
		"GATEWAY_TESTNET_SIGNER_BEARER_TOKEN", "GATEWAY_TESTNET_SIGNER_NETWORK",
		"GATEWAY_TESTNET_SIGNER_FULL_NODE_URL", "GATEWAY_TESTNET_SIGNER_OWNER_ADDRESS",
		"GATEWAY_TESTNET_SIGNER_CONTRACTS", "GATEWAY_TESTNET_SIGNER_MAX_AMOUNT",
	}
	values := make(map[string]string, len(requiredNames))
	for _, name := range requiredNames {
		value, err := requiredEnv(name)
		if err != nil {
			return TestnetSignerConfig{}, err
		}
		values[name] = value
	}
	network := values["GATEWAY_TESTNET_SIGNER_NETWORK"]
	if network != "tron-nile" && network != "tron-shasta" {
		return TestnetSignerConfig{}, fmt.Errorf("测试网 signer 仅允许 tron-nile 或 tron-shasta")
	}
	owner, err := tron.NormalizeAddressBase58(values["GATEWAY_TESTNET_SIGNER_OWNER_ADDRESS"])
	if err != nil {
		return TestnetSignerConfig{}, fmt.Errorf("GATEWAY_TESTNET_SIGNER_OWNER_ADDRESS: %w", err)
	}
	contractValues := strings.Split(values["GATEWAY_TESTNET_SIGNER_CONTRACTS"], ",")
	contracts := make([]string, 0, len(contractValues))
	seenContracts := make(map[string]struct{}, len(contractValues))
	for _, value := range contractValues {
		contract, normalizeErr := tron.NormalizeAddressBase58(strings.TrimSpace(value))
		if normalizeErr != nil {
			return TestnetSignerConfig{}, fmt.Errorf("GATEWAY_TESTNET_SIGNER_CONTRACTS: %w", normalizeErr)
		}
		if _, exists := seenContracts[contract]; !exists {
			seenContracts[contract] = struct{}{}
			contracts = append(contracts, contract)
		}
	}
	maxAmount := values["GATEWAY_TESTNET_SIGNER_MAX_AMOUNT"]
	parsedMaxAmount, ok := new(big.Int).SetString(maxAmount, 10)
	if !ok || parsedMaxAmount.Sign() <= 0 || parsedMaxAmount.BitLen() > 256 || parsedMaxAmount.Text(10) != maxAmount {
		return TestnetSignerConfig{}, fmt.Errorf("GATEWAY_TESTNET_SIGNER_MAX_AMOUNT 必须是规范正整数")
	}
	feeLimit := defaultTestnetSignerFeeLimit
	if value, parseErr := positiveInt64Env("GATEWAY_TESTNET_SIGNER_FEE_LIMIT", false); parseErr != nil {
		return TestnetSignerConfig{}, parseErr
	} else if value > 0 {
		feeLimit = value
	}
	maxLifetime, err := durationFromEnv("GATEWAY_TESTNET_SIGNER_MAX_TRANSACTION_LIFETIME", defaultTestnetSignerLifetime)
	if err != nil {
		return TestnetSignerConfig{}, err
	}
	maxRequestBytes, err := optionalBoundedInt64("GATEWAY_TESTNET_SIGNER_MAX_REQUEST_BODY_BYTES", defaultTestnetSignerMaxRequestBytes, 1024, 1<<20)
	if err != nil {
		return TestnetSignerConfig{}, err
	}
	maxResponseBytes, err := optionalBoundedInt64("GATEWAY_TESTNET_SIGNER_NODE_MAX_RESPONSE_BYTES", defaultTestnetSignerMaxResponseBytes, 1024, 16<<20)
	if err != nil {
		return TestnetSignerConfig{}, err
	}
	readHeaderTimeout, err := durationFromEnv("GATEWAY_TESTNET_SIGNER_READ_HEADER_TIMEOUT", defaultReadHeaderTimeout)
	if err != nil {
		return TestnetSignerConfig{}, err
	}
	shutdownTimeout, err := durationFromEnv("GATEWAY_TESTNET_SIGNER_SHUTDOWN_TIMEOUT", defaultShutdownTimeout)
	if err != nil {
		return TestnetSignerConfig{}, err
	}
	httpAddr := strings.TrimSpace(os.Getenv("GATEWAY_TESTNET_SIGNER_HTTP_ADDR"))
	if httpAddr == "" {
		httpAddr = defaultTestnetSignerHTTPAddr
	}
	return TestnetSignerConfig{
		HTTPAddr: httpAddr, TLSCertFile: values["GATEWAY_TESTNET_SIGNER_TLS_CERT_FILE"],
		TLSKeyFile:     values["GATEWAY_TESTNET_SIGNER_TLS_KEY_FILE"],
		PrivateKeyFile: values["GATEWAY_TESTNET_SIGNER_PRIVATE_KEY_FILE"],
		StoreDirectory: values["GATEWAY_TESTNET_SIGNER_STORE_DIR"],
		BearerToken:    values["GATEWAY_TESTNET_SIGNER_BEARER_TOKEN"], Network: network,
		FullNodeURL: values["GATEWAY_TESTNET_SIGNER_FULL_NODE_URL"],
		NodeAPIKey:  os.Getenv("GATEWAY_TESTNET_SIGNER_NODE_API_KEY"), OwnerAddress: owner,
		AllowedContracts: contracts, MaxAmount: maxAmount, FeeLimit: feeLimit,
		MaxTransactionLifetime: maxLifetime, MaxRequestBodyBytes: maxRequestBytes,
		NodeMaxResponseBytes: maxResponseBytes, ReadHeaderTimeout: readHeaderTimeout,
		ShutdownTimeout: shutdownTimeout,
	}, nil
}

func optionalBoundedInt64(name string, fallback, minimum, maximum int64) (int64, error) {
	value, err := positiveInt64Env(name, false)
	if err != nil {
		return 0, err
	}
	if value == 0 {
		value = fallback
	}
	if value < minimum || value > maximum {
		return 0, fmt.Errorf("配置 %s 超出允许范围", name)
	}
	return value, nil
}
