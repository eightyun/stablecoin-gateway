package screening

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/identity"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrDatabaseRequired      = errors.New("地址筛查 Store 需要数据库连接")
	ErrNoScreeningJob        = errors.New("没有待处理地址筛查任务")
	ErrInvalidScreeningClaim = errors.New("地址筛查租约无效")
	ErrScreeningLeaseLost    = errors.New("地址筛查租约已失效")
	ErrInvalidResult         = errors.New("地址筛查结果无效")
	ErrInvalidListLimit      = errors.New("地址筛查列表数量无效")
	reasonCodePattern        = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,127}$`)
	hashPattern              = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// ActionRequired 是需要运营拒绝或复核的当前有效筛查结果。
type ActionRequired struct {
	PayoutID          string    `json:"payout_id"`
	Network           string    `json:"network"`
	Address           string    `json:"address"`
	Decision          string    `json:"decision"`
	ReasonCodes       []string  `json:"reason_codes"`
	Provider          string    `json:"provider"`
	ProviderReference string    `json:"provider_reference"`
	CheckedAt         time.Time `json:"checked_at"`
	ValidUntil        time.Time `json:"valid_until"`
}

// Claim 是带栅栏令牌的出款筛查任务。
type Claim struct {
	PayoutID   string
	WorkerID   string
	LeaseEpoch int64
	Request    Request
}

// Store 管理出款筛查任务和不可变结果。
type Store struct {
	db *pgxpool.Pool
}

// NewStore 创建地址筛查 Store。
func NewStore(db *pgxpool.Pool) (*Store, error) {
	if db == nil {
		return nil, ErrDatabaseRequired
	}
	return &Store{db: db}, nil
}

// ListActionRequired 返回仍待人工处置的 deny/review 出款。
func (store *Store) ListActionRequired(ctx context.Context, limit int) ([]ActionRequired, error) {
	if limit <= 0 || limit > 1000 {
		return nil, ErrInvalidListLimit
	}
	rows, err := store.db.Query(ctx, `
		SELECT job.payout_id::TEXT, job.network, job.address,
		       result.decision, result.reason_codes, result.provider,
		       result.provider_reference, result.checked_at, result.valid_until
		FROM payout_screening_jobs AS job
		JOIN payouts AS payout ON payout.id = job.payout_id
		JOIN payout_screening_results AS result ON result.id = job.current_result_id
		WHERE payout.status = 'pending_review'
		  AND job.status = 'completed'
		  AND result.valid_until > CURRENT_TIMESTAMP
		  AND result.decision IN ('deny', 'review')
		ORDER BY result.checked_at, job.payout_id
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("查询待处置地址筛查结果: %w", err)
	}
	defer rows.Close()
	results := make([]ActionRequired, 0)
	for rows.Next() {
		var result ActionRequired
		var reasonCodes []byte
		if err := rows.Scan(
			&result.PayoutID, &result.Network, &result.Address,
			&result.Decision, &reasonCodes, &result.Provider,
			&result.ProviderReference, &result.CheckedAt, &result.ValidUntil,
		); err != nil {
			return nil, fmt.Errorf("读取待处置地址筛查结果: %w", err)
		}
		if err := json.Unmarshal(reasonCodes, &result.ReasonCodes); err != nil {
			return nil, fmt.Errorf("解析地址筛查原因码: %w", err)
		}
		results = append(results, result)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历待处置地址筛查结果: %w", err)
	}
	return results, nil
}

// Claim 原子领取待处理、租约过期或结果过期的出款筛查任务。
func (store *Store) Claim(ctx context.Context, workerID string, leaseDuration time.Duration) (Claim, error) {
	workerID = strings.TrimSpace(workerID)
	if workerID == "" || len(workerID) > 128 || leaseDuration.Milliseconds() <= 0 {
		return Claim{}, ErrInvalidScreeningClaim
	}
	claim := Claim{WorkerID: workerID}
	err := store.db.QueryRow(ctx, `
		WITH candidate AS (
			SELECT job.payout_id, asset.id AS asset_id, asset.contract_address,
			       payout.amount::TEXT
			FROM payout_screening_jobs AS job
			JOIN payouts AS payout ON payout.id = job.payout_id
			JOIN assets AS asset ON asset.id = payout.asset_id
			LEFT JOIN payout_screening_results AS current_result
			  ON current_result.id = job.current_result_id
			WHERE payout.status = 'pending_review'
			  AND (
			      job.status = 'pending'
			      OR (job.status = 'processing' AND job.lease_until <= clock_timestamp())
			      OR (job.status = 'completed' AND current_result.valid_until <= clock_timestamp())
			  )
			ORDER BY job.updated_at, job.payout_id
			FOR UPDATE OF job SKIP LOCKED
			LIMIT 1
		)
		UPDATE payout_screening_jobs AS job
		SET status = 'processing',
		    lease_owner = $1,
		    lease_until = clock_timestamp() + ($2 * INTERVAL '1 millisecond'),
		    lease_epoch = job.lease_epoch + 1,
		    attempts = job.attempts + 1,
		    last_error = NULL,
		    updated_at = clock_timestamp()
		FROM candidate
		WHERE job.payout_id = candidate.payout_id
		RETURNING job.payout_id::TEXT, job.lease_epoch, job.network, job.address,
		          candidate.asset_id, candidate.contract_address, candidate.amount
	`, workerID, leaseDuration.Milliseconds()).Scan(
		&claim.PayoutID, &claim.LeaseEpoch, &claim.Request.Network,
		&claim.Request.DestinationAddress, &claim.Request.AssetID,
		&claim.Request.ContractAddress, &claim.Request.Amount,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Claim{}, ErrNoScreeningJob
	}
	if err != nil {
		return Claim{}, fmt.Errorf("领取出款地址筛查任务: %w", err)
	}
	claim.Request.PayoutID = claim.PayoutID
	claim.Request.Direction = DirectionOutbound
	claim.Request.RequestID = fmt.Sprintf("%s:%d", claim.PayoutID, claim.LeaseEpoch)
	return claim, nil
}

