package screening

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/identity"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNoSweepScreeningJob     = errors.New("没有待处理归集地址筛查任务")
	ErrInvalidSweepClaim       = errors.New("归集地址筛查租约无效")
	ErrSweepScreeningLeaseLost = errors.New("归集地址筛查租约已失效")
)

// SweepClaim 是绑定固化快照与精确金额的归集筛查租约。
type SweepClaim struct {
	JobID      string
	WorkerID   string
	LeaseEpoch int64
	Request    Request
}

// SweepStore 管理归集前地址筛查任务与不可变结果。
type SweepStore struct {
	db *pgxpool.Pool
}

// NewSweepStore 创建归集筛查 Store。
func NewSweepStore(db *pgxpool.Pool) (*SweepStore, error) {
	if db == nil {
		return nil, ErrDatabaseRequired
	}
	return &SweepStore{db: db}, nil
}

// Claim 原子领取待处理、租约过期或结果过期的归集筛查任务。
func (store *SweepStore) Claim(ctx context.Context, workerID string, leaseDuration time.Duration) (SweepClaim, error) {
	workerID = strings.TrimSpace(workerID)
	if workerID == "" || len(workerID) > 128 || leaseDuration.Milliseconds() <= 0 {
		return SweepClaim{}, ErrInvalidSweepClaim
	}
	claim := SweepClaim{WorkerID: workerID}
	err := store.db.QueryRow(ctx, `
		WITH candidate AS (
			SELECT job.id, asset.network, asset.contract_address
			FROM sweep_screening_jobs AS job
			JOIN assets AS asset ON asset.id = job.asset_id AND asset.status = 'active'
			LEFT JOIN sweep_screening_results AS current_result
			  ON current_result.id = job.current_result_id
			WHERE NOT EXISTS (
				SELECT 1
				FROM sweep_plans AS plan
				WHERE plan.source_wallet_id = job.source_wallet_id
				  AND plan.snapshot_run_id = job.snapshot_run_id
			)
			  AND (
			      job.status = 'pending'
			      OR (job.status = 'processing' AND job.lease_until <= clock_timestamp())
			      OR (job.status = 'completed' AND current_result.valid_until <= clock_timestamp())
			  )
			ORDER BY job.updated_at, job.id
			FOR UPDATE OF job SKIP LOCKED
			LIMIT 1
		)
		UPDATE sweep_screening_jobs AS job
		SET status = 'processing', lease_owner = $1,
		    lease_until = clock_timestamp() + ($2 * INTERVAL '1 millisecond'),
		    lease_epoch = job.lease_epoch + 1, attempts = job.attempts + 1,
		    last_error = NULL, updated_at = clock_timestamp()
		FROM candidate
		WHERE job.id = candidate.id
		RETURNING job.id::TEXT, job.lease_epoch, candidate.network,
		          job.asset_id, candidate.contract_address, job.source_address,
		          job.destination_address, job.amount::TEXT
	`, workerID, leaseDuration.Milliseconds()).Scan(
		&claim.JobID, &claim.LeaseEpoch, &claim.Request.Network,
		&claim.Request.AssetID, &claim.Request.ContractAddress,
		&claim.Request.SourceAddress, &claim.Request.DestinationAddress,
		&claim.Request.Amount,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return SweepClaim{}, ErrNoSweepScreeningJob
	}
	if err != nil {
		return SweepClaim{}, fmt.Errorf("领取归集地址筛查任务: %w", err)
	}
	claim.Request.RequestID = fmt.Sprintf("%s:%d", claim.JobID, claim.LeaseEpoch)
	claim.Request.Direction = DirectionSweep
	claim.Request.SweepScreeningID = claim.JobID
	return claim, nil
}

