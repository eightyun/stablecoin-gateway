package deposit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/identity"
	"github.com/eightyun/stablecoin-gateway/internal/ledger"
	"github.com/eightyun/stablecoin-gateway/internal/outbox"
	"github.com/jackc/pgx/v5"
)

const (
	platformLedgerOwnerID = "gateway"
	custodyAccountCode    = "custody"
	availableAccountCode  = "available"
	frozenAccountCode     = "frozen"
)

type matchedIntent struct {
	ID                string
	MerchantID        string
	AssetID           string
	MerchantReference string
	CreatedAt         time.Time
	ExpiresAt         time.Time
	ExpectedAmount    string
	ReceivedAmount    string
}

type depositConfirmedPayload struct {
	DepositIntentID     string `json:"deposit_intent_id"`
	MerchantID          string `json:"merchant_id"`
	MerchantReference   string `json:"merchant_reference"`
	AssetID             string `json:"asset_id"`
	Amount              string `json:"amount"`
	LedgerTransactionID string `json:"ledger_transaction_id"`
}

func applyMatchedPayment(
	ctx context.Context,
	transaction pgx.Tx,
	key eventKey,
	addressID string,
	intent matchedIntent,
	amount string,
) (MatchResult, error) {
	receivedAmount, comparison, err := accumulatedAmount(intent.ReceivedAmount, amount, intent.ExpectedAmount)
	if err != nil {
		return MatchResult{}, fmt.Errorf("计算充值累计金额: %w", err)
	}
	if comparison > 0 {
		if err := updateIntentAmount(ctx, transaction, intent.ID, receivedAmount, "overpaid"); err != nil {
			return MatchResult{}, err
		}
		result, err := recordReview(ctx, transaction, key, addressID, intent.ID, amount, "overpaid")
		result.IntentStatus = "overpaid"
		result.ReceivedAmount = receivedAmount
		return result, err
	}
	if comparison < 0 {
		if err := updateIntentAmount(ctx, transaction, intent.ID, receivedAmount, "partially_paid"); err != nil {
			return MatchResult{}, err
		}
		return recordMatch(ctx, transaction, key, addressID, intent.ID, amount, "partially_paid", receivedAmount)
	}
	return creditExactPayment(ctx, transaction, key, addressID, intent, amount, receivedAmount)
}

func creditExactPayment(
	ctx context.Context,
	transaction pgx.Tx,
	key eventKey,
	addressID string,
	intent matchedIntent,
	eventAmount string,
	receivedAmount string,
) (MatchResult, error) {
	amount, err := strconv.ParseInt(receivedAmount, 10, 64)
	if err != nil || amount <= 0 {
		result, reviewErr := recordReview(ctx, transaction, key, addressID, intent.ID, eventAmount, "amount_out_of_range")
		result.ReceivedAmount = intent.ReceivedAmount
		return result, reviewErr
	}
	custodyAccountID, availableAccountID, found, err := findPostingAccounts(ctx, transaction, intent.MerchantID, intent.AssetID)
	if err != nil {
		return MatchResult{}, err
	}
	if !found {
		result, reviewErr := recordReview(ctx, transaction, key, addressID, intent.ID, eventAmount, "ledger_account_unavailable")
		result.ReceivedAmount = intent.ReceivedAmount
		return result, reviewErr
	}

	journalID, err := identity.NewUUID()
	if err != nil {
		return MatchResult{}, err
	}
	postResult, err := ledger.PostInTransaction(ctx, transaction, ledger.Transaction{
		ID:             journalID,
		RequesterType:  "system",
		RequesterID:    "deposit",
		IdempotencyKey: "credit:" + intent.ID,
		ReferenceType:  "deposit",
		ReferenceID:    intent.ID,
		Entries: []ledger.Entry{
			{AccountID: custodyAccountID, AssetID: intent.AssetID, Side: ledger.Debit, Amount: amount},
			{AccountID: availableAccountID, AssetID: intent.AssetID, Side: ledger.Credit, Amount: amount},
		},
	})
	if err != nil {
		return MatchResult{}, fmt.Errorf("充值过账: %w", err)
	}
	if err := markIntentCredited(ctx, transaction, intent.ID, receivedAmount, postResult.TransactionID); err != nil {
		return MatchResult{}, err
	}
	result, err := recordMatch(ctx, transaction, key, addressID, intent.ID, eventAmount, "paid", receivedAmount)
	if err != nil {
		return MatchResult{}, err
	}
	if err := enqueueDepositConfirmed(ctx, transaction, intent, receivedAmount, postResult.TransactionID); err != nil {
		return MatchResult{}, err
	}
	result.Credited = true
	result.LedgerTransactionID = postResult.TransactionID
	return result, nil
}

