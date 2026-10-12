package config

import (
	"fmt"
	"os"
	"strings"
)

// ReconciliationConfig 保存一次性对账任务配置。
type ReconciliationConfig struct {
	DatabaseURL   string
	WalletAssetID string
}

// LoadReconciliation 加载一次性对账任务配置。
func LoadReconciliation() (ReconciliationConfig, error) {
	databaseURL, err := requiredEnv("GATEWAY_DATABASE_URL")
	if err != nil {
		return ReconciliationConfig{}, err
	}
	walletAssetID := strings.TrimSpace(os.Getenv("GATEWAY_RECONCILIATION_WALLET_ASSET_ID"))
	if len(walletAssetID) > 128 {
		return ReconciliationConfig{}, fmt.Errorf("GATEWAY_RECONCILIATION_WALLET_ASSET_ID 不能超过 128 字符")
	}
	return ReconciliationConfig{DatabaseURL: databaseURL, WalletAssetID: walletAssetID}, nil
}
