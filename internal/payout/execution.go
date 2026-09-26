package payout

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/chain/tron"
	"github.com/eightyun/stablecoin-gateway/internal/identity"
	"github.com/eightyun/stablecoin-gateway/internal/ledger"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrNoBroadcastJob        = errors.New("没有待广播出款")
	ErrNoConfirmationJob     = errors.New("没有待确认出款")
	ErrInvalidExecutionClaim = errors.New("出款执行租约无效")
	ErrExecutionLeaseLost    = errors.New("出款执行租约已失效")
	executionCodePattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,127}$`)
)

// BroadcastClaim 是一笔只能广播原始签名内容的租约。
type BroadcastClaim struct {
	PayoutID    string
	WorkerID    string
	LeaseEpoch  int64
	Transaction tron.SignedTransaction
}

// ConfirmationClaim 是一笔等待固化终态的租约。
type ConfirmationClaim struct {
	PayoutID    string
	WorkerID    string
	LeaseEpoch  int64
	ExpiresAt   time.Time
	Transaction tron.SignedTransaction
}

// ClaimBroadcast 原子领取一个待广播任务。
func (store *Store) ClaimBroadcast(ctx context.Context, workerID string, leaseDuration time.Duration) (BroadcastClaim, error) {
	workerID = strings.TrimSpace(workerID)
	if workerID == "" || len(workerID) > 128 || leaseDuration.Milliseconds() <= 0 {
		return BroadcastClaim{}, ErrInvalidExecutionClaim
	}
	claim := BroadcastClaim{WorkerID: workerID}
	err := store.db.QueryRow(ctx, `
		WITH candidate AS (
			SELECT id FROM payouts
			WHERE status = 'ready_for_broadcast'
			  AND (execution_lease_owner IS NULL OR execution_lease_until <= clock_timestamp())
			ORDER BY updated_at, id
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE payouts AS payout
		SET execution_lease_owner = $1,
		    execution_lease_until = clock_timestamp() + ($2 * INTERVAL '1 millisecond'),
		    execution_lease_epoch = payout.execution_lease_epoch + 1,
		    broadcast_attempts = payout.broadcast_attempts + 1,
		    updated_at = clock_timestamp()
		FROM candidate
		WHERE payout.id = candidate.id
		RETURNING payout.id::TEXT, payout.execution_lease_epoch,
		          payout.transaction_id, payout.signed_transaction
	`, workerID, leaseDuration.Milliseconds()).Scan(
		&claim.PayoutID, &claim.LeaseEpoch, &claim.Transaction.ID, &claim.Transaction.Payload,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return BroadcastClaim{}, ErrNoBroadcastJob
	}
	if err != nil {
		return BroadcastClaim{}, fmt.Errorf("领取出款广播任务: %w", err)
	}
	return claim, nil
}

// BeginConfirmation 保存广播调用结果并进入原交易确认阶段。
func (store *Store) BeginConfirmation(ctx context.Context, claim BroadcastClaim, result, failureCode string) error {
	if err := validateExecutionClaim(claim.PayoutID, claim.WorkerID, claim.LeaseEpoch); err != nil {
		return err
	}
	if result != "accepted" && result != "unknown" {
		return ErrInvalidExecutionClaim
	}
	failureCode = strings.TrimSpace(failureCode)
	if result == "accepted" {
		failureCode = ""
	} else if !executionCodePattern.MatchString(failureCode) {
		return ErrInvalidExecutionClaim
	}
	command, err := store.db.Exec(ctx, `
		UPDATE payouts
		SET status = 'confirming',
		    broadcasted_at = clock_timestamp(),
		    broadcast_result = $4,
		    next_confirmation_at = clock_timestamp(),
		    last_execution_error = NULLIF($5, ''),
		    execution_lease_owner = NULL,
		    execution_lease_until = NULL,
		    updated_at = clock_timestamp()
		WHERE id = $1
		  AND status = 'ready_for_broadcast'
		  AND execution_lease_owner = $2
		  AND execution_lease_epoch = $3
		  AND execution_lease_until > clock_timestamp()
	`, claim.PayoutID, claim.WorkerID, claim.LeaseEpoch, result, failureCode)
	if err != nil {
		return fmt.Errorf("记录出款广播结果: %w", err)
	}
	if command.RowsAffected() != 1 {
		return ErrExecutionLeaseLost
	}
	return nil
}

// ClaimConfirmation 原子领取一个到期的固化确认任务。
func (store *Store) ClaimConfirmation(ctx context.Context, workerID string, leaseDuration time.Duration) (ConfirmationClaim, error) {
	workerID = strings.TrimSpace(workerID)
	if workerID == "" || len(workerID) > 128 || leaseDuration.Milliseconds() <= 0 {
		return ConfirmationClaim{}, ErrInvalidExecutionClaim
	}
	claim := ConfirmationClaim{WorkerID: workerID}
	err := store.db.QueryRow(ctx, `
		WITH candidate AS (
			SELECT id FROM payouts
			WHERE status = 'confirming'
			  AND next_confirmation_at <= clock_timestamp()
			  AND (execution_lease_owner IS NULL OR execution_lease_until <= clock_timestamp())
			ORDER BY next_confirmation_at, id
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE payouts AS payout
		SET execution_lease_owner = $1,
		    execution_lease_until = clock_timestamp() + ($2 * INTERVAL '1 millisecond'),
		    execution_lease_epoch = payout.execution_lease_epoch + 1,
		    confirmation_attempts = payout.confirmation_attempts + 1,
		    updated_at = clock_timestamp()
		FROM candidate
		WHERE payout.id = candidate.id
		RETURNING payout.id::TEXT, payout.execution_lease_epoch, payout.transaction_expires_at,
		          payout.transaction_id, payout.signed_transaction
	`, workerID, leaseDuration.Milliseconds()).Scan(
		&claim.PayoutID, &claim.LeaseEpoch, &claim.ExpiresAt,
		&claim.Transaction.ID, &claim.Transaction.Payload,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ConfirmationClaim{}, ErrNoConfirmationJob
	}
	if err != nil {
		return ConfirmationClaim{}, fmt.Errorf("领取出款确认任务: %w", err)
	}
	return claim, nil
}

// ReleaseConfirmation 延迟下一次查询，同时保存非敏感失败类别。
func (store *Store) ReleaseConfirmation(ctx context.Context, claim ConfirmationClaim, delay time.Duration, failureCode string) error {
	if err := validateExecutionClaim(claim.PayoutID, claim.WorkerID, claim.LeaseEpoch); err != nil || delay <= 0 {
		return ErrInvalidExecutionClaim
	}
	failureCode = strings.TrimSpace(failureCode)
	if failureCode != "" && !executionCodePattern.MatchString(failureCode) {
		return ErrInvalidExecutionClaim
	}
	command, err := store.db.Exec(ctx, `
		UPDATE payouts
		SET next_confirmation_at = clock_timestamp() + ($4 * INTERVAL '1 millisecond'),
		    last_execution_error = NULLIF($5, ''),
		    execution_lease_owner = NULL,
		    execution_lease_until = NULL,
		    updated_at = clock_timestamp()
		WHERE id = $1
		  AND status = 'confirming'
		  AND execution_lease_owner = $2
		  AND execution_lease_epoch = $3
	`, claim.PayoutID, claim.WorkerID, claim.LeaseEpoch, delay.Milliseconds(), failureCode)
	if err != nil {
		return fmt.Errorf("释放出款确认任务: %w", err)
	}
	if command.RowsAffected() != 1 {
		return ErrExecutionLeaseLost
	}
	return nil
}

// CompleteConfirmationSuccess 原子结算成功出款的冻结负债与托管资产。
func (store *Store) CompleteConfirmationSuccess(ctx context.Context, claim ConfirmationClaim) error {
	return store.finalizeConfirmation(ctx, claim, true, "")
}

// CompleteConfirmationFailure 原子解冻链上失败或安全过期的出款。
func (store *Store) CompleteConfirmationFailure(ctx context.Context, claim ConfirmationClaim, reason string) error {
	reason = strings.TrimSpace(reason)
	if !executionCodePattern.MatchString(reason) {
		return ErrInvalidExecutionClaim
	}
	return store.finalizeConfirmation(ctx, claim, false, reason)
}

func (store *Store) finalizeConfirmation(ctx context.Context, claim ConfirmationClaim, succeeded bool, reason string) (err error) {
	if err := validateExecutionClaim(claim.PayoutID, claim.WorkerID, claim.LeaseEpoch); err != nil {
		return err
	}
	databaseTransaction, err := store.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return fmt.Errorf("开始出款终态事务: %w", err)
	}
	defer func() {
		if rollbackErr := databaseTransaction.Rollback(context.Background()); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			err = errors.Join(err, rollbackErr)
		}
	}()
	var merchantID, assetID, amountText string
	err = databaseTransaction.QueryRow(ctx, `
		SELECT merchant_id::TEXT, asset_id, amount::TEXT
		FROM payouts
		WHERE id = $1 AND status = 'confirming'
		  AND execution_lease_owner = $2 AND execution_lease_epoch = $3
		  AND execution_lease_until > clock_timestamp()
		FOR UPDATE
	`, claim.PayoutID, claim.WorkerID, claim.LeaseEpoch).Scan(&merchantID, &assetID, &amountText)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrExecutionLeaseLost
	}
	if err != nil {
		return fmt.Errorf("锁定出款终态: %w", err)
	}
	amount, err := strconv.ParseInt(amountText, 10, 64)
	if err != nil || amount <= 0 {
		return errors.New("出款终态金额无效")
	}
	debitAccountID, creditAccountID, referenceType := "", "", ""
	if succeeded {
		referenceType = "payout_settlement"
		err = databaseTransaction.QueryRow(ctx, `
			SELECT frozen.id::TEXT, custody.id::TEXT
			FROM ledger_accounts AS frozen
			JOIN ledger_accounts AS custody ON custody.asset_id = frozen.asset_id
			WHERE frozen.owner_type = 'merchant' AND frozen.owner_id = $1
			  AND frozen.asset_id = $2 AND frozen.code = 'frozen' AND frozen.normal_side = 'C'
			  AND custody.owner_type = 'platform' AND custody.owner_id = 'gateway'
			  AND custody.code = 'custody' AND custody.normal_side = 'D'
			  AND frozen.status IN ('active', 'locked') AND custody.status IN ('active', 'locked')
			FOR UPDATE OF frozen, custody
		`, merchantID, assetID).Scan(&debitAccountID, &creditAccountID)
	} else {
		referenceType = "payout_failure_release"
		err = databaseTransaction.QueryRow(ctx, `
			SELECT frozen.id::TEXT, available.id::TEXT
			FROM ledger_accounts AS frozen
			JOIN ledger_accounts AS available
			  ON available.owner_type = frozen.owner_type AND available.owner_id = frozen.owner_id
			 AND available.asset_id = frozen.asset_id
			WHERE frozen.owner_type = 'merchant' AND frozen.owner_id = $1
			  AND frozen.asset_id = $2 AND frozen.code = 'frozen' AND frozen.normal_side = 'C'
			  AND available.code = 'available' AND available.normal_side = 'C'
			  AND frozen.status IN ('active', 'locked') AND available.status IN ('active', 'locked')
			FOR UPDATE OF frozen, available
		`, merchantID, assetID).Scan(&debitAccountID, &creditAccountID)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrLedgerUnavailable
	}
	if err != nil {
		return fmt.Errorf("锁定出款终态账本科目: %w", err)
	}
	frozenBalance, err := accountBalance(ctx, databaseTransaction, debitAccountID)
	if err != nil || frozenBalance.Cmp(big.NewInt(amount)) < 0 {
		return errors.New("出款冻结余额不足")
	}
	journalID, err := identity.NewUUID()
	if err != nil {
		return err
	}
	postResult, err := ledger.PostInTransaction(ctx, databaseTransaction, ledger.Transaction{
		ID: journalID, RequesterType: "system", RequesterID: "payout-confirmation",
		IdempotencyKey: referenceType + ":" + claim.PayoutID,
		ReferenceType:  referenceType, ReferenceID: claim.PayoutID,
		Entries: []ledger.Entry{
			{AccountID: debitAccountID, AssetID: assetID, Side: ledger.Debit, Amount: amount},
			{AccountID: creditAccountID, AssetID: assetID, Side: ledger.Credit, Amount: amount},
		},
	})
	if err != nil {
		return fmt.Errorf("过账出款终态: %w", err)
	}
	status := StatusSucceeded
	var command pgconn.CommandTag
	if succeeded {
		command, err = databaseTransaction.Exec(ctx, `
			UPDATE payouts SET status = 'succeeded', settlement_transaction_id = $4,
			    confirmed_at = clock_timestamp(), execution_lease_owner = NULL,
			    execution_lease_until = NULL, last_execution_error = NULL,
			    updated_at = clock_timestamp()
			WHERE id = $1 AND execution_lease_owner = $2 AND execution_lease_epoch = $3
		`, claim.PayoutID, claim.WorkerID, claim.LeaseEpoch, postResult.TransactionID)
	} else {
		status = StatusFailed
		command, err = databaseTransaction.Exec(ctx, `
			UPDATE payouts SET status = 'failed', unfreeze_transaction_id = $4,
			    failed_at = clock_timestamp(), failure_reason = $5,
			    execution_lease_owner = NULL, execution_lease_until = NULL,
			    last_execution_error = NULL, updated_at = clock_timestamp()
			WHERE id = $1 AND execution_lease_owner = $2 AND execution_lease_epoch = $3
		`, claim.PayoutID, claim.WorkerID, claim.LeaseEpoch, postResult.TransactionID, reason)
	}
	if err != nil {
		return fmt.Errorf("更新出款终态 %s: %w", status, err)
	}
	if command.RowsAffected() != 1 {
		return ErrExecutionLeaseLost
	}
	if err = databaseTransaction.Commit(ctx); err != nil {
		return fmt.Errorf("提交出款终态事务: %w", err)
	}
	return nil
}

func validateExecutionClaim(payoutID, workerID string, leaseEpoch int64) error {
	if !identity.ValidUUID(strings.TrimSpace(payoutID)) || strings.TrimSpace(workerID) == "" ||
		len(workerID) > 128 || leaseEpoch <= 0 {
		return ErrInvalidExecutionClaim
	}
	return nil
}