func accumulatedAmount(current, incoming, expected string) (string, int, error) {
	currentValue, valid := new(big.Int).SetString(current, 10)
	if !valid || currentValue.Sign() < 0 {
		return "", 0, errors.New("当前到账金额无效")
	}
	incomingValue, valid := new(big.Int).SetString(incoming, 10)
	if !valid || incomingValue.Sign() <= 0 {
		return "", 0, errors.New("链事件金额无效")
	}
	expectedValue, valid := new(big.Int).SetString(expected, 10)
	if !valid || expectedValue.Sign() <= 0 {
		return "", 0, errors.New("预期金额无效")
	}
	result := new(big.Int).Add(currentValue, incomingValue)
	return result.String(), result.Cmp(expectedValue), nil
}

func findPostingAccounts(ctx context.Context, transaction pgx.Tx, merchantID, assetID string) (string, string, bool, error) {
	var custodyAccountID, availableAccountID string
	err := transaction.QueryRow(ctx, `
		SELECT custody.id::TEXT, available.id::TEXT
		FROM ledger_accounts AS custody
		JOIN ledger_accounts AS available ON available.asset_id = custody.asset_id
		WHERE custody.owner_type = 'platform'
		  AND custody.owner_id = $1
		  AND custody.asset_id = $2
		  AND custody.code = $3
		  AND custody.normal_side = 'D'
		  AND custody.status = 'active'
		  AND available.owner_type = 'merchant'
		  AND available.owner_id = $4
		  AND available.code = $5
		  AND available.normal_side = 'C'
		  AND available.status = 'active'
		FOR SHARE OF custody, available
	`, platformLedgerOwnerID, assetID, custodyAccountCode, merchantID, availableAccountCode).Scan(
		&custodyAccountID, &availableAccountID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, fmt.Errorf("查询充值账本科目: %w", err)
	}
	return custodyAccountID, availableAccountID, true, nil
}

func updateIntentAmount(ctx context.Context, transaction pgx.Tx, intentID, receivedAmount, status string) error {
	command, err := transaction.Exec(ctx, `
		UPDATE deposit_intents
		SET received_amount = $2::NUMERIC, status = $3, updated_at = clock_timestamp()
		WHERE id = $1
	`, intentID, receivedAmount, status)
	if err != nil {
		return fmt.Errorf("累计充值金额: %w", err)
	}
	if command.RowsAffected() != 1 {
		return errors.New("充值意图状态已变化")
	}
	return nil
}

func markIntentCredited(ctx context.Context, transaction pgx.Tx, intentID, receivedAmount, journalID string) error {
	command, err := transaction.Exec(ctx, `
		UPDATE deposit_intents
		SET received_amount = $2::NUMERIC,
		    status = 'paid',
		    ledger_transaction_id = $3,
		    credited_amount = $2::NUMERIC,
		    credited_at = clock_timestamp(),
		    updated_at = clock_timestamp()
		WHERE id = $1 AND ledger_transaction_id IS NULL
	`, intentID, receivedAmount, journalID)
	if err != nil {
		return fmt.Errorf("标记充值已入账: %w", err)
	}
	if command.RowsAffected() != 1 {
		return errors.New("充值意图已入账或状态已变化")
	}
	return nil
}

func recordMatch(
	ctx context.Context,
	transaction pgx.Tx,
	key eventKey,
	addressID, intentID, amount, intentStatus, receivedAmount string,
) (MatchResult, error) {
	_, err := transaction.Exec(ctx, `
		INSERT INTO deposit_event_matches (
			network, contract, transaction_id, log_index, deposit_address_id,
			deposit_intent_id, status, amount
		) VALUES ($1, $2, $3, $4, $5, $6, 'matched', $7)
	`, key.network, key.contract, key.transactionID, key.logIndex, addressID, intentID, amount)
	if err != nil {
		return MatchResult{}, fmt.Errorf("记录充值匹配: %w", err)
	}
	return MatchResult{
		Processed: true, TransactionID: key.transactionID, LogIndex: uint32(key.logIndex),
		IntentID: intentID, MatchStatus: "matched", IntentStatus: intentStatus, ReceivedAmount: receivedAmount,
	}, nil
}

func enqueueDepositConfirmed(
	ctx context.Context,
	transaction pgx.Tx,
	intent matchedIntent,
	amount, journalID string,
) error {
	payload, err := json.Marshal(depositConfirmedPayload{
		DepositIntentID: intent.ID, MerchantID: intent.MerchantID, MerchantReference: intent.MerchantReference,
		AssetID: intent.AssetID, Amount: amount, LedgerTransactionID: journalID,
	})
	if err != nil {
		return fmt.Errorf("编码充值确认事件: %w", err)
	}
	eventID, err := identity.NewUUID()
	if err != nil {
		return err
	}
	if err := outbox.EnqueueInTransaction(ctx, transaction, outbox.PendingEvent{
		ID: eventID, Topic: "deposit.confirmed", AggregateType: "deposit", AggregateID: intent.ID, Payload: payload,
	}); err != nil {
		return fmt.Errorf("创建充值确认事件: %w", err)
	}
	return nil
}
