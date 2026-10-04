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
	ErrNoInboundScreeningJob     = errors.New("没有待处理入金地址筛查任务")
	ErrInvalidInboundClaim       = errors.New("入金地址筛查租约无效")
	ErrInboundScreeningLeaseLost = errors.New("入金地址筛查租约已失效")
)

// InboundActionRequired 是需要运营隔离处置的当前有效入金筛查结果。
type InboundActionRequired struct {
	JobID              string    `json:"job_id"`
	Network            string    `json:"network"`
	Contract           string    `json:"contract"`
	TransactionID      string    `json:"transaction_id"`
	LogIndex           int64     `json:"log_index"`
	SourceAddress      string    `json:"source_address"`
	DestinationAddress string    `json:"destination_address"`
	Decision           string    `json:"decision"`
	ReasonCodes        []string  `json:"reason_codes"`
	Provider           string    `json:"provider"`
	ProviderReference  string    `json:"provider_reference"`
	CheckedAt          time.Time `json:"checked_at"`
	ValidUntil         time.Time `json:"valid_until"`
	Quarantined        bool      `json:"quarantined"`
	MatchReason        string    `json:"match_reason,omitempty"`
}

// InboundClaim 是带栅栏令牌的入金来源筛查任务。
type InboundClaim struct {
	JobID      string
	WorkerID   string
	LeaseEpoch int64
	Request    Request
}

// InboundStore 管理由已固化链事件发现的入金筛查任务。
type InboundStore struct {
	db *pgxpool.Pool
}

// NewInboundStore 创建入金筛查 Store。
func NewInboundStore(db *pgxpool.Pool) (*InboundStore, error) {
	if db == nil {
		return nil, ErrDatabaseRequired
	}
	return &InboundStore{db: db}, nil
}

