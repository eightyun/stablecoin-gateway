package sweep

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/chain/tron"
	"github.com/eightyun/stablecoin-gateway/internal/identity"
	"github.com/jackc/pgx/v5"
)

var (
	ErrNoBroadcastJob        = errors.New("没有待广播归集")
	ErrNoConfirmationJob     = errors.New("没有待确认归集")
	ErrInvalidExecutionClaim = errors.New("归集执行租约无效")
	ErrExecutionLeaseLost    = errors.New("归集执行租约已失效")
	ErrExecutionNotFound     = errors.New("归集执行不存在")
)

// BroadcastClaim 是一笔只能广播原始签名内容的归集租约。
type BroadcastClaim struct {
	PlanID      string
	WorkerID    string
	LeaseEpoch  int64
	Network     string
	Transaction tron.SignedTransaction
}

// ConfirmationClaim 是一笔等待固化终态的归集租约。
type ConfirmationClaim struct {
	PlanID      string
	WorkerID    string
	LeaseEpoch  int64
	Network     string
	ExpiresAt   time.Time
	Transaction tron.SignedTransaction
}

// ExecutionState 是可供运营和验收轮询的非敏感归集执行视图。
type ExecutionState struct {
	PlanID        string    `json:"plan_id"`
	Status        string    `json:"status"`
	Network       string    `json:"network"`
	TransactionID string    `json:"transaction_id,omitempty"`
	FailureReason string    `json:"failure_reason,omitempty"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// GetExecution 返回归集执行状态，不暴露签名交易原文。
func (store *Store) GetExecution(ctx context.Context, planID string) (ExecutionState, error) {
	planID = strings.TrimSpace(planID)
	if !identity.ValidUUID(planID) {
		return ExecutionState{}, ErrExecutionNotFound
	}
	var state ExecutionState
	err := store.db.QueryRow(ctx, `
		SELECT execution.plan_id::TEXT, execution.status, asset.network,
		       COALESCE(execution.transaction_id, ''),
		       COALESCE(execution.failure_reason, ''), execution.updated_at
		FROM sweep_executions AS execution
		JOIN sweep_plans AS plan ON plan.id = execution.plan_id
		JOIN assets AS asset ON asset.id = plan.asset_id
		WHERE execution.plan_id = $1
	`, planID).Scan(
		&state.PlanID, &state.Status, &state.Network, &state.TransactionID,
		&state.FailureReason, &state.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ExecutionState{}, ErrExecutionNotFound
	}
	if err != nil {
		return ExecutionState{}, fmt.Errorf("查询归集执行状态: %w", err)
	}
	return state, nil
}

// ClaimBroadcast 原子领取指定网络的一笔待广播归集。
func (store *Store) ClaimBroadcast(
	ctx context.Context,
	workerID string,
	network string,
	leaseDuration time.Duration,
) (BroadcastClaim, error) {
	workerID = strings.TrimSpace(workerID)
	network = strings.TrimSpace(network)
	if !validExecutionLeaseInput(workerID, network, leaseDuration) {
		return BroadcastClaim{}, ErrInvalidExecutionClaim
	}
	claim := BroadcastClaim{WorkerID: workerID}
	err := store.db.QueryRow(ctx, `
		WITH candidate AS (
			SELECT execution.plan_id
			FROM sweep_executions AS execution
			JOIN sweep_plans AS plan ON plan.id = execution.plan_id
			JOIN assets AS asset ON asset.id = plan.asset_id AND asset.status = 'active'
			WHERE execution.status = 'ready_for_broadcast'
			  AND asset.network = $3
			  AND (execution.lease_owner IS NULL OR execution.lease_until <= clock_timestamp())
			ORDER BY execution.updated_at, execution.plan_id
			FOR UPDATE OF execution SKIP LOCKED
			LIMIT 1
		)
		UPDATE sweep_executions AS execution
		SET lease_owner = $1,
		    lease_until = clock_timestamp() + ($2 * INTERVAL '1 millisecond'),
		    lease_epoch = execution.lease_epoch + 1,
		    broadcast_attempts = execution.broadcast_attempts + 1,
		    updated_at = clock_timestamp()
		FROM candidate
		JOIN sweep_plans AS plan ON plan.id = candidate.plan_id
		JOIN assets AS asset ON asset.id = plan.asset_id AND asset.status = 'active'
		WHERE execution.plan_id = candidate.plan_id
		RETURNING execution.plan_id::TEXT, execution.lease_epoch, asset.network,
		          execution.transaction_id, execution.signed_transaction
	`, workerID, leaseDuration.Milliseconds(), network).Scan(
		&claim.PlanID, &claim.LeaseEpoch, &claim.Network,
		&claim.Transaction.ID, &claim.Transaction.Payload,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return BroadcastClaim{}, ErrNoBroadcastJob
	}
	if err != nil {
		return BroadcastClaim{}, fmt.Errorf("领取归集广播任务: %w", err)
	}
	return claim, nil
}

// BeginConfirmation 保存广播调用结果并进入原交易确认阶段。
func (store *Store) BeginConfirmation(ctx context.Context, claim BroadcastClaim, result, failureCode string) error {
	if err := validateExecutionClaim(claim.PlanID, claim.WorkerID, claim.LeaseEpoch); err != nil {
		return err
	}
	if result != "accepted" && result != "unknown" {
		return ErrInvalidExecutionClaim
	}
	failureCode = strings.TrimSpace(failureCode)
	if result == "accepted" {
		failureCode = ""
	} else if !failureCodePattern.MatchString(failureCode) {
		return ErrInvalidExecutionClaim
	}
	command, err := store.db.Exec(ctx, `
		UPDATE sweep_executions
		SET status = 'confirming',
		    broadcasted_at = clock_timestamp(),
		    broadcast_result = $4,
		    next_confirmation_at = clock_timestamp(),
		    last_error = NULLIF($5, ''),
		    lease_owner = NULL,
		    lease_until = NULL,
		    updated_at = clock_timestamp()
		WHERE plan_id = $1
		  AND status = 'ready_for_broadcast'
		  AND lease_owner = $2
		  AND lease_epoch = $3
		  AND lease_until > clock_timestamp()
	`, claim.PlanID, claim.WorkerID, claim.LeaseEpoch, result, failureCode)
	if err != nil {
		return fmt.Errorf("记录归集广播结果: %w", err)
	}
	if command.RowsAffected() != 1 {
		return ErrExecutionLeaseLost
	}
	return nil
}

// ClaimConfirmation 原子领取指定网络的一笔到期固化确认任务。
func (store *Store) ClaimConfirmation(
	ctx context.Context,
	workerID string,
	network string,
	leaseDuration time.Duration,
) (ConfirmationClaim, error) {
	workerID = strings.TrimSpace(workerID)
	network = strings.TrimSpace(network)
	if !validExecutionLeaseInput(workerID, network, leaseDuration) {
		return ConfirmationClaim{}, ErrInvalidExecutionClaim
	}
	claim := ConfirmationClaim{WorkerID: workerID}
	err := store.db.QueryRow(ctx, `
		WITH candidate AS (
			SELECT execution.plan_id
			FROM sweep_executions AS execution
			JOIN sweep_plans AS plan ON plan.id = execution.plan_id
			JOIN assets AS asset ON asset.id = plan.asset_id AND asset.status = 'active'
			WHERE execution.status = 'confirming'
			  AND asset.network = $3
			  AND execution.next_confirmation_at <= clock_timestamp()
			  AND (execution.lease_owner IS NULL OR execution.lease_until <= clock_timestamp())
			ORDER BY execution.next_confirmation_at, execution.plan_id
			FOR UPDATE OF execution SKIP LOCKED
			LIMIT 1
		)
		UPDATE sweep_executions AS execution
		SET lease_owner = $1,
		    lease_until = clock_timestamp() + ($2 * INTERVAL '1 millisecond'),
		    lease_epoch = execution.lease_epoch + 1,
		    confirmation_attempts = execution.confirmation_attempts + 1,
		    updated_at = clock_timestamp()
		FROM candidate
		JOIN sweep_plans AS plan ON plan.id = candidate.plan_id
		JOIN assets AS asset ON asset.id = plan.asset_id AND asset.status = 'active'
		WHERE execution.plan_id = candidate.plan_id
		RETURNING execution.plan_id::TEXT, execution.lease_epoch, asset.network,
		          execution.transaction_expires_at, execution.transaction_id,
		          execution.signed_transaction
	`, workerID, leaseDuration.Milliseconds(), network).Scan(
		&claim.PlanID, &claim.LeaseEpoch, &claim.Network, &claim.ExpiresAt,
		&claim.Transaction.ID, &claim.Transaction.Payload,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ConfirmationClaim{}, ErrNoConfirmationJob
	}
	if err != nil {
		return ConfirmationClaim{}, fmt.Errorf("领取归集确认任务: %w", err)
	}
	return claim, nil
}

// ReleaseConfirmation 延迟下一次查询，同时保存非敏感失败类别。
func (store *Store) ReleaseConfirmation(
	ctx context.Context,
	claim ConfirmationClaim,
	delay time.Duration,
	failureCode string,
) error {
	if err := validateExecutionClaim(claim.PlanID, claim.WorkerID, claim.LeaseEpoch); err != nil || delay <= 0 {
		return ErrInvalidExecutionClaim
	}
	failureCode = strings.TrimSpace(failureCode)
	if failureCode != "" && !failureCodePattern.MatchString(failureCode) {
		return ErrInvalidExecutionClaim
	}
	command, err := store.db.Exec(ctx, `
		UPDATE sweep_executions
		SET next_confirmation_at = clock_timestamp() + ($4 * INTERVAL '1 millisecond'),
		    last_error = NULLIF($5, ''),
		    lease_owner = NULL,
		    lease_until = NULL,
		    updated_at = clock_timestamp()
		WHERE plan_id = $1
		  AND status = 'confirming'
		  AND lease_owner = $2
		  AND lease_epoch = $3
	`, claim.PlanID, claim.WorkerID, claim.LeaseEpoch, delay.Milliseconds(), failureCode)
	if err != nil {
		return fmt.Errorf("释放归集确认任务: %w", err)
	}
	if command.RowsAffected() != 1 {
		return ErrExecutionLeaseLost
	}
	return nil
}

// CompleteConfirmationSuccess 将已固化成功的归集置为终态。
func (store *Store) CompleteConfirmationSuccess(ctx context.Context, claim ConfirmationClaim) error {
	return store.completeConfirmation(ctx, claim, true, "")
}

// CompleteConfirmationFailure 将链上失败或安全过期的归集置为终态。
func (store *Store) CompleteConfirmationFailure(ctx context.Context, claim ConfirmationClaim, reason string) error {
	reason = strings.TrimSpace(reason)
	if !failureCodePattern.MatchString(reason) {
		return ErrInvalidExecutionClaim
	}
	return store.completeConfirmation(ctx, claim, false, reason)
}

func (store *Store) completeConfirmation(
	ctx context.Context,
	claim ConfirmationClaim,
	succeeded bool,
	reason string,
) error {
	if err := validateExecutionClaim(claim.PlanID, claim.WorkerID, claim.LeaseEpoch); err != nil {
		return err
	}
	status := "failed"
	if succeeded {
		status = "succeeded"
	}
	command, err := store.db.Exec(ctx, `
		UPDATE sweep_executions
		SET status = $4,
		    next_confirmation_at = NULL,
		    confirmed_at = CASE WHEN $4 = 'succeeded' THEN clock_timestamp() ELSE NULL END,
		    failed_at = CASE WHEN $4 = 'failed' THEN clock_timestamp() ELSE NULL END,
		    failure_reason = NULLIF($5, ''),
		    last_error = NULL,
		    lease_owner = NULL,
		    lease_until = NULL,
		    updated_at = clock_timestamp()
		WHERE plan_id = $1
		  AND status = 'confirming'
		  AND lease_owner = $2
		  AND lease_epoch = $3
		  AND lease_until > clock_timestamp()
	`, claim.PlanID, claim.WorkerID, claim.LeaseEpoch, status, reason)
	if err != nil {
		return fmt.Errorf("保存归集确认终态: %w", err)
	}
	if command.RowsAffected() != 1 {
		return ErrExecutionLeaseLost
	}
	return nil
}

func validExecutionLeaseInput(workerID, network string, leaseDuration time.Duration) bool {
	return workerID != "" && len(workerID) <= 128 && network != "" && len(network) <= 128 &&
		leaseDuration.Milliseconds() > 0
}

func validateExecutionClaim(planID, workerID string, leaseEpoch int64) error {
	if !identity.ValidUUID(strings.TrimSpace(planID)) || strings.TrimSpace(workerID) == "" ||
		len(workerID) > 128 || leaseEpoch <= 0 {
		return ErrInvalidExecutionClaim
	}
	return nil
}
