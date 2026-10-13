// Package recovery 提供恢复数据库启用前的只读资金一致性审计。
package recovery

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/reconciliation"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrDatabaseRequired       = errors.New("恢复审计需要数据库连接")
	ErrExpectedVersionInvalid = errors.New("恢复审计要求的迁移版本无效")
)

// SchemaState 是恢复库和当前二进制之间的迁移兼容状态。
type SchemaState struct {
	Version         uint `json:"version"`
	ExpectedVersion uint `json:"expected_version"`
	Dirty           bool `json:"dirty"`
	Compatible      bool `json:"compatible"`
}

// Report 是单个 PostgreSQL 可重复读只读快照中的恢复验收报告。
type Report struct {
	AuditedAt               time.Time                  `json:"audited_at"`
	Schema                  SchemaState                `json:"schema"`
	Ledger                  reconciliation.AuditResult `json:"ledger"`
	WalletAssets            reconciliation.AuditResult `json:"wallet_assets"`
	MissingWalletSnapshots  []string                   `json:"missing_wallet_snapshots"`
	OpenReconciliationCases map[string]int64           `json:"open_reconciliation_cases"`
	PendingWork             map[string]int64           `json:"pending_work"`
	RecoverableLeases       map[string]int64           `json:"recoverable_expired_leases"`
	CriticalCount           int64                      `json:"critical_count"`
	WarningCount            int64                      `json:"warning_count"`
	Passed                  bool                       `json:"passed"`
}

// Store 从 PostgreSQL 生成恢复审计报告，不修改任何业务数据。
type Store struct {
	db *pgxpool.Pool
}

// NewStore 创建恢复审计 Store。
func NewStore(db *pgxpool.Pool) (*Store, error) {
	if db == nil {
		return nil, ErrDatabaseRequired
	}
	return &Store{db: db}, nil
}

// Audit 在单个只读快照内验证恢复库是否可安全进入启动流程。
func (store *Store) Audit(ctx context.Context, expectedVersion uint) (report Report, err error) {
	if expectedVersion == 0 {
		return Report{}, ErrExpectedVersionInvalid
	}
	report = newReport(expectedVersion)
	transaction, err := store.db.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return Report{}, fmt.Errorf("开始恢复审计只读事务: %w", err)
	}
	defer func() {
		if rollbackErr := transaction.Rollback(context.Background()); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			err = errors.Join(err, fmt.Errorf("回滚恢复审计事务: %w", rollbackErr))
		}
	}()
	if err = transaction.QueryRow(ctx, `SELECT CURRENT_TIMESTAMP`).Scan(&report.AuditedAt); err != nil {
		return Report{}, fmt.Errorf("读取恢复审计时间: %w", err)
	}
	var databaseVersion int64
	if err = transaction.QueryRow(ctx, `SELECT version, dirty FROM schema_migrations LIMIT 1`).Scan(
		&databaseVersion, &report.Schema.Dirty,
	); err != nil {
		return Report{}, fmt.Errorf("读取恢复库迁移版本: %w", err)
	}
	if databaseVersion <= 0 {
		return Report{}, fmt.Errorf("恢复库迁移版本无效: %d", databaseVersion)
	}
	report.Schema.Version = uint(databaseVersion)
	report.Schema.Compatible = report.Schema.Version == expectedVersion && !report.Schema.Dirty
	if !report.Schema.Compatible {
		report.CriticalCount = 1
		if err = transaction.Commit(ctx); err != nil {
			return Report{}, fmt.Errorf("提交恢复审计 schema 快照: %w", err)
		}
		return report, nil
	}

	report.Ledger, err = reconciliation.AuditLedgerIntegrityInTransaction(ctx, transaction)
	if err != nil {
		return Report{}, err
	}
	report.WalletAssets, err = reconciliation.AuditWalletAssetsInTransaction(ctx, transaction)
	if err != nil {
		return Report{}, err
	}
	if err = readMissingWalletSnapshots(ctx, transaction, &report); err != nil {
		return Report{}, err
	}
	if err = readOpenReconciliationCases(ctx, transaction, &report); err != nil {
		return Report{}, err
	}
	if err = readPendingWork(ctx, transaction, &report); err != nil {
		return Report{}, err
	}
	if err = readRecoverableLeases(ctx, transaction, &report); err != nil {
		return Report{}, err
	}

	report.CriticalCount += int64(report.Ledger.CriticalCount + report.WalletAssets.CriticalCount)
	report.CriticalCount += int64(len(report.MissingWalletSnapshots))
	report.CriticalCount += report.OpenReconciliationCases[reconciliation.SeverityCritical]
	report.WarningCount += int64(report.Ledger.WarningCount + report.WalletAssets.WarningCount)
	report.WarningCount += report.OpenReconciliationCases[reconciliation.SeverityWarning]
	for _, count := range report.RecoverableLeases {
		report.WarningCount += count
	}
	report.Passed = report.CriticalCount == 0
	if err = transaction.Commit(ctx); err != nil {
		return Report{}, fmt.Errorf("提交恢复审计只读事务: %w", err)
	}
	return report, nil
}

func newReport(expectedVersion uint) Report {
	return Report{
		Schema: SchemaState{ExpectedVersion: expectedVersion},
		Ledger: reconciliation.AuditResult{
			Kind: reconciliation.RunKindLedgerIntegrity, Findings: make([]reconciliation.AuditFinding, 0),
		},
		WalletAssets: reconciliation.AuditResult{
			Kind: reconciliation.RunKindWalletAssets, Findings: make([]reconciliation.AuditFinding, 0),
		},
		MissingWalletSnapshots:  make([]string, 0),
		OpenReconciliationCases: map[string]int64{reconciliation.SeverityCritical: 0, reconciliation.SeverityWarning: 0},
		PendingWork:             make(map[string]int64),
		RecoverableLeases:       make(map[string]int64),
	}
}

