package payout

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/identity"
	"github.com/eightyun/stablecoin-gateway/internal/ledger"
	"github.com/jackc/pgx/v5"
)

const (
	StatusPendingReview     = "pending_review"
	StatusApproved          = "approved"
	StatusReadyForBroadcast = "ready_for_broadcast"
	StatusConfirming        = "confirming"
	StatusSucceeded         = "succeeded"
	StatusFailed            = "failed"
	StatusRejected          = "rejected"
)

// ReviewDecision 表示运营人员对出款的审批决定。
type ReviewDecision string

const (
	DecisionApprove ReviewDecision = "approve"
	DecisionReject  ReviewDecision = "reject"
)

var (
	ErrInvalidReview       = errors.New("出款审批请求无效")
	ErrPayoutStateConflict = errors.New("出款状态不允许该审批决定")
)

// ReviewRequest 是一次带审计信息的出款审批请求。
type ReviewRequest struct {
	PayoutID string
	Decision ReviewDecision
	Reviewer string
	Reason   string
}

// ReviewResult 是审批后的状态及审计结果。
type ReviewResult struct {
	PayoutID              string    `json:"payout_id"`
	Status                string    `json:"status"`
	Reviewer              string    `json:"reviewer"`
	Reason                string    `json:"reason"`
	ReviewedAt            time.Time `json:"reviewed_at"`
	UnfreezeTransactionID string    `json:"unfreeze_transaction_id,omitempty"`
	Changed               bool      `json:"changed"`
}

// Review 审批出款。拒绝操作会在同一事务内解冻资金。
func (store *Store) Review(ctx context.Context, request ReviewRequest) (result ReviewResult, err error) {
	request = normalizeReviewRequest(request)
	targetStatus, err := validateReviewRequest(request)
	if err != nil {
		return ReviewResult{}, err
	}
	databaseTransaction, err := store.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return ReviewResult{}, fmt.Errorf("开始出款审批事务: %w", err)
	}
	defer func() {
		if rollbackErr := databaseTransaction.Rollback(context.Background()); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			err = errors.Join(err, fmt.Errorf("回滚出款审批事务: %w", rollbackErr))
		}
	}()

	var merchantID, assetID, amountText, currentStatus string
	var reviewedBy, reviewReason, unfreezeTransactionID sql.NullString
	var reviewedAt sql.NullTime
	err = databaseTransaction.QueryRow(ctx, `
		SELECT merchant_id::TEXT, asset_id, amount::TEXT, status,
		       reviewed_by, review_reason, reviewed_at, unfreeze_transaction_id::TEXT
		FROM payouts
		WHERE id = $1
		FOR UPDATE
	`, request.PayoutID).Scan(
		&merchantID, &assetID, &amountText, &currentStatus,
		&reviewedBy, &reviewReason, &reviewedAt, &unfreezeTransactionID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ReviewResult{}, ErrPayoutNotFound
	}
	if err != nil {
		return ReviewResult{}, fmt.Errorf("锁定待审批出款: %w", err)
	}
	if currentStatus != StatusPendingReview {
		if currentStatus != targetStatus || !reviewedBy.Valid || !reviewReason.Valid || !reviewedAt.Valid ||
			reviewedBy.String != request.Reviewer || reviewReason.String != request.Reason {
			return ReviewResult{}, ErrPayoutStateConflict
		}
		if err = databaseTransaction.Commit(ctx); err != nil {
			return ReviewResult{}, fmt.Errorf("提交出款审批幂等查询: %w", err)
		}
		return ReviewResult{
			PayoutID: request.PayoutID, Status: currentStatus, Reviewer: reviewedBy.String,
			Reason: reviewReason.String, ReviewedAt: reviewedAt.Time,
			UnfreezeTransactionID: unfreezeTransactionID.String, Changed: false,
		}, nil
	}

	result = ReviewResult{
		PayoutID: request.PayoutID, Status: targetStatus,
		Reviewer: request.Reviewer, Reason: request.Reason, Changed: true,
	}
	if request.Decision == DecisionReject {
		transactionID, reviewErr := store.unfreezeRejectedPayout(
			ctx, databaseTransaction, request, merchantID, assetID, amountText,
		)
		if reviewErr != nil {
			return ReviewResult{}, reviewErr
		}
		result.UnfreezeTransactionID = transactionID
	}
	err = databaseTransaction.QueryRow(ctx, `
		UPDATE payouts
		SET status = $2,
		    reviewed_by = $3,
		    review_reason = $4,
		    reviewed_at = CURRENT_TIMESTAMP,
		    unfreeze_transaction_id = NULLIF($5, '')::UUID,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND status = 'pending_review'
		RETURNING reviewed_at
	`, request.PayoutID, targetStatus, request.Reviewer, request.Reason, result.UnfreezeTransactionID).Scan(&result.ReviewedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ReviewResult{}, ErrPayoutStateConflict
	}
	if err != nil {
		return ReviewResult{}, fmt.Errorf("更新出款审批状态: %w", err)
	}
	if err = databaseTransaction.Commit(ctx); err != nil {
		return ReviewResult{}, fmt.Errorf("提交出款审批事务: %w", err)
	}
	return result, nil
}

