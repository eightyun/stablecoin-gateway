//go:build integration

package monitoring

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/eightyun/stablecoin-gateway/internal/database"
	"github.com/eightyun/stablecoin-gateway/internal/identity"
	"github.com/jackc/pgx/v5"
)

func TestStoreSnapshotAgainstMigratedDatabase(t *testing.T) {
	databaseURL := os.Getenv("GATEWAY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("未设置 GATEWAY_TEST_DATABASE_URL")
	}
	pool, err := database.Open(context.Background(), database.DefaultConfig(databaseURL, "monitoring-store-test"))
	if err != nil {
		t.Fatalf("连接测试数据库: %v", err)
	}
	defer pool.Close()
	ctx := context.Background()
	uniqueID, err := identity.NewUUID()
	if err != nil {
		t.Fatalf("生成测试标识: %v", err)
	}
	network := "monitoring-" + uniqueID
	assetID := "monitoring-asset-" + uniqueID
	walletID := mustMonitoringUUID(t)
	sourceWalletID := mustMonitoringUUID(t)
	blockedWalletID := mustMonitoringUUID(t)
	snapshotID := mustMonitoringUUID(t)
	pendingSweepScreeningID := mustMonitoringUUID(t)
	blockedSweepScreeningID := mustMonitoringUUID(t)
	blockedSweepResultID := mustMonitoringUUID(t)
	reconciliationRunID := mustMonitoringUUID(t)
	globalWalletRunID := mustMonitoringUUID(t)
	scopedWalletRunID := mustMonitoringUUID(t)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("开始准备测试数据事务: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	mustMonitoringExec(t, ctx, tx, `
		INSERT INTO assets (id, network, contract_address, symbol, decimals, status)
		VALUES ($1, $2, $3, 'USDT', 6, 'active')
	`, assetID, network, "contract-"+uniqueID)
	mustMonitoringExec(t, ctx, tx, `
		INSERT INTO custody_wallets (id, asset_id, address, role, status)
		VALUES ($1, $2, $3, 'hot', 'active'),
		       ($4, $2, $5, 'deposit', 'active'),
		       ($6, $2, $7, 'deposit', 'active')
	`, walletID, assetID, "wallet-"+uniqueID,
		sourceWalletID, "source-"+uniqueID,
		blockedWalletID, "blocked-"+uniqueID)
	mustMonitoringExec(t, ctx, tx, `
		INSERT INTO custody_wallet_registration_audits (id, wallet_id, actor, reason)
		VALUES ($1, $2, 'monitoring-test', 'business monitoring integration fixture'),
		       ($3, $4, 'monitoring-test', 'pending sweep screening fixture'),
		       ($5, $6, 'monitoring-test', 'blocked sweep screening fixture')
	`, mustMonitoringUUID(t), walletID, mustMonitoringUUID(t), sourceWalletID,
		mustMonitoringUUID(t), blockedWalletID)
	mustMonitoringExec(t, ctx, tx, `
		INSERT INTO wallet_balance_snapshot_runs (
			id, asset_id, block_height, block_hash, block_time, wallet_count, total_balance
		) VALUES ($1, $2, 100, $3, CURRENT_TIMESTAMP, 3, 12)
	`, snapshotID, assetID, strings.Repeat("a", 64))
	mustMonitoringExec(t, ctx, tx, `
		INSERT INTO wallet_balance_snapshots (run_id, wallet_id, asset_id, balance)
		VALUES ($1, $2, $3, 0), ($1, $4, $3, 5), ($1, $5, $3, 7)
	`, snapshotID, walletID, assetID, sourceWalletID, blockedWalletID)
	mustMonitoringExec(t, ctx, tx, `
		INSERT INTO sweep_screening_jobs (
			id, asset_id, source_wallet_id, source_address,
			destination_wallet_id, destination_address, snapshot_run_id,
			snapshot_block_height, amount, status
		) VALUES ($1, $2, $3, $4, $5, $6, $7, 100, 5, 'pending')
	`, pendingSweepScreeningID, assetID, sourceWalletID, "source-"+uniqueID,
		walletID, "wallet-"+uniqueID, snapshotID)
	mustMonitoringExec(t, ctx, tx, `
		INSERT INTO sweep_screening_jobs (
			id, asset_id, source_wallet_id, source_address,
			destination_wallet_id, destination_address, snapshot_run_id,
			snapshot_block_height, amount, status, lease_owner, lease_until,
			lease_epoch, attempts
		) VALUES ($1, $2, $3, $4, $5, $6, $7, 100, 7, 'processing',
		          'monitoring-test', CURRENT_TIMESTAMP + INTERVAL '1 minute', 1, 1)
	`, blockedSweepScreeningID, assetID, blockedWalletID, "blocked-"+uniqueID,
		walletID, "wallet-"+uniqueID, snapshotID)
	mustMonitoringExec(t, ctx, tx, `
		INSERT INTO sweep_screening_results (
			id, job_id, attempt, provider, decision, reason_codes,
			provider_reference, response_hash, checked_at, valid_until
		) VALUES ($1, $2, 1, 'monitoring-test', 'deny', '["risk_match"]'::jsonb,
		          $3, $4, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP + INTERVAL '1 hour')
	`, blockedSweepResultID, blockedSweepScreeningID, "provider-"+uniqueID, strings.Repeat("b", 64))
	mustMonitoringExec(t, ctx, tx, `
		UPDATE sweep_screening_jobs
		SET status = 'completed', current_result_id = $2,
		    lease_owner = NULL, lease_until = NULL, updated_at = clock_timestamp()
		WHERE id = $1
	`, blockedSweepScreeningID, blockedSweepResultID)
	mustMonitoringExec(t, ctx, tx, `
		INSERT INTO chain_scan_cursors (
			network, start_height, anchor_hash, next_height, previous_hash
		) VALUES ($1, 100, 'anchor', 101, 'previous')
	`, network)
	mustMonitoringExec(t, ctx, tx, `
		INSERT INTO outbox_events (
			id, topic, aggregate_type, aggregate_id, payload, status
		) VALUES ($1, 'monitoring.test', 'test', $2, '{}'::jsonb, 'dead')
	`, mustMonitoringUUID(t), uniqueID)
	mustMonitoringExec(t, ctx, tx, `
		INSERT INTO reconciliation_runs (
			id, kind, asset_id, snapshot_at, checked_items, finding_count
		) VALUES
			($1, 'ledger_integrity', NULL, CURRENT_TIMESTAMP, 1, 0),
			($2, 'wallet_assets', NULL, CURRENT_TIMESTAMP - INTERVAL '1 hour', 1, 0),
			($3, 'wallet_assets', $4, CURRENT_TIMESTAMP, 1, 0)
	`, reconciliationRunID, globalWalletRunID, scopedWalletRunID, assetID)
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("提交测试数据: %v", err)
	}
	store, err := NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	snapshot, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	for _, status := range payoutStatuses {
		if _, exists := snapshot.Payouts[status]; !exists {
			t.Fatalf("Payouts 缺少固定状态 %q", status)
		}
	}
	for _, status := range outboxStatuses {
		if _, exists := snapshot.Outbox[status]; !exists {
			t.Fatalf("Outbox 缺少固定状态 %q", status)
		}
	}
	for _, kind := range reconciliationKinds {
		if _, exists := snapshot.LastReconciliationRun[kind]; !exists {
			t.Fatalf("LastReconciliationRun 缺少固定类型 %q", kind)
		}
	}
	if cursor := snapshot.IndexerCursors[network]; cursor.NextHeight != 101 || cursor.UpdatedUnixTime <= 0 {
		t.Fatalf("IndexerCursors[%q] = %+v", network, cursor)
	}
	if snapshot.Outbox["dead"].Count < 1 || snapshot.Outbox["dead"].OldestCreatedUnixTime <= 0 {
		t.Fatalf("Outbox[dead] = %+v", snapshot.Outbox["dead"])
	}
	if snapshot.LastReconciliationRun["ledger_integrity"] <= 0 {
		t.Fatalf("LastReconciliationRun = %+v", snapshot.LastReconciliationRun)
	}
	var expectedGlobalWalletRun float64
	if err := pool.QueryRow(ctx, `
		SELECT EXTRACT(EPOCH FROM MAX(snapshot_at))::double precision
		FROM reconciliation_runs
		WHERE kind = 'wallet_assets' AND asset_id IS NULL
	`).Scan(&expectedGlobalWalletRun); err != nil {
		t.Fatalf("读取全局钱包对账时间: %v", err)
	}
	if snapshot.LastReconciliationRun["wallet_assets"] != expectedGlobalWalletRun {
		t.Fatalf("定向对账误刷新全局时间 LastReconciliationRun=%+v want=%v",
			snapshot.LastReconciliationRun, expectedGlobalWalletRun)
	}
	if snapshot.LastWalletSnapshot[assetID] <= 0 {
		t.Fatalf("LastWalletSnapshot[%q] = %v", assetID, snapshot.LastWalletSnapshot[assetID])
	}
	if snapshot.SweepScreeningJobs["pending"].Count < 1 ||
		snapshot.SweepScreeningJobs["pending"].OldestCreatedUnixTime <= 0 {
		t.Fatalf("SweepScreeningJobs[pending] = %+v", snapshot.SweepScreeningJobs["pending"])
	}
	if snapshot.SweepScreeningDecisions["deny"] < 1 {
		t.Fatalf("SweepScreeningDecisions = %+v", snapshot.SweepScreeningDecisions)
	}
}

func mustMonitoringUUID(t *testing.T) string {
	t.Helper()
	value, err := identity.NewUUID()
	if err != nil {
		t.Fatalf("生成测试 UUID: %v", err)
	}
	return value
}

func mustMonitoringExec(t *testing.T, ctx context.Context, tx pgx.Tx, query string, arguments ...any) {
	t.Helper()
	if _, err := tx.Exec(ctx, query, arguments...); err != nil {
		t.Fatalf("准备业务监控测试数据: %v", err)
	}
}