func readMissingWalletSnapshots(ctx context.Context, transaction pgx.Tx, report *Report) error {
	rows, err := transaction.Query(ctx, `
		SELECT asset.id
		FROM assets AS asset
		WHERE asset.status = 'active'
		  AND EXISTS (
		      SELECT 1 FROM custody_wallets AS wallet
		      WHERE wallet.asset_id = asset.id AND wallet.status = 'active'
		  )
		  AND NOT EXISTS (
		      SELECT 1 FROM wallet_balance_snapshot_runs AS snapshot
		      WHERE snapshot.asset_id = asset.id AND snapshot.ledger_account_id IS NOT NULL
		  )
		ORDER BY asset.id
	`)
	if err != nil {
		return fmt.Errorf("查询缺少检查点的钱包资产: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var assetID string
		if err := rows.Scan(&assetID); err != nil {
			return fmt.Errorf("读取缺少检查点的钱包资产: %w", err)
		}
		report.MissingWalletSnapshots = append(report.MissingWalletSnapshots, assetID)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("遍历缺少检查点的钱包资产: %w", err)
	}
	return nil
}

func readOpenReconciliationCases(ctx context.Context, transaction pgx.Tx, report *Report) error {
	rows, err := transaction.Query(ctx, `
		SELECT severity, COUNT(*)
		FROM reconciliation_cases
		WHERE status = 'open'
		GROUP BY severity
	`)
	if err != nil {
		return fmt.Errorf("查询恢复库开放对账工单: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var severity string
		var count int64
		if err := rows.Scan(&severity, &count); err != nil {
			return fmt.Errorf("读取恢复库开放对账工单: %w", err)
		}
		report.OpenReconciliationCases[severity] = count
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("遍历恢复库开放对账工单: %w", err)
	}
	return nil
}

func readPendingWork(ctx context.Context, transaction pgx.Tx, report *Report) error {
	rows, err := transaction.Query(ctx, `
		SELECT queue, COUNT(*)
		FROM (
		    SELECT 'payout.' || status AS queue
		    FROM payouts
		    WHERE status IN ('pending_review', 'approved', 'ready_for_broadcast', 'confirming')
		    UNION ALL
		    SELECT 'sweep.' || status
		    FROM sweep_executions
		    WHERE status IN ('planned', 'ready_for_broadcast', 'confirming')
		    UNION ALL
		    SELECT 'outbox.' || status
		    FROM outbox_events
		    WHERE status IN ('pending', 'processing')
		    UNION ALL
		    SELECT 'screening.payout.' || status
		    FROM payout_screening_jobs
		    WHERE status IN ('pending', 'processing')
		    UNION ALL
		    SELECT 'screening.deposit.' || status
		    FROM deposit_screening_jobs
		    WHERE status IN ('pending', 'processing')
		    UNION ALL
		    SELECT 'screening.sweep.' || status
		    FROM sweep_screening_jobs
		    WHERE status IN ('pending', 'processing')
		) AS pending
		GROUP BY queue
		ORDER BY queue
	`)
	if err != nil {
		return fmt.Errorf("查询恢复库待处理工作: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var queue string
		var count int64
		if err := rows.Scan(&queue, &count); err != nil {
			return fmt.Errorf("读取恢复库待处理工作: %w", err)
		}
		report.PendingWork[queue] = count
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("遍历恢复库待处理工作: %w", err)
	}
	return nil
}

func readRecoverableLeases(ctx context.Context, transaction pgx.Tx, report *Report) error {
	rows, err := transaction.Query(ctx, `
		SELECT queue, count
		FROM (
		    SELECT 'indexer' AS queue, COUNT(*) AS count
		    FROM chain_scan_cursors
		    WHERE lease_owner IS NOT NULL AND lease_until <= CURRENT_TIMESTAMP
		    UNION ALL
		    SELECT 'outbox', COUNT(*) FROM outbox_events
		    WHERE status = 'processing' AND lease_until <= CURRENT_TIMESTAMP
		    UNION ALL
		    SELECT 'payout_execution', COUNT(*) FROM payouts
		    WHERE execution_lease_owner IS NOT NULL AND execution_lease_until <= CURRENT_TIMESTAMP
		    UNION ALL
		    SELECT 'sweep_execution', COUNT(*) FROM sweep_executions
		    WHERE lease_owner IS NOT NULL AND lease_until <= CURRENT_TIMESTAMP
		    UNION ALL
		    SELECT 'payout_screening', COUNT(*) FROM payout_screening_jobs
		    WHERE status = 'processing' AND lease_until <= CURRENT_TIMESTAMP
		    UNION ALL
		    SELECT 'deposit_screening', COUNT(*) FROM deposit_screening_jobs
		    WHERE status = 'processing' AND lease_until <= CURRENT_TIMESTAMP
		    UNION ALL
		    SELECT 'sweep_screening', COUNT(*) FROM sweep_screening_jobs
		    WHERE status = 'processing' AND lease_until <= CURRENT_TIMESTAMP
		) AS leases
		WHERE count > 0
		ORDER BY queue
	`)
	if err != nil {
		return fmt.Errorf("查询恢复库可接管租约: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var queue string
		var count int64
		if err := rows.Scan(&queue, &count); err != nil {
			return fmt.Errorf("读取恢复库可接管租约: %w", err)
		}
		report.RecoverableLeases[queue] = count
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("遍历恢复库可接管租约: %w", err)
	}
	return nil
}
