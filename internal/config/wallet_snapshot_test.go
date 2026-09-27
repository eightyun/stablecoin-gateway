package config

import (
	"testing"
	"time"
)

func TestLoadWalletSnapshot(t *testing.T) {
	clearWalletSnapshotEnvironment(t)
	t.Setenv("GATEWAY_DATABASE_URL", "postgres://gateway@example.com/gateway")
	t.Setenv("GATEWAY_PAYOUT_TRON_SOLIDITY_NODE_URL", "https://nile-solidity.example")
	t.Setenv("GATEWAY_WALLET_SNAPSHOT_ASSET_ID", "usdt-tron-nile")
	loaded, err := LoadWalletSnapshot()
	if err != nil {
		t.Fatalf("LoadWalletSnapshot() error = %v", err)
	}
	if loaded.AssetID != "usdt-tron-nile" || loaded.Timeout != 5*time.Minute ||
		loaded.NodeMaxResponseBytes != 2<<20 {
		t.Fatalf("LoadWalletSnapshot() = %+v", loaded)
	}
}

func TestLoadWalletSnapshotRejectsInvalidConfig(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T)
	}{
		{name: "缺少数据库", setup: func(t *testing.T) {}},
		{name: "缺少节点", setup: func(t *testing.T) {
			t.Setenv("GATEWAY_DATABASE_URL", "postgres://gateway@example.com/gateway")
		}},
		{name: "缺少资产", setup: func(t *testing.T) {
			t.Setenv("GATEWAY_DATABASE_URL", "postgres://gateway@example.com/gateway")
			t.Setenv("GATEWAY_PAYOUT_TRON_SOLIDITY_NODE_URL", "https://nile-solidity.example")
		}},
		{name: "超大响应", setup: func(t *testing.T) {
			t.Setenv("GATEWAY_DATABASE_URL", "postgres://gateway@example.com/gateway")
			t.Setenv("GATEWAY_PAYOUT_TRON_SOLIDITY_NODE_URL", "https://nile-solidity.example")
			t.Setenv("GATEWAY_WALLET_SNAPSHOT_ASSET_ID", "usdt-tron-nile")
			t.Setenv("GATEWAY_PAYOUT_NODE_MAX_RESPONSE_BYTES", "16777217")
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clearWalletSnapshotEnvironment(t)
			test.setup(t)
			if _, err := LoadWalletSnapshot(); err == nil {
				t.Fatal("LoadWalletSnapshot() 未返回错误")
			}
		})
	}
}

func clearWalletSnapshotEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"GATEWAY_DATABASE_URL", "GATEWAY_PAYOUT_TRON_SOLIDITY_NODE_URL", "GATEWAY_TRON_API_KEY",
		"GATEWAY_WALLET_SNAPSHOT_ASSET_ID", "GATEWAY_WALLET_SNAPSHOT_TIMEOUT",
		"GATEWAY_PAYOUT_NODE_MAX_RESPONSE_BYTES",
	} {
		t.Setenv(name, "")
	}
}
