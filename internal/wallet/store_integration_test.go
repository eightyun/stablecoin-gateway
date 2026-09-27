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

func TestStoreRegistersCustodyWalletWithImmutableAudit(t *testing.T) {
	store, pool, asset := newRegistrationFixture(t)
	request := RegisterRequest{
		AssetID: asset.ID, Address: "T9yD14Nj9j7xAB4dbGeiX9h8unkKHxuWwb", Role: RoleFee,
		Actor: "ops@example.com", Reason: "resource fee wallet",
	}
	created, err := store.Register(context.Background(), request)
	if err != nil || !created.Created || created.Address != "410000000000000000000000000000000000000000" {
		t.Fatalf("Register() = %+v, %v", created, err)
	}
	retry, err := store.Register(context.Background(), request)
	if err != nil || retry.Created || retry.WalletID != created.WalletID ||
		!retry.RegisteredAt.Equal(created.RegisteredAt) {
		t.Fatalf("重复 Register() = %+v, %v", retry, err)
	}
	conflict := request
	conflict.Reason = "different reason"
	if _, err := store.Register(context.Background(), conflict); !errors.Is(err, ErrWalletConflict) {
		t.Fatalf("冲突 Register() error = %v", err)
	}
	var actor, reason string
	if err := pool.QueryRow(context.Background(), `
		SELECT actor, reason FROM custody_wallet_registration_audits WHERE wallet_id = $1
	`, created.WalletID).Scan(&actor, &reason); err != nil || actor != request.Actor || reason != request.Reason {
		t.Fatalf("登记审计 actor=%s reason=%s error=%v", actor, reason, err)
	}
	if _, err := pool.Exec(context.Background(), `
		UPDATE custody_wallet_registration_audits SET reason = 'tampered' WHERE wallet_id = $1
	`, created.WalletID); err == nil || !strings.Contains(err.Error(), "registration audits are immutable") {
		t.Fatalf("修改登记审计 error=%v", err)
	}
}

func TestStoreRegistersCustodyWalletConcurrentlyOnce(t *testing.T) {
	store, _, asset := newRegistrationFixture(t)
	request := RegisterRequest{
		AssetID: asset.ID, Address: walletHexAddress(t), Role: RoleHot,
		Actor: "ops@example.com", Reason: "primary payout wallet",
	}
	start := make(chan struct{})
	results := make(chan RegisterResult, 2)
	errorsChannel := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			result, err := store.Register(context.Background(), request)
			results <- result
			errorsChannel <- err
		}()
	}
	close(start)
	first, second := <-results, <-results
	firstErr, secondErr := <-errorsChannel, <-errorsChannel
	if firstErr != nil || secondErr != nil || first.WalletID == "" || first.WalletID != second.WalletID ||
		first.Created == second.Created {
		t.Fatalf("并发 Register() first=%+v second=%+v errors=%v,%v", first, second, firstErr, secondErr)
	}
}

func TestDatabaseRejectsUnauditedNonDepositWallet(t *testing.T) {
	_, pool, asset := newRegistrationFixture(t)
	_, err := pool.Exec(context.Background(), `
		INSERT INTO custody_wallets (id, asset_id, address, role, status)
		VALUES ($1, $2, $3, 'cold', 'active')
	`, walletUUID(t), asset.ID, walletHexAddress(t))
	if err == nil || !strings.Contains(err.Error(), "requires registration audit") {
		t.Fatalf("插入无审计钱包 error=%v", err)
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
	asset := Asset{ID: assetID, Network: "tron-nile", ContractAddress: walletHexAddress(t)}
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO assets (id, network, contract_address, symbol, decimals, status)
		VALUES ($1, $2, $3, 'USDT', 6, 'active')
	`, asset.ID, asset.Network, asset.ContractAddress); err != nil {
		t.Fatalf("创建钱包测试资产: %v", err)
	}
	wallets := []Wallet{{ID: walletUUID(t), AssetID: asset.ID, Address: walletHexAddress(t), Role: "deposit"}}
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO custody_wallets (id, asset_id, address, role, status)
		VALUES ($1, $2, $3, $4, 'active')
	`, wallets[0].ID, wallets[0].AssetID, wallets[0].Address, wallets[0].Role); err != nil {
		t.Fatalf("创建测试充值钱包: %v", err)
	}
	store, err := NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	registered, err := store.Register(context.Background(), RegisterRequest{
		AssetID: asset.ID, Address: walletHexAddress(t), Role: RoleHot,
		Actor: "integration-test", Reason: "snapshot fixture",
	})
	if err != nil {
		t.Fatalf("创建测试热钱包: %v", err)
	}
	wallets = append(wallets, Wallet{
		ID: registered.WalletID, AssetID: asset.ID, Address: registered.Address, Role: registered.Role,
	})
	return store, pool, asset, wallets
}

func newRegistrationFixture(t *testing.T) (*Store, *pgxpool.Pool, Asset) {
	t.Helper()
	databaseURL := os.Getenv("GATEWAY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("未设置 GATEWAY_TEST_DATABASE_URL")
	}
	pool, err := database.Open(context.Background(), database.DefaultConfig(databaseURL, "wallet-registration-test"))
	if err != nil {
		t.Fatalf("连接测试数据库: %v", err)
	}
	t.Cleanup(pool.Close)
	asset := Asset{ID: "wallet-" + walletUUID(t), Network: "tron-nile", ContractAddress: walletHexAddress(t)}
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO assets (id, network, contract_address, symbol, decimals, status)
		VALUES ($1, $2, $3, 'USDT', 6, 'active')
	`, asset.ID, asset.Network, asset.ContractAddress); err != nil {
		t.Fatalf("创建钱包登记测试资产: %v", err)
	}
	store, err := NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	return store, pool, asset
}

func walletUUID(t *testing.T) string {
	t.Helper()
	value, err := identity.NewUUID()
	if err != nil {
		t.Fatalf("生成 UUID: %v", err)
	}
	return value
}

func walletHexAddress(t *testing.T) string {
	t.Helper()
	return "41" + strings.ReplaceAll(walletUUID(t), "-", "") + strings.Repeat("0", 8)
}
