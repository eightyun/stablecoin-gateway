package monitoring

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrStoreRequired = errors.New("业务监控 Store 需要数据库连接")

// Store 从 PostgreSQL 读取业务风险快照，不修改业务数据。
type Store struct {
	db *pgxpool.Pool
}

// NewStore 创建业务监控 Store。
func NewStore(db *pgxpool.Pool) (*Store, error) {
	if db == nil {
		return nil, ErrStoreRequired
	}
	return &Store{db: db}, nil
}

// Snapshot 在可重复读只读事务中读取所有指标，避免跨查询时间漂移。
func (store *Store) Snapshot(ctx context.Context) (Snapshot, error) {
	tx, err := store.db.BeginTx(ctx, pgx.TxOptions{
		IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return Snapshot{}, fmt.Errorf("开始业务监控快照事务: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	snapshot := emptySnapshot()
	if err := readReconciliationCases(ctx, tx, snapshot.OpenReconciliationCases); err != nil {
		return Snapshot{}, err
	}
	if err := readPayouts(ctx, tx, snapshot.Payouts); err != nil {
		return Snapshot{}, err
	}
	if err := readSweepExecutions(ctx, tx, snapshot.SweepExecutions); err != nil {
		return Snapshot{}, err
	}
	if err := readOutbox(ctx, tx, snapshot.Outbox); err != nil {
		return Snapshot{}, err
	}
	if err := readScreening(ctx, tx, snapshot.ScreeningJobs, snapshot.ScreeningDecisions); err != nil {
		return Snapshot{}, err
	}
	if err := readDepositScreening(ctx, tx, snapshot.DepositScreeningJobs, snapshot.DepositScreeningDecisions); err != nil {
		return Snapshot{}, err
	}
	if err := readCursors(ctx, tx, snapshot.IndexerCursors); err != nil {
		return Snapshot{}, err
	}
	if err := readReconciliationRuns(ctx, tx, snapshot.LastReconciliationRun); err != nil {
		return Snapshot{}, err
	}
	if err := readWalletSnapshots(ctx, tx, snapshot.LastWalletSnapshot); err != nil {
		return Snapshot{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Snapshot{}, fmt.Errorf("提交业务监控快照事务: %w", err)
	}
	return snapshot, nil
}

func emptySnapshot() Snapshot {
	snapshot := Snapshot{
		OpenReconciliationCases:   make(map[string]int64, len(reconciliationSeverity)),
		Payouts:                   make(map[string]CountAndOldest, len(payoutStatuses)),
		SweepExecutions:           make(map[string]CountAndOldest, len(sweepExecutionStatuses)),
		Outbox:                    make(map[string]CountAndOldest, len(outboxStatuses)),
		IndexerCursors:            make(map[string]CursorSnapshot),
		LastReconciliationRun:     make(map[string]float64, len(reconciliationKinds)),
		LastWalletSnapshot:        make(map[string]float64),
		ScreeningJobs:             make(map[string]CountAndOldest, len(screeningJobStatuses)),
		ScreeningDecisions:        make(map[string]int64, len(screeningDecisions)),
		DepositScreeningJobs:      make(map[string]CountAndOldest, len(screeningJobStatuses)),
		DepositScreeningDecisions: make(map[string]int64, len(screeningDecisions)),
	}
	for _, severity := range reconciliationSeverity {
		snapshot.OpenReconciliationCases[severity] = 0
	}
	for _, status := range payoutStatuses {
		snapshot.Payouts[status] = CountAndOldest{}
	}
	for _, status := range sweepExecutionStatuses {
		snapshot.SweepExecutions[status] = CountAndOldest{}
	}
	for _, status := range outboxStatuses {
		snapshot.Outbox[status] = CountAndOldest{}
	}
	for _, kind := range reconciliationKinds {
		snapshot.LastReconciliationRun[kind] = 0
	}
	for _, status := range screeningJobStatuses {
		snapshot.ScreeningJobs[status] = CountAndOldest{}
	}
	for _, decision := range screeningDecisions {
		snapshot.ScreeningDecisions[decision] = 0
		snapshot.DepositScreeningDecisions[decision] = 0
	}
	for _, status := range screeningJobStatuses {
		snapshot.DepositScreeningJobs[status] = CountAndOldest{}
	}
	return snapshot
}

func readSweepExecutions(ctx context.Context, tx pgx.Tx, target map[string]CountAndOldest) error {
	rows, err := tx.Query(ctx, `
		SELECT execution.status, COUNT(*),
		       COALESCE(EXTRACT(EPOCH FROM MIN(
		           CASE execution.status
		               WHEN 'ready_for_broadcast' THEN execution.updated_at
		               WHEN 'confirming' THEN execution.broadcasted_at
		               WHEN 'failed' THEN execution.failed_at
		               ELSE execution.created_at
		           END
		       )), 0)::double precision
		FROM sweep_executions AS execution
		WHERE execution.status IN ('planned', 'ready_for_broadcast', 'confirming', 'failed')
		GROUP BY execution.status
	`)
	if err != nil {
		return fmt.Errorf("查询归集执行状态指标: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var value CountAndOldest
		if err := rows.Scan(&status, &value.Count, &value.OldestCreatedUnixTime); err != nil {
			return fmt.Errorf("读取归集执行状态指标: %w", err)
		}
		target[status] = value
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("遍历归集执行状态指标: %w", err)
	}
	return nil
}

func readDepositScreening(
	ctx context.Context,
	tx pgx.Tx,
	jobs map[string]CountAndOldest,
	decisions map[string]int64,
) error {
	rows, err := tx.Query(ctx, `
		SELECT status, COUNT(*), COALESCE(EXTRACT(EPOCH FROM MIN(created_at)), 0)::double precision
		FROM deposit_screening_jobs
		WHERE status IN ('pending', 'processing')
		GROUP BY status
	`)
	if err != nil {
		return fmt.Errorf("查询入金地址筛查任务指标: %w", err)
	}
	for rows.Next() {
		var status string
		var value CountAndOldest
		if err := rows.Scan(&status, &value.Count, &value.OldestCreatedUnixTime); err != nil {
			rows.Close()
			return fmt.Errorf("读取入金地址筛查任务指标: %w", err)
		}
		jobs[status] = value
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("遍历入金地址筛查任务指标: %w", err)
	}
	rows.Close()

	rows, err = tx.Query(ctx, `
		SELECT result.decision, COUNT(*)
		FROM deposit_screening_jobs AS job
		JOIN deposit_screening_results AS result ON result.id = job.current_result_id
		LEFT JOIN deposit_event_matches AS match
		  ON match.network = job.network AND match.contract = job.contract
		 AND match.transaction_id = job.transaction_id AND match.log_index = job.log_index
		WHERE match.network IS NULL AND job.status = 'completed'
		  AND result.valid_until > CURRENT_TIMESTAMP
		  AND result.decision IN ('deny', 'review')
		GROUP BY result.decision
	`)
	if err != nil {
		return fmt.Errorf("查询入金地址筛查决策指标: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var decision string
		var count int64
		if err := rows.Scan(&decision, &count); err != nil {
			return fmt.Errorf("读取入金地址筛查决策指标: %w", err)
		}
		decisions[decision] = count
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("遍历入金地址筛查决策指标: %w", err)
	}
	return nil
}

func readReconciliationCases(ctx context.Context, tx pgx.Tx, target map[string]int64) error {
	rows, err := tx.Query(ctx, `
		SELECT severity, COUNT(*)
		FROM reconciliation_cases
		WHERE status = 'open'
		GROUP BY severity
	`)
	if err != nil {
		return fmt.Errorf("查询未关闭对账工单: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var severity string
		var count int64
		if err := rows.Scan(&severity, &count); err != nil {
			return fmt.Errorf("读取未关闭对账工单: %w", err)
		}
		target[severity] = count
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("遍历未关闭对账工单: %w", err)
	}
	return nil
}

func readPayouts(ctx context.Context, tx pgx.Tx, target map[string]CountAndOldest) error {
	rows, err := tx.Query(ctx, `
		SELECT status, COUNT(*), COALESCE(EXTRACT(EPOCH FROM MIN(created_at)), 0)::double precision
		FROM payouts
		WHERE status IN ('pending_review', 'approved', 'ready_for_broadcast', 'confirming')
		GROUP BY status
	`)
	if err != nil {
		return fmt.Errorf("查询出款状态指标: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var value CountAndOldest
		if err := rows.Scan(&status, &value.Count, &value.OldestCreatedUnixTime); err != nil {
			return fmt.Errorf("读取出款状态指标: %w", err)
		}
		target[status] = value
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("遍历出款状态指标: %w", err)
	}
	return nil
}

func readOutbox(ctx context.Context, tx pgx.Tx, target map[string]CountAndOldest) error {
	rows, err := tx.Query(ctx, `
		SELECT status, COUNT(*), COALESCE(EXTRACT(EPOCH FROM MIN(created_at)), 0)::double precision
		FROM outbox_events
		WHERE status IN ('pending', 'processing', 'dead')
		GROUP BY status
	`)
	if err != nil {
		return fmt.Errorf("查询 Outbox 状态指标: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var value CountAndOldest
		if err := rows.Scan(&status, &value.Count, &value.OldestCreatedUnixTime); err != nil {
			return fmt.Errorf("读取 Outbox 状态指标: %w", err)
		}
		target[status] = value
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("遍历 Outbox 状态指标: %w", err)
	}
	return nil
}

func readScreening(
	ctx context.Context,
	tx pgx.Tx,
	jobs map[string]CountAndOldest,
	decisions map[string]int64,
) error {
	rows, err := tx.Query(ctx, `
		SELECT status, COUNT(*), COALESCE(EXTRACT(EPOCH FROM MIN(created_at)), 0)::double precision
		FROM payout_screening_jobs
		WHERE status IN ('pending', 'processing')
		GROUP BY status
	`)
	if err != nil {
		return fmt.Errorf("查询地址筛查任务指标: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var value CountAndOldest
		if err := rows.Scan(&status, &value.Count, &value.OldestCreatedUnixTime); err != nil {
			return fmt.Errorf("读取地址筛查任务指标: %w", err)
		}
		jobs[status] = value
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("遍历地址筛查任务指标: %w", err)
	}
	rows.Close()

	rows, err = tx.Query(ctx, `
		SELECT result.decision, COUNT(*)
		FROM payout_screening_jobs AS job
		JOIN payouts AS payout ON payout.id = job.payout_id
		JOIN payout_screening_results AS result ON result.id = job.current_result_id
		WHERE payout.status = 'pending_review'
		  AND job.status = 'completed'
		  AND result.valid_until > CURRENT_TIMESTAMP
		  AND result.decision IN ('deny', 'review')
		GROUP BY result.decision
	`)
	if err != nil {
		return fmt.Errorf("查询地址筛查决策指标: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var decision string
		var count int64
		if err := rows.Scan(&decision, &count); err != nil {
			return fmt.Errorf("读取地址筛查决策指标: %w", err)
		}
		decisions[decision] = count
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("遍历地址筛查决策指标: %w", err)
	}
	return nil
}

func readCursors(ctx context.Context, tx pgx.Tx, target map[string]CursorSnapshot) error {
	rows, err := tx.Query(ctx, `
		SELECT network, next_height, EXTRACT(EPOCH FROM updated_at)::double precision
		FROM chain_scan_cursors
	`)
	if err != nil {
		return fmt.Errorf("查询链扫描游标指标: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var network string
		var cursor CursorSnapshot
		if err := rows.Scan(&network, &cursor.NextHeight, &cursor.UpdatedUnixTime); err != nil {
			return fmt.Errorf("读取链扫描游标指标: %w", err)
		}
		target[network] = cursor
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("遍历链扫描游标指标: %w", err)
	}
	return nil
}

func readReconciliationRuns(ctx context.Context, tx pgx.Tx, target map[string]float64) error {
	rows, err := tx.Query(ctx, `
		SELECT kind, EXTRACT(EPOCH FROM MAX(snapshot_at))::double precision
		FROM reconciliation_runs
		GROUP BY kind
	`)
	if err != nil {
		return fmt.Errorf("查询最近对账时间: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		var timestamp float64
		if err := rows.Scan(&kind, &timestamp); err != nil {
			return fmt.Errorf("读取最近对账时间: %w", err)
		}
		target[kind] = timestamp
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("遍历最近对账时间: %w", err)
	}
	return nil
}

func readWalletSnapshots(ctx context.Context, tx pgx.Tx, target map[string]float64) error {
	rows, err := tx.Query(ctx, `
		SELECT asset.id, COALESCE(EXTRACT(EPOCH FROM MAX(run.captured_at)), 0)::double precision
		FROM assets AS asset
		LEFT JOIN wallet_balance_snapshot_runs AS run ON run.asset_id = asset.id
		WHERE asset.status = 'active'
		  AND EXISTS (
		      SELECT 1
		      FROM custody_wallets AS wallet
		      WHERE wallet.asset_id = asset.id AND wallet.status = 'active'
		  )
		GROUP BY asset.id
	`)
	if err != nil {
		return fmt.Errorf("查询最近钱包快照时间: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var assetID string
		var timestamp float64
		if err := rows.Scan(&assetID, &timestamp); err != nil {
			return fmt.Errorf("读取最近钱包快照时间: %w", err)
		}
		target[assetID] = timestamp
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("遍历最近钱包快照时间: %w", err)
	}
	return nil
}
