package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/chain/tron"
)

const (
	defaultTRONPreflightTimeout          = 20 * time.Second
	defaultTRONPreflightMaxHeadAge       = 2 * time.Minute
	defaultTRONPreflightMaxFutureSkew    = 30 * time.Second
	defaultTRONPreflightMaxFinalizedLag  = 64
	defaultTRONPreflightMaxResponseBytes = 2 << 20
)

// TRONPreflightConfig 保存测试网只读预检配置。
type TRONPreflightConfig struct {
	Network              string
	FullNodeURL          string
	SolidityNodeURL      string
	NodeAPIKey           string
	ContractAddress      string
	ExpectedSymbol       string
	ExpectedDecimals     uint8
	MaxFinalizedLag      uint64
	MaxHeadAge           time.Duration
	MaxFutureSkew        time.Duration
	Timeout              time.Duration
	NodeMaxResponseBytes int64
}

// LoadTRONPreflight 加载测试网预检配置；该入口明确禁止主网。
func LoadTRONPreflight() (TRONPreflightConfig, error) {
	network, err := requiredEnv("GATEWAY_TRON_NETWORK")
	if err != nil {
		return TRONPreflightConfig{}, err
	}
	network, err = validatedTRONNetwork(network)
	if err != nil {
		return TRONPreflightConfig{}, err
	}
	if network == "tron-mainnet" {
		return TRONPreflightConfig{}, fmt.Errorf("测试网预检不允许 tron-mainnet")
	}
	fullNodeURL, err := requiredEnv("GATEWAY_PAYOUT_TRON_FULL_NODE_URL")
	if err != nil {
		return TRONPreflightConfig{}, err
	}
	solidityNodeURL, err := requiredEnv("GATEWAY_PAYOUT_TRON_SOLIDITY_NODE_URL")
	if err != nil {
		return TRONPreflightConfig{}, err
	}
	contractAddress, err := requiredEnv("GATEWAY_TRON_PREFLIGHT_CONTRACT")
	if err != nil {
		return TRONPreflightConfig{}, err
	}
	contractAddress, err = tron.NormalizeAddress(contractAddress)
	if err != nil {
		return TRONPreflightConfig{}, fmt.Errorf("GATEWAY_TRON_PREFLIGHT_CONTRACT: %w", err)
	}
	expectedSymbol, err := requiredEnv("GATEWAY_TRON_PREFLIGHT_EXPECTED_SYMBOL")
	if err != nil {
		return TRONPreflightConfig{}, err
	}
	if len(expectedSymbol) > 64 {
		return TRONPreflightConfig{}, fmt.Errorf("GATEWAY_TRON_PREFLIGHT_EXPECTED_SYMBOL 不能超过 64 字符")
	}
	expectedDecimals, err := uint8Env("GATEWAY_TRON_PREFLIGHT_EXPECTED_DECIMALS")
	if err != nil {
		return TRONPreflightConfig{}, err
	}
	maxFinalizedLag, err := positiveInt64Env("GATEWAY_TRON_PREFLIGHT_MAX_FINALIZED_LAG", false)
	if err != nil {
		return TRONPreflightConfig{}, err
	}
	if maxFinalizedLag == 0 {
		maxFinalizedLag = defaultTRONPreflightMaxFinalizedLag
	}
	maxHeadAge, err := durationFromEnv("GATEWAY_TRON_PREFLIGHT_MAX_HEAD_AGE", defaultTRONPreflightMaxHeadAge)
	if err != nil {
		return TRONPreflightConfig{}, err
	}
	maxFutureSkew, err := durationFromEnv("GATEWAY_TRON_PREFLIGHT_MAX_FUTURE_SKEW", defaultTRONPreflightMaxFutureSkew)
	if err != nil {
		return TRONPreflightConfig{}, err
	}
	timeout, err := durationFromEnv("GATEWAY_TRON_PREFLIGHT_TIMEOUT", defaultTRONPreflightTimeout)
	if err != nil {
		return TRONPreflightConfig{}, err
	}
	maxResponseBytes := int64(defaultTRONPreflightMaxResponseBytes)
	if value, parseErr := positiveInt64Env("GATEWAY_PAYOUT_NODE_MAX_RESPONSE_BYTES", false); parseErr != nil {
		return TRONPreflightConfig{}, parseErr
	} else if value > 0 {
		maxResponseBytes = value
	}
	if maxResponseBytes > 16<<20 {
		return TRONPreflightConfig{}, fmt.Errorf("GATEWAY_PAYOUT_NODE_MAX_RESPONSE_BYTES 不能超过 16MiB")
	}
	return TRONPreflightConfig{
		Network: network, FullNodeURL: fullNodeURL, SolidityNodeURL: solidityNodeURL,
		NodeAPIKey:      strings.TrimSpace(os.Getenv("GATEWAY_TRON_API_KEY")),
		ContractAddress: contractAddress, ExpectedSymbol: expectedSymbol, ExpectedDecimals: expectedDecimals,
		MaxFinalizedLag: uint64(maxFinalizedLag), MaxHeadAge: maxHeadAge,
		MaxFutureSkew: maxFutureSkew, Timeout: timeout, NodeMaxResponseBytes: maxResponseBytes,
	}, nil
}

func uint8Env(name string) (uint8, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	value, err := strconv.ParseUint(raw, 10, 8)
	if err != nil {
		return 0, fmt.Errorf("配置 %s 必须是 0 到 255 的整数", name)
	}
	return uint8(value), nil
}
