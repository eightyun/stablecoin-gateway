package sweep

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/eightyun/stablecoin-gateway/internal/identity"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const transactionRetryLimit = 3

// Store 使用 PostgreSQL 原子生成不可变归集计划。
type Store struct {
	db *pgxpool.Pool
}

// NewStore 创建归集计划 Store。
func NewStore(db *pgxpool.Pool) (*Store, error) {
	if db == nil {
		return nil, ErrDatabaseRequired
	}
	return &Store{db: db}, nil
}

// PlanNext 为最新固化快照中的一个安全候选生成计划。
func (store *Store) PlanNext(ctx context.Context, policy Policy) (Plan, error) {
	policy.AssetID = strings.TrimSpace(policy.AssetID)
	policy.MinimumAmount = strings.TrimSpace(policy.MinimumAmount)
	if err := validatePolicy(policy); err != nil {
		return Plan{}, err
	}
	minimumAmount, _ := new(big.Int).SetString(policy.MinimumAmount, 10)
	policy.MinimumAmount = minimumAmount.String()
	var lastErr error
	for range transactionRetryLimit {
		plan, err := store.planNext(ctx, policy)
		if !isRetryableTransactionError(err) {
			return plan, err
		}
		lastErr = err
		if ctx.Err() != nil {
			return Plan{}, ctx.Err()
		}
	}
	return Plan{}, lastErr
}

