package sweep

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
	ErrNoSigningJob              = errors.New("没有待签名归集计划")
	ErrInvalidSigningClaim       = errors.New("归集签名租约无效")
	ErrSigningLeaseLost          = errors.New("归集签名租约已失效")
	ErrInvalidSignedTransaction  = errors.New("归集签名交易无效")
	ErrSignedTransactionConflict = errors.New("链上交易 ID 已被其他归集使用")
	transactionIDPattern         = regexp.MustCompile(`^[0-9a-f]{64}$`)
	failureCodePattern           = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,127}$`)
)

// SigningClaim 是带 fencing epoch 的归集签名租约。
type SigningClaim struct {
	PlanID             string
	WorkerID           string
	LeaseEpoch         int64
	Network            string
	SourceAddress      string
	ContractAddress    string
	DestinationAddress string
	Amount             string
}

// BalanceObservation 保存签名前读取到的一致固化余额。
type BalanceObservation struct {
	Balance     string
	BlockHeight int64
	BlockHash   string
}

// ClaimSigning 原子领取一个未签名归集计划。
func (store *Store) ClaimSigning(
	ctx context.Context,
	workerID string,
	network string,
	leaseDuration time.Duration,
) (SigningClaim, error) {
	workerID = strings.TrimSpace(workerID)
	network = strings.TrimSpace(network)
	if workerID == "" || len(workerID) > 128 || network == "" || len(network) > 128 || leaseDuration.Milliseconds() <= 0 {
		return SigningClaim{}, ErrInvalidSigningClaim
	}
	claim := SigningClaim{WorkerID: workerID}
	err := store.db.QueryRow(ctx, `
		WITH candidate AS (
			SELECT execution.plan_id
			FROM sweep_executions AS execution
			JOIN sweep_plans AS plan ON plan.id = execution.plan_id
			JOIN assets AS asset ON asset.id = plan.asset_id AND asset.status = 'active'
			WHERE execution.status = 'planned'
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
		    signing_attempts = execution.signing_attempts + 1,
		    last_error = NULL,
		    updated_at = clock_timestamp()
		FROM candidate
		JOIN sweep_plans AS plan ON plan.id = candidate.plan_id
		JOIN assets AS asset ON asset.id = plan.asset_id AND asset.status = 'active'
		WHERE execution.plan_id = candidate.plan_id
		RETURNING execution.plan_id::TEXT, execution.lease_epoch,
		          asset.network, plan.source_address, asset.contract_address,
		          plan.destination_address, plan.amount::TEXT
	`, workerID, leaseDuration.Milliseconds(), network).Scan(
		&claim.PlanID, &claim.LeaseEpoch, &claim.Network, &claim.SourceAddress,
		&claim.ContractAddress, &claim.DestinationAddress, &claim.Amount,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return SigningClaim{}, ErrNoSigningJob
	}
	if err != nil {
		return SigningClaim{}, fmt.Errorf("领取归集签名任务: %w", err)
	}
	return claim, nil
}

// CompleteSigning 保存余额观察和唯一签名交易，并交给广播阶段。
func (store *Store) CompleteSigning(
	ctx context.Context,
	claim SigningClaim,
	observation BalanceObservation,
	transaction tron.SignedTransaction,
) error {
	if err := validateSigningClaim(claim); err != nil || !validObservation(observation, claim.Amount) {
		return ErrInvalidSigningClaim
	}
	metadata, metadataErr := tron.ParseSignedTransactionMetadata(transaction)
	if !transactionIDPattern.MatchString(transaction.ID) || len(transaction.Payload) == 0 ||
		len(transaction.Payload) > maxSignedTransactionBytes || metadataErr != nil {
		return ErrInvalidSignedTransaction
	}
	result, err := store.db.Exec(ctx, `
		UPDATE sweep_executions
		SET status = 'ready_for_broadcast',
		    observed_balance = $4,
		    observed_block_height = $5,
		    observed_block_hash = $6,
		    transaction_id = $7,
		    signed_transaction = $8,
		    transaction_expires_at = $9,
		    lease_owner = NULL,
		    lease_until = NULL,
		    last_error = NULL,
		    updated_at = clock_timestamp()
		WHERE plan_id = $1
		  AND status = 'planned'
		  AND lease_owner = $2
		  AND lease_epoch = $3
		  AND lease_until > clock_timestamp()
	`, claim.PlanID, claim.WorkerID, claim.LeaseEpoch, observation.Balance,
		observation.BlockHeight, observation.BlockHash, transaction.ID, transaction.Payload, metadata.ExpiresAt)
	if err != nil {
		var databaseError *pgconn.PgError
		if errors.As(err, &databaseError) && databaseError.ConstraintName == "sweep_executions_transaction_id_unique_idx" {
			return ErrSignedTransactionConflict
		}
		return fmt.Errorf("保存归集签名结果: %w", err)
	}
	if result.RowsAffected() != 1 {
		return ErrSigningLeaseLost
	}
	return nil
}

// ReleaseSigning 记录可重试失败类别并释放租约。
func (store *Store) ReleaseSigning(ctx context.Context, claim SigningClaim, failureCode string) error {
	if err := validateSigningClaim(claim); err != nil {
		return err
	}
	failureCode = strings.TrimSpace(failureCode)
	if !failureCodePattern.MatchString(failureCode) {
		return ErrInvalidSigningClaim
	}
	result, err := store.db.Exec(ctx, `
		UPDATE sweep_executions
		SET lease_owner = NULL,
		    lease_until = NULL,
		    last_error = $4,
		    updated_at = clock_timestamp()
		WHERE plan_id = $1
		  AND status = 'planned'
		  AND lease_owner = $2
		  AND lease_epoch = $3
	`, claim.PlanID, claim.WorkerID, claim.LeaseEpoch, failureCode)
	if err != nil {
		return fmt.Errorf("释放归集签名任务: %w", err)
	}
	if result.RowsAffected() != 1 {
		return ErrSigningLeaseLost
	}
	return nil
}

func validateSigningClaim(claim SigningClaim) error {
	if !identity.ValidUUID(strings.TrimSpace(claim.PlanID)) || strings.TrimSpace(claim.WorkerID) == "" ||
		len(claim.WorkerID) > 128 || claim.LeaseEpoch <= 0 {
		return ErrInvalidSigningClaim
	}
	return nil
}

func validObservation(observation BalanceObservation, requiredAmount string) bool {
	if observation.BlockHeight <= 0 || !transactionIDPattern.MatchString(observation.BlockHash) {
		return false
	}
	balance, balanceOK := parsePositiveAmount(observation.Balance)
	required, requiredOK := parsePositiveAmount(requiredAmount)
	return balanceOK && requiredOK && balance.Cmp(required) >= 0
}
