//go:build integration

package wallet

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/eightyun/stablecoin-gateway/internal/database"
	"github.com/eightyun/stablecoin-gateway/internal/identity"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestStoreSavesImmutableWalletBalanceSnapshot(t *testing.T) {
	store, pool, asset, wallets := newWalletStoreFixture(t)
	snapshot := Snapshot{
		ID: walletUUID(t), Asset: asset, Block: snapshotHeader(12, "c"),
		Balances: []Balance{
			{WalletID: wallets[0].ID, Amount: "7"},
			{WalletID: wallets[1].ID, Amount: "5"},
		},
		TotalBalance: "12",
	}
	if err := store.SaveSnapshot(context.Background(), snapshot); err != nil {
		t.Fatalf("SaveSnapshot() error = %v", err)
	}
	var walletCount int
	var totalBalance string
	if err := pool.QueryRow(context.Background(), `
		SELECT wallet_count, total_balance::TEXT
		FROM wallet_balance_snapshot_runs WHERE id = $1
	`, snapshot.ID).Scan(&walletCount, &totalBalance); err != nil || walletCount != 2 || totalBalance != "12" {
		t.Fatalf("快照运行 wallet_count=%d total=%s error=%v", walletCount, totalBalance, err)
	}
	var balanceCount int
	if err := pool.QueryRow(context.Background(), `
		SELECT COUNT(*) FROM wallet_balance_snapshots WHERE run_id = $1
	`, snapshot.ID).Scan(&balanceCount); err != nil || balanceCount != 2 {
		t.Fatalf("钱包快照数量=%d error=%v", balanceCount, err)
	}
	if _, err := pool.Exec(context.Background(), `
		UPDATE wallet_balance_snapshot_runs SET total_balance = 0 WHERE id = $1
	`, snapshot.ID); err == nil || !strings.Contains(err.Error(), "wallet_balance_snapshot_runs rows are immutable") {
		t.Fatalf("修改不可变快照 error=%v", err)
	}
}

func TestStoreRejectsChangedWalletSet(t *testing.T) {
	store, _, asset, wallets := newWalletStoreFixture(t)
	err := store.SaveSnapshot(context.Background(), Snapshot{
		ID: walletUUID(t), Asset: asset, Block: snapshotHeader(12, "d"),
		Balances: []Balance{{WalletID: wallets[0].ID, Amount: "7"}}, TotalBalance: "7",
	})
	if !errors.Is(err, ErrWalletSetChanged) {
		t.Fatalf("SaveSnapshot() error = %v", err)
	}
}

func newWalletStoreFixture(t *testing.T) (*Store, *pgxpool.Pool, Asset, []Wallet) {
	t.Helper()
	databaseURL := os.Getenv("GATEWAY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("未设置 GATEWAY_TEST_DATABASE_URL")
	}
	pool, err := database.Open(context.Background(), database.DefaultConfig(databaseURL, "wallet-integration-test"))
	if err != nil {
		t.Fatalf("连接测试数据库: %v", err)
	}
	t.Cleanup(pool.Close)
	assetID := "wallet-" + walletUUID(t)
	asset := Asset{ID: assetID, Network: "tron-" + assetID, ContractAddress: "41" + strings.Repeat("a", 40)}
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO assets (id, network, contract_address, symbol, decimals, status)
		VALUES ($1, $2, $3, 'USDT', 6, 'active')
	`, asset.ID, asset.Network, asset.ContractAddress); err != nil {
		t.Fatalf("创建钱包测试资产: %v", err)
	}
	wallets := []Wallet{
		{ID: walletUUID(t), AssetID: asset.ID, Address: "41" + strings.Repeat("1", 40), Role: "deposit"},
		{ID: walletUUID(t), AssetID: asset.ID, Address: "41" + strings.Repeat("2", 40), Role: "hot"},
	}
	for _, item := range wallets {
		if _, err := pool.Exec(context.Background(), `
			INSERT INTO custody_wallets (id, asset_id, address, role, status)
			VALUES ($1, $2, $3, $4, 'active')
		`, item.ID, item.AssetID, item.Address, item.Role); err != nil {
			t.Fatalf("创建测试托管钱包: %v", err)
		}
	}
	store, err := NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	return store, pool, asset, wallets
}

func walletUUID(t *testing.T) string {
	t.Helper()
	value, err := identity.NewUUID()
	if err != nil {
		t.Fatalf("生成 UUID: %v", err)
	}
	return value
}
