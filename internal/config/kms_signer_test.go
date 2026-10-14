package config

import (
	"strings"
	"testing"
)

func TestLoadKMSSignerRequiresExplicitMainnetOptIn(t *testing.T) {
	setKMSSignerEnv(t)
	t.Setenv("GATEWAY_KMS_SIGNER_NETWORK", "tron-mainnet")
	if _, err := LoadKMSSigner(); err == nil || !strings.Contains(err.Error(), "ALLOW_MAINNET") {
		t.Fatalf("未启用主网 LoadKMSSigner() error = %v", err)
	}
	t.Setenv("GATEWAY_KMS_SIGNER_ALLOW_MAINNET", "true")
	config, err := LoadKMSSigner()
	if err != nil {
		t.Fatalf("LoadKMSSigner() error = %v", err)
	}
	if config.Network != "tron-mainnet" || config.AWSRegion != "ap-southeast-1" ||
		config.AWSKMSKeyID != "alias/gateway-tron" || len(config.AllowedContracts) != 1 {
		t.Fatalf("LoadKMSSigner() = %+v", config)
	}
}

func TestLoadKMSSignerAllowsTestnetWithoutMainnetOptIn(t *testing.T) {
	setKMSSignerEnv(t)
	config, err := LoadKMSSigner()
	if err != nil || config.Network != "tron-nile" {
		t.Fatalf("LoadKMSSigner() = %+v, %v", config, err)
	}
}

func setKMSSignerEnv(t *testing.T) {
	t.Helper()
	values := map[string]string{
		"GATEWAY_KMS_SIGNER_DATABASE_URL":       "postgres://gateway:secret@db/gateway?sslmode=require",
		"GATEWAY_KMS_SIGNER_TLS_CERT_FILE":      "/run/secrets/tls.crt",
		"GATEWAY_KMS_SIGNER_TLS_KEY_FILE":       "/run/secrets/tls.key",
		"GATEWAY_KMS_SIGNER_BEARER_TOKEN":       "secret-token",
		"GATEWAY_KMS_SIGNER_NETWORK":            "tron-nile",
		"GATEWAY_KMS_SIGNER_FULL_NODE_URL":      "https://nile.example",
		"GATEWAY_KMS_SIGNER_OWNER_ADDRESS":      "TMVQGm1qAQYVdetCeGRRkTWYYrLXuHK2HC",
		"GATEWAY_KMS_SIGNER_CONTRACTS":          "TXYZopYRdj2D9XRtbG411XZZ3kM5VkAeBf,TXYZopYRdj2D9XRtbG411XZZ3kM5VkAeBf",
		"GATEWAY_KMS_SIGNER_MAX_AMOUNT":         "1000000",
		"GATEWAY_KMS_SIGNER_AWS_REGION":         "ap-southeast-1",
		"GATEWAY_KMS_SIGNER_AWS_KEY_ID":         "alias/gateway-tron",
	}
	for name, value := range values {
		t.Setenv(name, value)
	}
}
