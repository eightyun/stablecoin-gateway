package config

import "testing"

func TestLoadTestnetSigner(t *testing.T) {
	clearTestnetSignerEnvironment(t)
	t.Setenv("GATEWAY_TESTNET_SIGNER_TLS_CERT_FILE", "/tmp/cert.pem")
	t.Setenv("GATEWAY_TESTNET_SIGNER_TLS_KEY_FILE", "/tmp/key.pem")
	t.Setenv("GATEWAY_TESTNET_SIGNER_PRIVATE_KEY_FILE", "/tmp/wallet.key")
	t.Setenv("GATEWAY_TESTNET_SIGNER_STORE_DIR", "/tmp/signer-store")
	t.Setenv("GATEWAY_TESTNET_SIGNER_BEARER_TOKEN", "secret")
	t.Setenv("GATEWAY_TESTNET_SIGNER_NETWORK", "tron-nile")
	t.Setenv("GATEWAY_TESTNET_SIGNER_FULL_NODE_URL", "https://nile.example")
	t.Setenv("GATEWAY_TESTNET_SIGNER_OWNER_ADDRESS", "T9yD14Nj9j7xAB4dbGeiX9h8unkKHxuWwb")
	t.Setenv("GATEWAY_TESTNET_SIGNER_CONTRACTS", "TXYZopYRdj2D9XRtbG411XZZ3kM5VkAeBf")
	t.Setenv("GATEWAY_TESTNET_SIGNER_MAX_AMOUNT", "1000000")
	config, err := LoadTestnetSigner()
	if err != nil {
		t.Fatalf("LoadTestnetSigner() error = %v", err)
	}
	if config.Network != "tron-nile" || config.FeeLimit != 100_000_000 || len(config.AllowedContracts) != 1 {
		t.Fatalf("LoadTestnetSigner() = %+v", config)
	}
}

func TestLoadTestnetSignerRejectsMainnet(t *testing.T) {
	clearTestnetSignerEnvironment(t)
	for name, value := range map[string]string{
		"GATEWAY_TESTNET_SIGNER_TLS_CERT_FILE":    "/tmp/cert.pem",
		"GATEWAY_TESTNET_SIGNER_TLS_KEY_FILE":     "/tmp/key.pem",
		"GATEWAY_TESTNET_SIGNER_PRIVATE_KEY_FILE": "/tmp/wallet.key",
		"GATEWAY_TESTNET_SIGNER_STORE_DIR":        "/tmp/signer-store",
		"GATEWAY_TESTNET_SIGNER_BEARER_TOKEN":     "secret",
		"GATEWAY_TESTNET_SIGNER_NETWORK":          "tron-mainnet",
		"GATEWAY_TESTNET_SIGNER_FULL_NODE_URL":    "https://api.trongrid.io",
		"GATEWAY_TESTNET_SIGNER_OWNER_ADDRESS":    "T9yD14Nj9j7xAB4dbGeiX9h8unkKHxuWwb",
		"GATEWAY_TESTNET_SIGNER_CONTRACTS":        "TXYZopYRdj2D9XRtbG411XZZ3kM5VkAeBf",
		"GATEWAY_TESTNET_SIGNER_MAX_AMOUNT":       "1000000",
	} {
		t.Setenv(name, value)
	}
	if _, err := LoadTestnetSigner(); err == nil {
		t.Fatal("LoadTestnetSigner() 应拒绝主网")
	}
}

func clearTestnetSignerEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"GATEWAY_TESTNET_SIGNER_HTTP_ADDR", "GATEWAY_TESTNET_SIGNER_TLS_CERT_FILE",
		"GATEWAY_TESTNET_SIGNER_TLS_KEY_FILE", "GATEWAY_TESTNET_SIGNER_PRIVATE_KEY_FILE",
		"GATEWAY_TESTNET_SIGNER_STORE_DIR", "GATEWAY_TESTNET_SIGNER_BEARER_TOKEN",
		"GATEWAY_TESTNET_SIGNER_NETWORK", "GATEWAY_TESTNET_SIGNER_FULL_NODE_URL",
		"GATEWAY_TESTNET_SIGNER_NODE_API_KEY", "GATEWAY_TESTNET_SIGNER_OWNER_ADDRESS",
		"GATEWAY_TESTNET_SIGNER_CONTRACTS", "GATEWAY_TESTNET_SIGNER_MAX_AMOUNT",
		"GATEWAY_TESTNET_SIGNER_FEE_LIMIT", "GATEWAY_TESTNET_SIGNER_MAX_TRANSACTION_LIFETIME",
		"GATEWAY_TESTNET_SIGNER_MAX_REQUEST_BODY_BYTES", "GATEWAY_TESTNET_SIGNER_NODE_MAX_RESPONSE_BYTES",
		"GATEWAY_TESTNET_SIGNER_READ_HEADER_TIMEOUT", "GATEWAY_TESTNET_SIGNER_SHUTDOWN_TIMEOUT",
	} {
		t.Setenv(name, "")
	}
}
