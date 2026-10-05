package config

import "testing"

const (
	testSweepAcceptanceSource      = "410000000000000000000000000000000000000000"
	testSweepAcceptanceDestination = "411111111111111111111111111111111111111111"
)

func TestLoadSweepAcceptance(t *testing.T) {
	setSweepAcceptanceEnvironment(t)

	config, err := LoadSweepAcceptance()
	if err != nil {
		t.Fatalf("LoadSweepAcceptance() error = %v", err)
	}
	if config.Signing.Network != "tron-nile" || config.Execution.Network != "tron-nile" ||
		config.SourceAddress != testSweepAcceptanceSource ||
		config.DestinationAddress != testSweepAcceptanceDestination || config.MaximumAmount != "1000000" {
		t.Fatalf("LoadSweepAcceptance() = %+v", config)
	}
}

func TestLoadSweepAcceptanceRejectsMainnet(t *testing.T) {
	setSweepAcceptanceEnvironment(t)
	t.Setenv("GATEWAY_TRON_NETWORK", "tron-mainnet")
	t.Setenv("GATEWAY_TRON_MAINNET_ENABLED", "true")

	if _, err := LoadSweepAcceptance(); err == nil {
		t.Fatal("LoadSweepAcceptance() 应拒绝主网")
	}
}

func TestLoadSweepAcceptanceRejectsUnsafeBounds(t *testing.T) {
	for _, test := range []struct {
		name  string
		key   string
		value string
	}{
		{name: "上限低于阈值", key: "GATEWAY_SWEEP_ACCEPTANCE_MAX_AMOUNT", value: "99"},
		{name: "来源等于目标", key: "GATEWAY_SWEEP_ACCEPTANCE_DESTINATION_ADDRESS", value: testSweepAcceptanceSource},
		{name: "超时过短", key: "GATEWAY_SWEEP_ACCEPTANCE_TIMEOUT", value: "30s"},
	} {
		t.Run(test.name, func(t *testing.T) {
			setSweepAcceptanceEnvironment(t)
			t.Setenv(test.key, test.value)
			if _, err := LoadSweepAcceptance(); err == nil {
				t.Fatal("LoadSweepAcceptance() 未拒绝不安全配置")
			}
		})
	}
}

func setSweepAcceptanceEnvironment(t *testing.T) {
	t.Helper()
	values := map[string]string{
		"GATEWAY_DATABASE_URL":                         "postgres://gateway:secret@localhost:5432/gateway",
		"GATEWAY_SWEEP_ASSET_ID":                       "usdt-trc20-nile",
		"GATEWAY_SWEEP_MINIMUM_AMOUNT":                 "100",
		"GATEWAY_TRON_NETWORK":                         "tron-nile",
		"GATEWAY_SWEEP_TRON_SOLIDITY_NODE_URL":         "https://nile.trongrid.io",
		"GATEWAY_SWEEP_TRON_FULL_NODE_URL":             "https://nile.trongrid.io",
		"GATEWAY_SWEEP_SIGNER_URL":                     "https://signer.internal",
		"GATEWAY_SWEEP_SIGNER_BEARER_TOKEN":            "test-token",
		"GATEWAY_SWEEP_SIGNER_MAX_FEE_LIMIT":           "100000000",
		"GATEWAY_SWEEP_ACCEPTANCE_SOURCE_ADDRESS":      testSweepAcceptanceSource,
		"GATEWAY_SWEEP_ACCEPTANCE_DESTINATION_ADDRESS": testSweepAcceptanceDestination,
		"GATEWAY_SWEEP_ACCEPTANCE_MAX_AMOUNT":          "1000000",
	}
	for key, value := range values {
		t.Setenv(key, value)
	}
}
