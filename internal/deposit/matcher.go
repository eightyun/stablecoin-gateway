package deposit

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// MatchNext 匹配下一条尚未处理、且收款地址属于平台的链事件。
func (store *Store) MatchNext(ctx context.Context) (result MatchResult, err error) {
	transaction, err := store.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return MatchResult{}, fmt.Errorf("开始充值匹配事务: %w", err)
	}
	defer func() {
		if rollbackErr := transaction.Rollback(context.Background()); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			err = errors.Join(err, fmt.Errorf("回滚充值匹配事务: %w", rollbackErr))
		}
	}()

	var network, contract, transactionID, addressID, amount string
	var assetStatus, merchantStatus, addressStatus string
	var logIndex int64
	var blockTime *time.Time
	err = transaction.QueryRow(ctx, `
		SELECT event.network, event.contract, event.transaction_id, event.log_index,
		       event.block_time, event.amount::TEXT, address.id,
		       asset.status, merchant.status, address.status
		FROM chain_events AS event
		JOIN assets AS asset
		  ON asset.network = event.network
		 AND asset.contract_address = event.contract
		JOIN deposit_addresses AS address
		  ON address.asset_id = asset.id
		 AND address.address = event.to_address
		JOIN merchants AS merchant ON merchant.id = address.merchant_id
		LEFT JOIN deposit_event_matches AS match
		  ON match.network = event.network
		 AND match.contract = event.contract
		 AND match.transaction_id = event.transaction_id
		 AND match.log_index = event.log_index
		WHERE match.network IS NULL
		ORDER BY event.block_height, event.transaction_id, event.log_index
		FOR UPDATE OF event SKIP LOCKED
		LIMIT 1
	`).Scan(
		&network, &contract, &transactionID, &logIndex, &blockTime, &amount, &addressID,
		&assetStatus, &merchantStatus, &addressStatus,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return MatchResult{}, nil
	}
	if err != nil {
		return MatchResult{}, fmt.Errorf("领取待匹配链事件: %w", err)
	}

	var intent matchedIntent
	err = transaction.QueryRow(ctx, `
		SELECT id, merchant_id::TEXT, asset_id, merchant_reference,
		       created_at, expires_at, expected_amount::TEXT, received_amount::TEXT
		FROM deposit_intents
		WHERE deposit_address_id = $1
		FOR UPDATE
	`, addressID).Scan(
		&intent.ID, &intent.MerchantID, &intent.AssetID, &intent.MerchantReference,
		&intent.CreatedAt, &intent.ExpiresAt, &intent.ExpectedAmount, &intent.ReceivedAmount,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		result, err = recordReview(ctx, transaction, eventKey{network, contract, transactionID, logIndex}, addressID, "", amount, "no_intent")
	} else if err != nil {
		return MatchResult{}, fmt.Errorf("查询充值意图: %w", err)
	} else if assetStatus != "active" {
		result, err = recordReview(ctx, transaction, eventKey{network, contract, transactionID, logIndex}, addressID, intent.ID, amount, "asset_disabled")
	} else if merchantStatus != "active" {
		result, err = recordReview(ctx, transaction, eventKey{network, contract, transactionID, logIndex}, addressID, intent.ID, amount, "merchant_inactive")
	} else if addressStatus != "active" {
		result, err = recordReview(ctx, transaction, eventKey{network, contract, transactionID, logIndex}, addressID, intent.ID, amount, "address_retired")
	} else if blockTime == nil {
		result, err = recordReview(ctx, transaction, eventKey{network, contract, transactionID, logIndex}, addressID, intent.ID, amount, "missing_block_time")
	} else if blockTime.Before(intent.CreatedAt) {
		result, err = recordReview(ctx, transaction, eventKey{network, contract, transactionID, logIndex}, addressID, intent.ID, amount, "before_intent")
	} else if blockTime.After(intent.ExpiresAt) {
		result, err = recordReview(ctx, transaction, eventKey{network, contract, transactionID, logIndex}, addressID, intent.ID, amount, "after_expiry")
	} else {
		result, err = applyMatchedPayment(
			ctx, transaction, eventKey{network, contract, transactionID, logIndex}, addressID, intent, amount,
		)
	}
	if err != nil {
		return MatchResult{}, err
	}
	if err = transaction.Commit(ctx); err != nil {
		return MatchResult{}, fmt.Errorf("提交充值匹配事务: %w", err)
	}
	return result, nil
}

// ExpireDue 将已到期的未付款和部分付款意图转为终态。
func (store *Store) ExpireDue(ctx context.Context, limit int) (int64, error) {
	if limit <= 0 || limit > 1000 {
		return 0, ErrInvalidLimit
	}
	command, err := store.db.Exec(ctx, `
		WITH due AS (
			SELECT id
			FROM deposit_intents
			WHERE expires_at < clock_timestamp()
			  AND status IN ('pending', 'partially_paid')
			ORDER BY expires_at, id
			FOR UPDATE SKIP LOCKED
			LIMIT $1
		)
		UPDATE deposit_intents AS intent
		SET status = CASE WHEN intent.received_amount = 0 THEN 'expired' ELSE 'underpaid' END,
		    updated_at = clock_timestamp()
		FROM due
		WHERE intent.id = due.id
	`, limit)
	if err != nil {
		return 0, fmt.Errorf("关闭到期充值意图: %w", err)
	}
	return command.RowsAffected(), nil
}

type eventKey struct {
	network       string
	contract      string
	transactionID string
	logIndex      int64
}

func recordReview(ctx context.Context, transaction pgx.Tx, key eventKey, addressID, intentID, amount, reason string) (MatchResult, error) {
	var nullableIntent any
	if intentID != "" {
		nullableIntent = intentID
	}
	_, err := transaction.Exec(ctx, `
		INSERT INTO deposit_event_matches (
			network, contract, transaction_id, log_index, deposit_address_id,
			deposit_intent_id, status, reason, amount
		) VALUES ($1, $2, $3, $4, $5, $6, 'review', $7, $8)
	`, key.network, key.contract, key.transactionID, key.logIndex, addressID, nullableIntent, reason, amount)
	if err != nil {
		return MatchResult{}, fmt.Errorf("记录待复核充值: %w", err)
	}
	return MatchResult{
		Processed: true, TransactionID: key.transactionID, LogIndex: uint32(key.logIndex),
		IntentID: intentID, MatchStatus: "review", Reason: reason,
	}, nil
}