// Claim 先发现一条尚未匹配的托管入金，再原子领取可处理任务。
func (store *InboundStore) Claim(ctx context.Context, workerID string, leaseDuration time.Duration) (claim InboundClaim, err error) {
	workerID = strings.TrimSpace(workerID)
	if workerID == "" || len(workerID) > 128 || leaseDuration.Milliseconds() <= 0 {
		return InboundClaim{}, ErrInvalidInboundClaim
	}
	tx, err := store.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return InboundClaim{}, fmt.Errorf("开始领取入金筛查任务事务: %w", err)
	}
	defer func() {
		if rollbackErr := tx.Rollback(context.Background()); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			err = errors.Join(err, fmt.Errorf("回滚领取入金筛查任务事务: %w", rollbackErr))
		}
	}()
	jobID, err := identity.NewUUID()
	if err != nil {
		return InboundClaim{}, err
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO deposit_screening_jobs (
			id, network, contract, transaction_id, log_index,
			source_address, destination_address, status
		)
		SELECT $1, event.network, event.contract, event.transaction_id, event.log_index,
		       event.from_address, event.to_address, 'pending'
		FROM chain_events AS event
		JOIN assets AS asset
		  ON asset.network = event.network AND asset.contract_address = event.contract
		JOIN deposit_addresses AS address
		  ON address.asset_id = asset.id AND address.address = event.to_address
		LEFT JOIN deposit_event_matches AS match
		  ON match.network = event.network AND match.contract = event.contract
		 AND match.transaction_id = event.transaction_id AND match.log_index = event.log_index
		LEFT JOIN deposit_screening_jobs AS existing
		  ON existing.network = event.network AND existing.contract = event.contract
		 AND existing.transaction_id = event.transaction_id AND existing.log_index = event.log_index
		WHERE match.network IS NULL AND existing.id IS NULL
		ORDER BY event.block_height, event.transaction_id, event.log_index
		LIMIT 1
		ON CONFLICT DO NOTHING
	`, jobID); err != nil {
		return InboundClaim{}, fmt.Errorf("发现入金筛查任务: %w", err)
	}
	claim.WorkerID = workerID
	err = tx.QueryRow(ctx, `
		WITH candidate AS (
			SELECT job.id, asset.id AS asset_id, event.amount::TEXT
			FROM deposit_screening_jobs AS job
			JOIN chain_events AS event
			  ON event.network = job.network AND event.contract = job.contract
			 AND event.transaction_id = job.transaction_id AND event.log_index = job.log_index
			JOIN assets AS asset
			  ON asset.network = job.network AND asset.contract_address = job.contract
			LEFT JOIN deposit_event_matches AS match
			  ON match.network = job.network AND match.contract = job.contract
			 AND match.transaction_id = job.transaction_id AND match.log_index = job.log_index
			LEFT JOIN deposit_screening_results AS current_result
			  ON current_result.id = job.current_result_id
			WHERE match.network IS NULL
			  AND (
			      job.status = 'pending'
			      OR (job.status = 'processing' AND job.lease_until <= clock_timestamp())
			      OR (job.status = 'completed' AND current_result.valid_until <= clock_timestamp())
			  )
			ORDER BY job.updated_at, job.id
			FOR UPDATE OF job SKIP LOCKED
			LIMIT 1
		)
		UPDATE deposit_screening_jobs AS job
		SET status = 'processing', lease_owner = $1,
		    lease_until = clock_timestamp() + ($2 * INTERVAL '1 millisecond'),
		    lease_epoch = job.lease_epoch + 1, attempts = job.attempts + 1,
		    last_error = NULL, updated_at = clock_timestamp()
		FROM candidate
		WHERE job.id = candidate.id
		RETURNING job.id::TEXT, job.lease_epoch, job.network, candidate.asset_id,
		          job.contract, job.source_address, job.destination_address, candidate.amount
	`, workerID, leaseDuration.Milliseconds()).Scan(
		&claim.JobID, &claim.LeaseEpoch, &claim.Request.Network, &claim.Request.AssetID,
		&claim.Request.ContractAddress, &claim.Request.SourceAddress,
		&claim.Request.DestinationAddress, &claim.Request.Amount,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return InboundClaim{}, ErrNoInboundScreeningJob
	}
	if err != nil {
		return InboundClaim{}, fmt.Errorf("领取入金地址筛查任务: %w", err)
	}
	claim.Request.RequestID = fmt.Sprintf("%s:%d", claim.JobID, claim.LeaseEpoch)
	claim.Request.Direction = DirectionInbound
	claim.Request.DepositScreeningID = claim.JobID
	if err = tx.Commit(ctx); err != nil {
		return InboundClaim{}, fmt.Errorf("提交领取入金筛查任务事务: %w", err)
	}
	return claim, nil
}

// Complete 在同一事务中保存不可变结果并推进任务指针。
func (store *InboundStore) Complete(ctx context.Context, claim InboundClaim, result Result) (err error) {
	if err := validateInboundClaim(claim); err != nil {
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
	tx, err := store.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return fmt.Errorf("开始保存入金筛查结果事务: %w", err)
	}
	defer func() {
		if rollbackErr := tx.Rollback(context.Background()); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			err = errors.Join(err, fmt.Errorf("回滚入金筛查结果事务: %w", rollbackErr))
		}
	}()
	var leaseValid bool
	err = tx.QueryRow(ctx, `
		SELECT status = 'processing' AND lease_owner = $2 AND lease_epoch = $3
		   AND lease_until > clock_timestamp()
		FROM deposit_screening_jobs
		WHERE id = $1
		FOR UPDATE
	`, claim.JobID, claim.WorkerID, claim.LeaseEpoch).Scan(&leaseValid)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !leaseValid) {
		return ErrInboundScreeningLeaseLost
	}
	if err != nil {
		return fmt.Errorf("锁定入金筛查任务: %w", err)
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO deposit_screening_results (
			id, job_id, attempt, provider, decision, reason_codes,
			provider_reference, response_hash, checked_at, valid_until
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`, resultID, claim.JobID, claim.LeaseEpoch, result.Provider, result.Decision,
		reasonCodes, result.ProviderReference, result.ResponseHash,
		result.CheckedAt.UTC(), result.ValidUntil.UTC()); err != nil {
		return fmt.Errorf("保存入金筛查结果: %w", err)
	}
	command, err := tx.Exec(ctx, `
		UPDATE deposit_screening_jobs
		SET status = 'completed', current_result_id = $4,
		    lease_owner = NULL, lease_until = NULL, last_error = NULL,
		    updated_at = clock_timestamp()
		WHERE id = $1 AND status = 'processing'
		  AND lease_owner = $2 AND lease_epoch = $3
	`, claim.JobID, claim.WorkerID, claim.LeaseEpoch, resultID)
	if err != nil {
		return fmt.Errorf("完成入金筛查任务: %w", err)
	}
	if command.RowsAffected() != 1 {
		return ErrInboundScreeningLeaseLost
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("提交入金筛查结果事务: %w", err)
	}
	return nil
}

