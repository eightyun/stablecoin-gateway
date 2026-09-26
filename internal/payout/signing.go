package payout

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/chain/tron"
	"github.com/eightyun/stablecoin-gateway/internal/identity"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const maxSignedTransactionBytes = 1 << 20

var (
	ErrNoSigningJob              = errors.New("没有待签名出款")
	ErrInvalidSigningClaim       = errors.New("出款签名租约无效")
	ErrSigningLeaseLost          = errors.New("出款签名租约已失效")
	ErrInvalidSignedTransaction  = errors.New("已签名交易无效")
	ErrSignedTransactionConflict = errors.New("链上交易 ID 已被其他出款使用")
	transactionIDPattern         = regexp.MustCompile(`^[0-9a-f]{64}$`)
	failureCodePattern           = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,127}$`)
)

// SigningClaim 是一次带栅栏令牌的出款签名租约。
type SigningClaim struct {
	PayoutID           string
	WorkerID           string
	LeaseEpoch         int64
	Network            string
	ContractAddress    string
	DestinationAddress string
	Amount             string
}

// ClaimSigning 原子领取一个已审批的签名任务。
func (store *Store) ClaimSigning(
	ctx context.Context,
	workerID string,
	leaseDuration time.Duration,
) (SigningClaim, error) {
	workerID = strings.TrimSpace(workerID)
	if workerID == "" || len(workerID) > 128 || leaseDuration.Milliseconds() <= 0 {
		return SigningClaim{}, ErrInvalidSigningClaim
	}
	claim := SigningClaim{WorkerID: workerID}
	err := store.db.QueryRow(ctx, `
		WITH candidate AS (
			SELECT payout.id, asset.network, asset.contract_address
			FROM payouts AS payout
			JOIN assets AS asset ON asset.id = payout.asset_id
			WHERE payout.status = 'approved'
			  AND (payout.execution_lease_owner IS NULL OR payout.execution_lease_until <= clock_timestamp())
			ORDER BY payout.updated_at, payout.id
			FOR UPDATE OF payout SKIP LOCKED
			LIMIT 1
		)
		UPDATE payouts AS payout
		SET execution_lease_owner = $1,
		    execution_lease_until = clock_timestamp() + ($2 * INTERVAL '1 millisecond'),
		    execution_lease_epoch = payout.execution_lease_epoch + 1,
		    signing_attempts = payout.signing_attempts + 1,
		    last_execution_error = NULL,
		    updated_at = clock_timestamp()
		FROM candidate
		WHERE payout.id = candidate.id
		RETURNING payout.id::TEXT, payout.execution_lease_epoch,
		          candidate.network, candidate.contract_address,
		          payout.destination_address, payout.amount::TEXT
	`, workerID, leaseDuration.Milliseconds()).Scan(
		&claim.PayoutID, &claim.LeaseEpoch, &claim.Network, &claim.ContractAddress,
		&claim.DestinationAddress, &claim.Amount,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return SigningClaim{}, ErrNoSigningJob
	}
	if err != nil {
		return SigningClaim{}, fmt.Errorf("领取出款签名任务: %w", err)
	}
	return claim, nil
}

// CompleteSigning 用当前租约保存唯一签名结果，并把出款交给广播阶段。
func (store *Store) CompleteSigning(
	ctx context.Context,
	claim SigningClaim,
	transaction tron.SignedTransaction,
) error {
	if err := validateSigningClaim(claim); err != nil {
		return err
	}
	if !transactionIDPattern.MatchString(transaction.ID) || len(transaction.Payload) == 0 ||
		len(transaction.Payload) > maxSignedTransactionBytes || tron.ValidateSignedTransaction(transaction) != nil {
		return ErrInvalidSignedTransaction
	}
	result, err := store.db.Exec(ctx, `
		UPDATE payouts
		SET status = 'ready_for_broadcast',
		    transaction_id = $4,
		    signed_transaction = $5,
		    execution_lease_owner = NULL,
		    execution_lease_until = NULL,
		    last_execution_error = NULL,
		    updated_at = clock_timestamp()
		WHERE id = $1
		  AND status = 'approved'
		  AND execution_lease_owner = $2
		  AND execution_lease_epoch = $3
		  AND execution_lease_until > clock_timestamp()
	`, claim.PayoutID, claim.WorkerID, claim.LeaseEpoch, transaction.ID, transaction.Payload)
	if err != nil {
		var databaseError *pgconn.PgError
		if errors.As(err, &databaseError) && databaseError.ConstraintName == "payouts_transaction_id_unique_idx" {
			return ErrSignedTransactionConflict
		}
		return fmt.Errorf("保存出款签名结果: %w", err)
	}
	if result.RowsAffected() != 1 {
		return ErrSigningLeaseLost
	}
	return nil
}

// ReleaseSigning 记录可重试的失败类别并释放当前租约。
func (store *Store) ReleaseSigning(ctx context.Context, claim SigningClaim, failureCode string) error {
	if err := validateSigningClaim(claim); err != nil {
		return err
	}
	failureCode = strings.TrimSpace(failureCode)
	if !failureCodePattern.MatchString(failureCode) {
		return ErrInvalidSigningClaim
	}
	result, err := store.db.Exec(ctx, `
		UPDATE payouts
		SET execution_lease_owner = NULL,
		    execution_lease_until = NULL,
		    last_execution_error = $4,
		    updated_at = clock_timestamp()
		WHERE id = $1
		  AND status = 'approved'
		  AND execution_lease_owner = $2
		  AND execution_lease_epoch = $3
	`, claim.PayoutID, claim.WorkerID, claim.LeaseEpoch, failureCode)
	if err != nil {
		return fmt.Errorf("释放出款签名任务: %w", err)
	}
	if result.RowsAffected() != 1 {
		return ErrSigningLeaseLost
	}
	return nil
}

func validateSigningClaim(claim SigningClaim) error {
	if !identity.ValidUUID(strings.TrimSpace(claim.PayoutID)) || strings.TrimSpace(claim.WorkerID) == "" ||
		len(claim.WorkerID) > 128 || claim.LeaseEpoch <= 0 {
		return ErrInvalidSigningClaim
	}
	return nil
}