func (store *Store) unfreezeRejectedPayout(
	ctx context.Context,
	databaseTransaction pgx.Tx,
	request ReviewRequest,
	merchantID, assetID, amountText string,
) (string, error) {
	amount, err := strconv.ParseInt(amountText, 10, 64)
	if err != nil || amount <= 0 {
		return "", errors.New("出款冻结金额无效")
	}
	var availableAccountID, frozenAccountID string
	err = databaseTransaction.QueryRow(ctx, `
		SELECT available.id::TEXT, frozen.id::TEXT
		FROM ledger_accounts AS available
		JOIN ledger_accounts AS frozen
		  ON frozen.owner_type = available.owner_type
		 AND frozen.owner_id = available.owner_id
		 AND frozen.asset_id = available.asset_id
		WHERE available.owner_type = 'merchant'
		  AND available.owner_id = $1
		  AND available.asset_id = $2
		  AND available.code = $3
		  AND available.normal_side = 'C'
		  AND available.status IN ('active', 'locked')
		  AND frozen.code = $4
		  AND frozen.normal_side = 'C'
		  AND frozen.status IN ('active', 'locked')
		FOR UPDATE OF available, frozen
	`, merchantID, assetID, availableAccountCode, frozenAccountCode).Scan(&availableAccountID, &frozenAccountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrLedgerUnavailable
	}
	if err != nil {
		return "", fmt.Errorf("锁定出款解冻账本科目: %w", err)
	}
	frozenBalance, err := accountBalance(ctx, databaseTransaction, frozenAccountID)
	if err != nil {
		return "", err
	}
	if frozenBalance.Cmp(big.NewInt(amount)) < 0 {
		return "", errors.New("出款冻结余额不足")
	}
	journalID, err := identity.NewUUID()
	if err != nil {
		return "", err
	}
	postResult, err := ledger.PostInTransaction(ctx, databaseTransaction, ledger.Transaction{
		ID: journalID, RequesterType: "operator", RequesterID: request.Reviewer,
		IdempotencyKey: "payout-unfreeze:" + request.PayoutID,
		ReferenceType:  "payout_unfreeze", ReferenceID: request.PayoutID,
		Entries: []ledger.Entry{
			{AccountID: frozenAccountID, AssetID: assetID, Side: ledger.Debit, Amount: amount},
			{AccountID: availableAccountID, AssetID: assetID, Side: ledger.Credit, Amount: amount},
		},
	})
	if err != nil {
		return "", fmt.Errorf("解冻拒绝出款余额: %w", err)
	}
	return postResult.TransactionID, nil
}

func normalizeReviewRequest(request ReviewRequest) ReviewRequest {
	request.PayoutID = strings.TrimSpace(request.PayoutID)
	request.Decision = ReviewDecision(strings.TrimSpace(string(request.Decision)))
	request.Reviewer = strings.TrimSpace(request.Reviewer)
	request.Reason = strings.TrimSpace(request.Reason)
	return request
}

func validateReviewRequest(request ReviewRequest) (string, error) {
	if !identity.ValidUUID(request.PayoutID) || request.Reviewer == "" || len(request.Reviewer) > 128 ||
		request.Reason == "" || len(request.Reason) > 512 {
		return "", ErrInvalidReview
	}
	switch request.Decision {
	case DecisionApprove:
		return StatusApproved, nil
	case DecisionReject:
		return StatusRejected, nil
	default:
		return "", ErrInvalidReview
	}
}