// Complete 在同一事务中保存不可变结果并推进任务指针。
func (store *SweepStore) Complete(ctx context.Context, claim SweepClaim, result Result) (err error) {
	if err := validateSweepClaim(claim); err != nil {
		return err
	}
	result = normalizeResult(result)
	if err := validateResult(result); err != nil {
		return err
	}
	reasonCodes, err := json.Marshal(result.ReasonCodes)
	if err != nil {
		return ErrInvalidResult
	}
	resultID, err := identity.NewUUID()
	if err != nil {
		return err
	}
	transaction, err := store.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return fmt.Errorf("开始保存归集筛查结果事务: %w", err)
	}
	defer func() {
		if rollbackErr := transaction.Rollback(context.Background()); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			err = errors.Join(err, fmt.Errorf("回滚归集筛查结果事务: %w", rollbackErr))
		}
	}()
	var leaseValid bool
	err = transaction.QueryRow(ctx, `
		SELECT status = 'processing' AND lease_owner = $2 AND lease_epoch = $3
		   AND lease_until > clock_timestamp()
		FROM sweep_screening_jobs
		WHERE id = $1
		FOR UPDATE
	`, claim.JobID, claim.WorkerID, claim.LeaseEpoch).Scan(&leaseValid)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !leaseValid) {
		return ErrSweepScreeningLeaseLost
	}
	if err != nil {
		return fmt.Errorf("锁定归集地址筛查任务: %w", err)
	}
	if _, err = transaction.Exec(ctx, `
		INSERT INTO sweep_screening_results (
			id, job_id, attempt, provider, decision, reason_codes,
			provider_reference, response_hash, checked_at, valid_until
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`, resultID, claim.JobID, claim.LeaseEpoch, result.Provider, result.Decision,
		reasonCodes, result.ProviderReference, result.ResponseHash,
		result.CheckedAt.UTC(), result.ValidUntil.UTC()); err != nil {
		return fmt.Errorf("保存归集地址筛查结果: %w", err)
	}
	command, err := transaction.Exec(ctx, `
		UPDATE sweep_screening_jobs
		SET status = 'completed', current_result_id = $4,
		    lease_owner = NULL, lease_until = NULL, last_error = NULL,
		    updated_at = clock_timestamp()
		WHERE id = $1 AND status = 'processing'
		  AND lease_owner = $2 AND lease_epoch = $3
	`, claim.JobID, claim.WorkerID, claim.LeaseEpoch, resultID)
	if err != nil {
		return fmt.Errorf("完成归集地址筛查任务: %w", err)
	}
	if command.RowsAffected() != 1 {
		return ErrSweepScreeningLeaseLost
	}
	if err = transaction.Commit(ctx); err != nil {
		return fmt.Errorf("提交归集地址筛查结果事务: %w", err)
	}
	return nil
}

// Release 记录可重试故障并释放当前租约。
func (store *SweepStore) Release(ctx context.Context, claim SweepClaim, failureCode string) error {
	if err := validateSweepClaim(claim); err != nil {
		return err
	}
	failureCode = strings.TrimSpace(failureCode)
	if !reasonCodePattern.MatchString(failureCode) {
		return ErrInvalidSweepClaim
	}
	command, err := store.db.Exec(ctx, `
		UPDATE sweep_screening_jobs
		SET status = 'pending', lease_owner = NULL, lease_until = NULL,
		    last_error = $4, updated_at = clock_timestamp()
		WHERE id = $1 AND status = 'processing'
		  AND lease_owner = $2 AND lease_epoch = $3
	`, claim.JobID, claim.WorkerID, claim.LeaseEpoch, failureCode)
	if err != nil {
		return fmt.Errorf("释放归集地址筛查任务: %w", err)
	}
	if command.RowsAffected() != 1 {
		return ErrSweepScreeningLeaseLost
	}
	return nil
}

func validateSweepClaim(claim SweepClaim) error {
	if !identity.ValidUUID(strings.TrimSpace(claim.JobID)) ||
		strings.TrimSpace(claim.WorkerID) == "" || len(strings.TrimSpace(claim.WorkerID)) > 128 ||
		claim.LeaseEpoch <= 0 {
		return ErrInvalidSweepClaim
	}
	return nil
}
