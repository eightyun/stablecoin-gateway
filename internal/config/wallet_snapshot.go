package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	defaultWalletSnapshotTimeout          = 5 * time.Minute
	defaultWalletSnapshotMaxResponseBytes = 2 << 20
)

// WalletSnapshotConfig 保存一次性托管钱包余额快照配置。
type WalletSnapshotConfig struct {
	DatabaseURL          string
	SolidityNodeURL      string
	NodeAPIKey           string
	AssetID              string
	Timeout              time.Duration
	NodeMaxResponseBytes int64
}

// LoadWalletSnapshot 加载钱包余额快照配置。
func LoadWalletSnapshot() (WalletSnapshotConfig, error) {
	databaseURL, err := requiredEnv("GATEWAY_DATABASE_URL")
	if err != nil {
		return WalletSnapshotConfig{}, err
	}
	solidityNodeURL, err := requiredEnv("GATEWAY_PAYOUT_TRON_SOLIDITY_NODE_URL")
	if err != nil {
		return WalletSnapshotConfig{}, err
	}
	assetID, err := requiredEnv("GATEWAY_WALLET_SNAPSHOT_ASSET_ID")
	if err != nil {
		return WalletSnapshotConfig{}, err
	}
	if len(assetID) > 128 {
		return WalletSnapshotConfig{}, fmt.Errorf("GATEWAY_WALLET_SNAPSHOT_ASSET_ID 不能超过 128 字符")
	}
	timeout, err := durationFromEnv("GATEWAY_WALLET_SNAPSHOT_TIMEOUT", defaultWalletSnapshotTimeout)
	if err != nil {
		return WalletSnapshotConfig{}, err
	}
	maxResponseBytes := int64(defaultWalletSnapshotMaxResponseBytes)
	if value, parseErr := positiveInt64Env("GATEWAY_PAYOUT_NODE_MAX_RESPONSE_BYTES", false); parseErr != nil {
		return WalletSnapshotConfig{}, parseErr
	} else if value > 0 {
		maxResponseBytes = value
	}
	if maxResponseBytes > 16<<20 {
		return WalletSnapshotConfig{}, fmt.Errorf("GATEWAY_PAYOUT_NODE_MAX_RESPONSE_BYTES 不能超过 16MiB")
	}
	return WalletSnapshotConfig{
		DatabaseURL: databaseURL, SolidityNodeURL: solidityNodeURL,
		NodeAPIKey: strings.TrimSpace(os.Getenv("GATEWAY_TRON_API_KEY")), AssetID: assetID,
		Timeout: timeout, NodeMaxResponseBytes: maxResponseBytes,
	}, nil
}
