package config

import (
	"testing"
	"time"
)

func TestLoadTRONPreflight(t *testing.T) {
	clearTRONPreflightEnvironment(t)
	t.Setenv("GATEWAY_TRON_NETWORK", "tron-nile")
	t.Setenv("GATEWAY_PAYOUT_TRON_FULL_NODE_URL", "https://nile.example")
	t.Setenv("GATEWAY_PAYOUT_TRON_SOLIDITY_NODE_URL", "https://nile-solidity.example")
	t.Setenv("GATEWAY_TRON_PREFLIGHT_CONTRACT", "TXYZopYRdj2D9XRtbG411XZZ3kM5VkAeBf")
	t.Setenv("GATEWAY_TRON_PREFLIGHT_EXPECTED_SYMBOL", "USDT")
	t.Setenv("GATEWAY_TRON_PREFLIGHT_EXPECTED_DECIMALS", "6")
	config, err := LoadTRONPreflight()
	if err != nil {
		t.Fatalf("LoadTRONPreflight() error = %v", err)
	}
	if config.ExpectedDecimals != 6 || config.MaxFinalizedLag != 64 || config.Timeout != 20*time.Second {
		t.Fatalf("LoadTRONPreflight() = %+v", config)
	}
}

func TestLoadTRONPreflightRejectsMainnet(t *testing.T) {
	clearTRONPreflightEnvironment(t)
	t.Setenv("GATEWAY_TRON_NETWORK", "tron-mainnet")
	if _, err := LoadTRONPreflight(); err == nil {
		t.Fatal("LoadTRONPreflight() 应拒绝主网")
	}
}

func clearTRONPreflightEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"GATEWAY_TRON_NETWORK", "GATEWAY_PAYOUT_TRON_FULL_NODE_URL",
		"GATEWAY_PAYOUT_TRON_SOLIDITY_NODE_URL", "GATEWAY_TRON_API_KEY",
		"GATEWAY_TRON_PREFLIGHT_CONTRACT", "GATEWAY_TRON_PREFLIGHT_EXPECTED_SYMBOL",
		"GATEWAY_TRON_PREFLIGHT_EXPECTED_DECIMALS", "GATEWAY_TRON_PREFLIGHT_MAX_FINALIZED_LAG",
		"GATEWAY_TRON_PREFLIGHT_MAX_HEAD_AGE", "GATEWAY_TRON_PREFLIGHT_MAX_FUTURE_SKEW",
		"GATEWAY_TRON_PREFLIGHT_TIMEOUT", "GATEWAY_PAYOUT_NODE_MAX_RESPONSE_BYTES",
	} {
		t.Setenv(name, "")
	}
}