// Release 记录可重试故障并释放当前租约。
func (store *InboundStore) Release(ctx context.Context, claim InboundClaim, failureCode string) error {
	if err := validateInboundClaim(claim); err != nil {
		return err
	}
	failureCode = strings.TrimSpace(failureCode)
	if !reasonCodePattern.MatchString(failureCode) {
		return ErrInvalidInboundClaim
	}
	command, err := store.db.Exec(ctx, `
		UPDATE deposit_screening_jobs
		SET status = 'pending', lease_owner = NULL, lease_until = NULL,
		    last_error = $4, updated_at = clock_timestamp()
		WHERE id = $1 AND status = 'processing'
		  AND lease_owner = $2 AND lease_epoch = $3
	`, claim.JobID, claim.WorkerID, claim.LeaseEpoch, failureCode)
	if err != nil {
		return fmt.Errorf("释放入金筛查任务: %w", err)
	}
	if command.RowsAffected() != 1 {
		return ErrInboundScreeningLeaseLost
	}
	return nil
}

// ListInboundActionRequired 返回待隔离或已隔离但仍需人工处置的 deny/review 结果。
func (store *InboundStore) ListInboundActionRequired(ctx context.Context, limit int) ([]InboundActionRequired, error) {
	if limit <= 0 || limit > 1000 {
		return nil, ErrInvalidListLimit
	}
	rows, err := store.db.Query(ctx, `
		SELECT job.id::TEXT, job.network, job.contract, job.transaction_id, job.log_index,
		       job.source_address, job.destination_address, result.decision,
		       result.reason_codes, result.provider, result.provider_reference,
		       result.checked_at, result.valid_until,
		       match.network IS NOT NULL, COALESCE(match.reason, '')
		FROM deposit_screening_jobs AS job
		JOIN deposit_screening_results AS result ON result.id = job.current_result_id
		LEFT JOIN deposit_event_matches AS match
		  ON match.network = job.network AND match.contract = job.contract
		 AND match.transaction_id = job.transaction_id AND match.log_index = job.log_index
		WHERE result.decision IN ('deny', 'review')
		  AND (
		      (match.network IS NULL AND job.status = 'completed'
		       AND result.valid_until > CURRENT_TIMESTAMP)
		      OR
		      (match.reason IN ('screening_denied', 'screening_review')
		       AND job.status = 'closed')
		  )
		ORDER BY result.checked_at DESC, job.id
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("查询待处置入金筛查结果: %w", err)
	}
	defer rows.Close()
	results := make([]InboundActionRequired, 0)
	for rows.Next() {
		var result InboundActionRequired
		var reasonCodes []byte
		if err := rows.Scan(
			&result.JobID, &result.Network, &result.Contract, &result.TransactionID,
			&result.LogIndex, &result.SourceAddress, &result.DestinationAddress,
			&result.Decision, &reasonCodes, &result.Provider, &result.ProviderReference,
			&result.CheckedAt, &result.ValidUntil, &result.Quarantined, &result.MatchReason,
		); err != nil {
			return nil, fmt.Errorf("读取待处置入金筛查结果: %w", err)
		}
		if err := json.Unmarshal(reasonCodes, &result.ReasonCodes); err != nil {
			return nil, fmt.Errorf("解析入金筛查原因码: %w", err)
		}
		results = append(results, result)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历待处置入金筛查结果: %w", err)
	}
	return results, nil
}

func validateInboundClaim(claim InboundClaim) error {
	if !identity.ValidUUID(strings.TrimSpace(claim.JobID)) || strings.TrimSpace(claim.WorkerID) == "" ||
		len(claim.WorkerID) > 128 || claim.LeaseEpoch <= 0 {
		return ErrInvalidInboundClaim
	}
	return nil
}