func (store *Store) planNext(ctx context.Context, policy Policy) (plan Plan, err error) {
	transaction, err := store.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return Plan{}, fmt.Errorf("开始归集规划事务: %w", err)
	}
	defer func() {
		if rollbackErr := transaction.Rollback(context.Background()); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			err = errors.Join(err, fmt.Errorf("回滚归集规划事务: %w", rollbackErr))
		}
	}()
	var assetExists bool
	if err = transaction.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM assets WHERE id = $1 AND status = 'active'
		)
	`, policy.AssetID).Scan(&assetExists); err != nil {
		return Plan{}, fmt.Errorf("查询归集资产: %w", err)
	}
	if !assetExists {
		return Plan{}, ErrAssetUnavailable
	}

	var snapshotRunID string
	var snapshotBlockHeight int64
	if err = transaction.QueryRow(ctx, `
		SELECT run.id::TEXT, run.block_height
		FROM wallet_balance_snapshot_runs AS run
		JOIN assets AS asset ON asset.id = run.asset_id AND asset.status = 'active'
		WHERE run.asset_id = $1
		  AND run.captured_at >= clock_timestamp() - ($2 * INTERVAL '1 millisecond')
		ORDER BY run.block_height DESC, run.captured_at DESC, run.id DESC
		LIMIT 1
	`, policy.AssetID, policy.MaxSnapshotAge.Milliseconds()).Scan(&snapshotRunID, &snapshotBlockHeight); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Plan{}, ErrNoCandidate
		}
		return Plan{}, fmt.Errorf("查询最新钱包快照: %w", err)
	}

	var hotCount int
	if err = transaction.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM custody_wallets AS wallet
		WHERE wallet.asset_id = $1 AND wallet.role = 'hot' AND wallet.status = 'active'
	`, policy.AssetID).Scan(&hotCount); err != nil {
		return Plan{}, fmt.Errorf("统计活动热钱包: %w", err)
	}
	if hotCount != 1 {
		return Plan{}, ErrHotWalletCount
	}

	plan.ID, err = identity.NewUUID()
	if err != nil {
		return Plan{}, err
	}
	plan.AssetID = policy.AssetID
	plan.SnapshotRunID = snapshotRunID
	plan.SnapshotBlockHeight = snapshotBlockHeight
	plan.MinimumAmount = policy.MinimumAmount
	err = transaction.QueryRow(ctx, `
		WITH hot_wallet AS (
			SELECT wallet.id, wallet.address
			FROM custody_wallets AS wallet
			JOIN wallet_balance_snapshots AS balance
			  ON balance.wallet_id = wallet.id AND balance.run_id = $2
			WHERE wallet.asset_id = $1 AND wallet.role = 'hot' AND wallet.status = 'active'
		), candidate AS (
			SELECT source.id, source.address, balance.balance
			FROM custody_wallets AS source
			JOIN wallet_balance_snapshots AS balance
			  ON balance.wallet_id = source.id AND balance.run_id = $2
			JOIN assets AS asset ON asset.id = source.asset_id AND asset.status = 'active'
			JOIN chain_scan_cursors AS cursor
			  ON cursor.network = asset.network
			 AND cursor.tracked_contract = asset.contract_address
			 AND cursor.next_height > $3
			WHERE source.asset_id = $1
			  AND source.role = 'deposit'
			  AND source.status = 'active'
			  AND balance.balance >= $4::NUMERIC
			  AND NOT EXISTS (
				SELECT 1 FROM sweep_plans AS existing
				WHERE existing.source_wallet_id = source.id
			  )
			  AND EXISTS (
				SELECT 1
				FROM chain_events AS event
				WHERE event.network = asset.network
				  AND event.contract = asset.contract_address
				  AND event.to_address = source.address
				  AND event.block_height <= $3
			  )
			  AND NOT EXISTS (
				SELECT 1
				FROM chain_events AS event
				LEFT JOIN deposit_event_matches AS event_match
				  ON event_match.network = event.network
				 AND event_match.contract = event.contract
				 AND event_match.transaction_id = event.transaction_id
				 AND event_match.log_index = event.log_index
				LEFT JOIN deposit_screening_jobs AS screening_job
				  ON screening_job.network = event.network
				 AND screening_job.contract = event.contract
				 AND screening_job.transaction_id = event.transaction_id
				 AND screening_job.log_index = event.log_index
				LEFT JOIN deposit_screening_results AS screening_result
				  ON screening_result.id = screening_job.current_result_id
				WHERE event.network = asset.network
				  AND event.contract = asset.contract_address
				  AND event.to_address = source.address
				  AND event.block_height <= $3
				  AND (
					event_match.status IS DISTINCT FROM 'matched'
					OR event_match.deposit_address_id IS DISTINCT FROM source.id
					OR event_match.amount IS DISTINCT FROM event.amount
					OR screening_job.source_address IS DISTINCT FROM event.from_address
					OR screening_job.destination_address IS DISTINCT FROM source.address
					OR screening_job.status IS DISTINCT FROM 'closed'
					OR screening_result.decision IS DISTINCT FROM 'allow'
					OR screening_result.valid_until <= clock_timestamp()
				  )
			  )
			  AND balance.balance = (
				SELECT COALESCE(SUM(event.amount), 0)
				FROM chain_events AS event
				WHERE event.network = asset.network
				  AND event.contract = asset.contract_address
				  AND event.to_address = source.address
				  AND event.block_height <= $3
			  )
			ORDER BY balance.balance DESC, source.id
			FOR UPDATE OF source SKIP LOCKED
			LIMIT 1
		)
		INSERT INTO sweep_plans (
			id, asset_id, source_wallet_id, source_address,
			destination_wallet_id, destination_address, snapshot_run_id,
			snapshot_block_height, amount, minimum_amount
		)
		SELECT $5, $1, candidate.id, candidate.address,
		       hot_wallet.id, hot_wallet.address, $2, $3,
		       candidate.balance, $4::NUMERIC
		FROM candidate CROSS JOIN hot_wallet
		ON CONFLICT (source_wallet_id, snapshot_run_id) DO NOTHING
		RETURNING source_wallet_id::TEXT, source_address,
		          destination_wallet_id::TEXT, destination_address,
		          amount::TEXT, created_at
	`, policy.AssetID, snapshotRunID, snapshotBlockHeight, policy.MinimumAmount, plan.ID).Scan(
		&plan.SourceWalletID, &plan.SourceAddress, &plan.DestinationWalletID,
		&plan.DestinationAddress, &plan.Amount, &plan.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Plan{}, ErrNoCandidate
		}
		return Plan{}, fmt.Errorf("生成归集计划: %w", err)
	}
	if err = transaction.Commit(ctx); err != nil {
		return Plan{}, fmt.Errorf("提交归集规划事务: %w", err)
	}
	return plan, nil
}

func isRetryableTransactionError(err error) bool {
	var databaseError *pgconn.PgError
	return errors.As(err, &databaseError) && (databaseError.Code == "40001" || databaseError.Code == "40P01")
}
