package config

import (
	"strings"
	"testing"
)

func TestLoadReconciliation(t *testing.T) {
	t.Setenv("GATEWAY_RECONCILIATION_WALLET_ASSET_ID", "")
	t.Setenv("GATEWAY_DATABASE_URL", "")
	if _, err := LoadReconciliation(); err == nil {
		t.Fatal("LoadReconciliation() 未拒绝空数据库地址")
	}
	t.Setenv("GATEWAY_DATABASE_URL", "postgres://gateway@example.com/gateway")
	loaded, err := LoadReconciliation()
	if err != nil || loaded.DatabaseURL != "postgres://gateway@example.com/gateway" || loaded.WalletAssetID != "" {
		t.Fatalf("LoadReconciliation() = %+v, %v", loaded, err)
	}
	t.Setenv("GATEWAY_RECONCILIATION_WALLET_ASSET_ID", " usdt-tron-nile ")
	loaded, err = LoadReconciliation()
	if err != nil || loaded.WalletAssetID != "usdt-tron-nile" {
		t.Fatalf("LoadReconciliation() scoped = %+v, %v", loaded, err)
	}
	t.Setenv("GATEWAY_RECONCILIATION_WALLET_ASSET_ID", strings.Repeat("a", 129))
	if _, err := LoadReconciliation(); err == nil {
		t.Fatal("LoadReconciliation() 未拒绝过长资产标识")
	}
}