// Complete 在同一事务中保存不可变结果并推进任务指针。
func (store *Store) Complete(ctx context.Context, claim Claim, result Result) (err error) {
	if err := validateClaim(claim); err != nil {
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
		return fmt.Errorf("开始保存地址筛查结果事务: %w", err)
	}
	defer func() {
		if rollbackErr := tx.Rollback(context.Background()); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			err = errors.Join(err, fmt.Errorf("回滚地址筛查结果事务: %w", rollbackErr))
		}
	}()
	var leaseValid bool
	err = tx.QueryRow(ctx, `
		SELECT status = 'processing'
		   AND lease_owner = $2
		   AND lease_epoch = $3
		   AND lease_until > clock_timestamp()
		FROM payout_screening_jobs
		WHERE payout_id = $1
		FOR UPDATE
	`, claim.PayoutID, claim.WorkerID, claim.LeaseEpoch).Scan(&leaseValid)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrScreeningLeaseLost
	}
	if err != nil {
		return fmt.Errorf("锁定地址筛查任务: %w", err)
	}
	if !leaseValid {
		return ErrScreeningLeaseLost
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO payout_screening_results (
			id, payout_id, attempt, provider, decision, reason_codes,
			provider_reference, response_hash, checked_at, valid_until
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`, resultID, claim.PayoutID, claim.LeaseEpoch, result.Provider, result.Decision,
		reasonCodes, result.ProviderReference, result.ResponseHash,
		result.CheckedAt.UTC(), result.ValidUntil.UTC()); err != nil {
		return fmt.Errorf("保存地址筛查结果: %w", err)
	}
	command, err := tx.Exec(ctx, `
		UPDATE payout_screening_jobs
		SET status = 'completed', current_result_id = $4,
		    lease_owner = NULL, lease_until = NULL, last_error = NULL,
		    updated_at = clock_timestamp()
		WHERE payout_id = $1
		  AND status = 'processing'
		  AND lease_owner = $2
		  AND lease_epoch = $3
	`, claim.PayoutID, claim.WorkerID, claim.LeaseEpoch, resultID)
	if err != nil {
		return fmt.Errorf("完成地址筛查任务: %w", err)
	}
	if command.RowsAffected() != 1 {
		return ErrScreeningLeaseLost
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("提交地址筛查结果事务: %w", err)
	}
	return nil
}

// Release 记录可重试故障并释放当前租约。
func (store *Store) Release(ctx context.Context, claim Claim, failureCode string) error {
	if err := validateClaim(claim); err != nil {
		return err
	}
	failureCode = strings.TrimSpace(failureCode)
	if !reasonCodePattern.MatchString(failureCode) {
		return ErrInvalidScreeningClaim
	}
	command, err := store.db.Exec(ctx, `
		UPDATE payout_screening_jobs
		SET status = 'pending', lease_owner = NULL, lease_until = NULL,
		    last_error = $4, updated_at = clock_timestamp()
		WHERE payout_id = $1
		  AND status = 'processing'
		  AND lease_owner = $2
		  AND lease_epoch = $3
	`, claim.PayoutID, claim.WorkerID, claim.LeaseEpoch, failureCode)
	if err != nil {
		return fmt.Errorf("释放地址筛查任务: %w", err)
	}
	if command.RowsAffected() != 1 {
		return ErrScreeningLeaseLost
	}
	return nil
}

func validateClaim(claim Claim) error {
	if !identity.ValidUUID(strings.TrimSpace(claim.PayoutID)) || strings.TrimSpace(claim.WorkerID) == "" ||
		len(claim.WorkerID) > 128 || claim.LeaseEpoch <= 0 {
		return ErrInvalidScreeningClaim
	}
	return nil
}

func validateResult(result Result) error {
	now := time.Now().UTC()
	if result.Provider == "" || len(result.Provider) > 128 ||
		(result.Decision != DecisionAllow && result.Decision != DecisionDeny && result.Decision != DecisionReview) ||
		result.ProviderReference == "" || len(result.ProviderReference) > 256 ||
		!hashPattern.MatchString(result.ResponseHash) || result.CheckedAt.IsZero() ||
		result.CheckedAt.After(now.Add(maximumFutureSkew)) || !result.ValidUntil.After(now) ||
		!result.ValidUntil.After(result.CheckedAt) || result.ValidUntil.After(result.CheckedAt.Add(30*24*time.Hour)) ||
		result.ReasonCodes == nil || len(result.ReasonCodes) > 32 {
		return ErrInvalidResult
	}
	seen := make(map[string]struct{}, len(result.ReasonCodes))
	for _, code := range result.ReasonCodes {
		if !reasonCodePattern.MatchString(code) {
			return ErrInvalidResult
		}
		if _, exists := seen[code]; exists {
			return ErrInvalidResult
		}
		seen[code] = struct{}{}
	}
	return nil
}

func normalizeResult(result Result) Result {
	result.Provider = strings.TrimSpace(result.Provider)
	result.Decision = strings.TrimSpace(result.Decision)
	result.ProviderReference = strings.TrimSpace(result.ProviderReference)
	result.ResponseHash = strings.TrimSpace(result.ResponseHash)
	result.CheckedAt = result.CheckedAt.UTC()
	result.ValidUntil = result.ValidUntil.UTC()
	return result
}
