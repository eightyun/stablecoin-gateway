//go:build integration

package wallet

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/eightyun/stablecoin-gateway/internal/chain/tron"
	"github.com/eightyun/stablecoin-gateway/internal/database"
	"github.com/eightyun/stablecoin-gateway/internal/identity"
	"github.com/eightyun/stablecoin-gateway/internal/ledger"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestStoreSavesImmutableWalletBalanceSnapshot(t *testing.T) {
	store, pool, asset, wallets := newWalletStoreFixture(t)
	header := snapshotHeader(12, "c")
	reader := &balanceReaderStub{
		headers: []tron.Header{header, header},
		balances: map[string]string{
			wallets[0].Address: "7",
			wallets[1].Address: "5",
		},
	}
	snapshot, err := store.CaptureSnapshot(context.Background(), reader, asset, wallets)
	if err != nil || snapshot.TotalBalance != "12" || snapshot.Ledger.Balance != "0" {
		t.Fatalf("CaptureSnapshot() = %+v, %v", snapshot, err)
	}
	if err := store.SaveSnapshot(context.Background(), snapshot); err != nil {
		t.Fatalf("SaveSnapshot() error = %v", err)
	}
	var walletCount int
	var totalBalance, ledgerAccountID, ledgerBalance string
	var ledgerEntryCount int64
	if err := pool.QueryRow(context.Background(), `
		SELECT wallet_count, total_balance::TEXT, ledger_account_id::TEXT,
		       ledger_entry_count, ledger_balance::TEXT
		FROM wallet_balance_snapshot_runs WHERE id = $1
	`, snapshot.ID).Scan(
		&walletCount, &totalBalance, &ledgerAccountID, &ledgerEntryCount, &ledgerBalance,
	); err != nil || walletCount != 2 || totalBalance != "12" ||
		ledgerAccountID != snapshot.Ledger.AccountID || ledgerEntryCount != 0 || ledgerBalance != "0" {
		t.Fatalf("快照运行 wallet_count=%d total=%s ledger_account=%s ledger_entries=%d ledger_balance=%s error=%v",
			walletCount, totalBalance, ledgerAccountID, ledgerEntryCount, ledgerBalance, err)
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
	checkpoint, err := store.LedgerCheckpoint(context.Background(), asset.ID)
	if err != nil {
		t.Fatalf("LedgerCheckpoint() error = %v", err)
	}
	err = store.SaveSnapshot(context.Background(), Snapshot{
		ID: walletUUID(t), Asset: asset, Block: snapshotHeader(12, "d"),
		Balances: []Balance{{WalletID: wallets[0].ID, Amount: "7"}}, TotalBalance: "7", Ledger: checkpoint,
	})
	if !errors.Is(err, ErrWalletSetChanged) {
		t.Fatalf("SaveSnapshot() error = %v", err)
	}
}

func TestStoreRejectsLedgerChangeDuringCapture(t *testing.T) {
	store, pool, asset, wallets := newWalletStoreFixture(t)
	header := snapshotHeader(12, "e")
	reader := &balanceReaderStub{
		headers: []tron.Header{header, header},
		balances: map[string]string{
			wallets[0].Address: "7", wallets[1].Address: "5",
		},
		onBalance: func() {
			postWalletLedgerChange(t, pool, asset.ID)
		},
	}
	if _, err := store.CaptureSnapshot(context.Background(), reader, asset, wallets); !errors.Is(err, ErrLedgerChanged) {
		t.Fatalf("CaptureSnapshot() error = %v", err)
	}
}

func TestStoreRejectsLedgerChangeBeforeSave(t *testing.T) {
	store, pool, asset, wallets := newWalletStoreFixture(t)
	header := snapshotHeader(12, "f")
	reader := &balanceReaderStub{
		headers: []tron.Header{header, header},
		balances: map[string]string{
			wallets[0].Address: "7", wallets[1].Address: "5",
		},
	}
	snapshot, err := store.CaptureSnapshot(context.Background(), reader, asset, wallets)
	if err != nil {
		t.Fatalf("CaptureSnapshot() error = %v", err)
	}
	postWalletLedgerChange(t, pool, asset.ID)
	if err := store.SaveSnapshot(context.Background(), snapshot); !errors.Is(err, ErrLedgerChanged) {
		t.Fatalf("SaveSnapshot() error = %v", err)
	}
}

func TestStoreCapturesPayoutInFlightEvidence(t *testing.T) {
	store, pool, asset, wallets := newWalletStoreFixture(t)
	merchantID := walletUUID(t)
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO merchants (id, name, status)
		VALUES ($1, 'wallet checkpoint merchant', 'active')
	`, merchantID); err != nil {
		t.Fatalf("创建在途出款测试商户: %v", err)
	}
	freezeTransactionID := postWalletLedgerChange(t, pool, asset.ID)
	payoutID := walletUUID(t)
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO payouts (
			id, merchant_id, asset_id, idempotency_key, merchant_reference,
			request_hash, destination_address, amount, status, freeze_transaction_id,
			reviewed_by, review_reason, reviewed_at, signing_attempts,
			transaction_id, signed_transaction, transaction_expires_at, broadcast_attempts
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, 1, 'ready_for_broadcast', $8,
			'integration-test', 'approved test payout', CURRENT_TIMESTAMP, 1,
			$9, decode('01', 'hex'), CURRENT_TIMESTAMP + INTERVAL '1 hour', 1
		)
	`, payoutID, merchantID, asset.ID, "payout-"+payoutID, "payout-"+payoutID,
		strings.Repeat("b", 64), walletHexAddress(t), freezeTransactionID,
		strings.Repeat("a", 64)); err != nil {
		t.Fatalf("创建在途出款: %v", err)
	}
	checkpoint, err := store.LedgerCheckpoint(context.Background(), asset.ID)
	if err != nil || checkpoint.PayoutInFlightCount != 1 || checkpoint.PayoutInFlightAmount != "1" ||
		checkpoint.SweepInFlightCount != 0 || checkpoint.SweepInFlightAmount != "0" {
		t.Fatalf("LedgerCheckpoint() = %+v, %v", checkpoint, err)
	}
	header := snapshotHeader(12, "1")
	reader := &balanceReaderStub{
		headers: []tron.Header{header, header},
		balances: map[string]string{
			wallets[0].Address: "7", wallets[1].Address: "5",
		},
	}
	snapshot, err := store.CaptureSnapshot(context.Background(), reader, asset, wallets)
	if err != nil || snapshot.Ledger != checkpoint {
		t.Fatalf("CaptureSnapshot() = %+v, %v", snapshot, err)
	}
	if err := store.SaveSnapshot(context.Background(), snapshot); err != nil {
		t.Fatalf("SaveSnapshot() error = %v", err)
	}
	var payoutCount, sweepCount int64
	var payoutAmount, sweepAmount string
	if err := pool.QueryRow(context.Background(), `
		SELECT payout_in_flight_count, payout_in_flight_amount::TEXT,
		       sweep_in_flight_count, sweep_in_flight_amount::TEXT
		FROM wallet_balance_snapshot_runs
		WHERE id = $1
	`, snapshot.ID).Scan(&payoutCount, &payoutAmount, &sweepCount, &sweepAmount); err != nil ||
		payoutCount != 1 || payoutAmount != "1" || sweepCount != 0 || sweepAmount != "0" {
		t.Fatalf("在途快照证据 payout=%d/%s sweep=%d/%s error=%v",
			payoutCount, payoutAmount, sweepCount, sweepAmount, err)
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
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO ledger_accounts (id, owner_type, owner_id, asset_id, code, normal_side, status)
		VALUES
			($1, 'platform', 'gateway', $3, 'custody', 'D', 'active'),
			($2, 'platform', 'gateway', $3, 'equity', 'C', 'active')
	`, walletUUID(t), walletUUID(t), asset.ID); err != nil {
		t.Fatalf("创建钱包测试托管科目: %v", err)
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

func postWalletLedgerChange(t *testing.T, pool *pgxpool.Pool, assetID string) string {
	t.Helper()
	var custodyAccountID, equityAccountID string
	if err := pool.QueryRow(context.Background(), `
		SELECT custody.id::TEXT, equity.id::TEXT
		FROM ledger_accounts AS custody
		JOIN ledger_accounts AS equity ON equity.asset_id = custody.asset_id
		WHERE custody.asset_id = $1
		  AND custody.owner_type = 'platform' AND custody.owner_id = 'gateway'
		  AND custody.code = 'custody'
		  AND equity.owner_type = 'platform' AND equity.owner_id = 'gateway'
		  AND equity.code = 'equity'
	`, assetID).Scan(&custodyAccountID, &equityAccountID); err != nil {
		t.Fatalf("查询测试账本科目: %v", err)
	}
	repository, err := ledger.NewPostgreSQLRepository(pool)
	if err != nil {
		t.Fatalf("NewPostgreSQLRepository() error = %v", err)
	}
	journalID := walletUUID(t)
	if _, err := repository.Post(context.Background(), ledger.Transaction{
		ID: journalID, RequesterType: "system", RequesterID: "wallet-checkpoint-test",
		IdempotencyKey: "change:" + journalID, ReferenceType: "test", ReferenceID: journalID,
		Entries: []ledger.Entry{
			{AccountID: custodyAccountID, AssetID: assetID, Side: ledger.Debit, Amount: 1},
			{AccountID: equityAccountID, AssetID: assetID, Side: ledger.Credit, Amount: 1},
		},
	}); err != nil {
		t.Fatalf("写入账务变化: %v", err)
	}
	return journalID
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
