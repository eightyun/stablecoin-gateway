//go:build integration

package recovery

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/eightyun/stablecoin-gateway/internal/database"
	"github.com/eightyun/stablecoin-gateway/internal/identity"
	"github.com/eightyun/stablecoin-gateway/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAuditUsesReadOnlySnapshotAndClassifiesRecoveryState(t *testing.T) {
	databaseURL := os.Getenv("GATEWAY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("未设置 GATEWAY_TEST_DATABASE_URL")
	}
	ctx := context.Background()
	pool, err := database.Open(ctx, database.DefaultConfig(databaseURL, "recovery-audit-test"))
	if err != nil {
		t.Fatalf("连接测试数据库: %v", err)
	}
	defer pool.Close()
	store, err := NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	expectedVersion, err := migrations.LatestVersion()
	if err != nil {
		t.Fatalf("LatestVersion() error = %v", err)
	}

	runsBefore := recoveryRunCount(t, ctx, pool)
	baseReport, err := store.Audit(ctx, expectedVersion)
	if err != nil {
		t.Fatalf("首次 Audit() error = %v", err)
	}
	if !baseReport.Schema.Compatible || baseReport.Schema.Version != expectedVersion {
		t.Fatalf("首次 Audit() schema = %+v", baseReport.Schema)
	}
	if runsAfter := recoveryRunCount(t, ctx, pool); runsAfter != runsBefore {
		t.Fatalf("只读审计写入了 reconciliation_runs: before=%d after=%d", runsBefore, runsAfter)
	}

	incompatible, err := store.Audit(ctx, expectedVersion+1)
	if err != nil {
		t.Fatalf("不兼容版本 Audit() error = %v", err)
	}
	if incompatible.Passed || incompatible.Schema.Compatible || incompatible.CriticalCount != 1 {
		t.Fatalf("不兼容版本未阻断恢复: %+v", incompatible)
	}

	uniqueID := recoveryUUID(t)
	assetID := "recovery-asset-" + uniqueID
	walletID := recoveryUUID(t)
	accountID := recoveryUUID(t)
	if _, err := pool.Exec(ctx, `
		INSERT INTO assets (id, network, contract_address, symbol, decimals, status)
		VALUES ($1, $2, $3, 'USDT', 6, 'active')
	`, assetID, "recovery-network-"+uniqueID, "contract-"+uniqueID); err != nil {
		t.Fatalf("创建恢复审计测试资产: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO custody_wallets (id, asset_id, address, role, status)
		VALUES ($1, $2, $3, 'deposit', 'active')
	`, walletID, assetID, "wallet-"+uniqueID); err != nil {
		t.Fatalf("创建恢复审计测试钱包: %v", err)
	}

	missingSnapshot, err := store.Audit(ctx, expectedVersion)
	if err != nil {
		t.Fatalf("缺少快照 Audit() error = %v", err)
	}
	if missingSnapshot.Passed || !slices.Contains(missingSnapshot.MissingWalletSnapshots, assetID) {
		t.Fatalf("缺少钱包快照未阻断恢复: %+v", missingSnapshot)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO ledger_accounts (id, owner_type, owner_id, asset_id, code, normal_side, status)
		VALUES ($1, 'platform', 'gateway', $2, 'custody', 'D', 'active')
	`, accountID, assetID); err != nil {
		t.Fatalf("创建恢复审计托管科目: %v", err)
	}
	snapshotID := recoveryUUID(t)
	if _, err := pool.Exec(ctx, `
		INSERT INTO wallet_balance_snapshot_runs (
			id, asset_id, block_height, block_hash, block_time, wallet_count, total_balance,
			ledger_account_id, ledger_entry_count, ledger_balance
		) VALUES ($1, $2, 1, $3, CURRENT_TIMESTAMP, 1, 0, $4, 0, 0)
	`, snapshotID, assetID, strings.Repeat("a", 64), accountID); err != nil {
		t.Fatalf("创建恢复审计钱包快照运行: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO wallet_balance_snapshots (run_id, wallet_id, asset_id, balance)
		VALUES ($1, $2, $3, 0)
	`, snapshotID, walletID, assetID); err != nil {
		t.Fatalf("创建恢复审计钱包余额快照: %v", err)
	}
	resolvedSnapshot, err := store.Audit(ctx, expectedVersion)
	if err != nil {
		t.Fatalf("补齐快照 Audit() error = %v", err)
	}
	if slices.Contains(resolvedSnapshot.MissingWalletSnapshots, assetID) {
		t.Fatalf("已补齐的钱包快照仍被报告缺失: %+v", resolvedSnapshot.MissingWalletSnapshots)
	}

	leaseBefore := resolvedSnapshot.RecoverableLeases["indexer"]
	if _, err := pool.Exec(ctx, `
		INSERT INTO chain_scan_cursors (
			network, start_height, anchor_hash, next_height, previous_hash, lease_owner, lease_until
		) VALUES ($1, 1, 'anchor', 1, 'previous', 'crashed-worker', CURRENT_TIMESTAMP - INTERVAL '1 minute')
	`, "recovery-lease-"+uniqueID); err != nil {
		t.Fatalf("创建恢复审计过期租约: %v", err)
	}
	withLease, err := store.Audit(ctx, expectedVersion)
	if err != nil {
		t.Fatalf("过期租约 Audit() error = %v", err)
	}
	if withLease.RecoverableLeases["indexer"] != leaseBefore+1 {
		t.Fatalf("过期租约数量=%d want=%d", withLease.RecoverableLeases["indexer"], leaseBefore+1)
	}
	if withLease.WarningCount <= resolvedSnapshot.WarningCount {
		t.Fatalf("过期租约未增加告警: before=%d after=%d", resolvedSnapshot.WarningCount, withLease.WarningCount)
	}
	if runsAfter := recoveryRunCount(t, ctx, pool); runsAfter != runsBefore {
		t.Fatalf("只读审计写入了 reconciliation_runs: before=%d after=%d", runsBefore, runsAfter)
	}
}

func recoveryRunCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int64 {
	t.Helper()
	var count int64
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM reconciliation_runs`).Scan(&count); err != nil {
		t.Fatalf("统计 reconciliation_runs: %v", err)
	}
	return count
}

func recoveryUUID(t *testing.T) string {
	t.Helper()
	value, err := identity.NewUUID()
	if err != nil {
		t.Fatalf("生成恢复审计测试标识: %v", err)
	}
	return value
}
