package wallet

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/eightyun/stablecoin-gateway/internal/chain/tron"
	"github.com/eightyun/stablecoin-gateway/internal/identity"
	"github.com/jackc/pgx/v5"
)

var (
	ErrLedgerUnavailable = errors.New("托管账本科目不存在或余额无效")
	ErrLedgerChanged     = errors.New("钱包采集期间托管账本发生变化")
)

// LedgerCheckpoint 是钱包余额采集期间保持稳定的托管账本状态。
type LedgerCheckpoint struct {
	AccountID            string
	EntryCount           int64
	Balance              string
	PayoutInFlightCount  int64
	PayoutInFlightAmount string
	SweepInFlightCount   int64
	SweepInFlightAmount  string
}

type checkpointQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// LedgerCheckpoint 返回指定资产当前的托管账本检查点。
func (store *Store) LedgerCheckpoint(ctx context.Context, assetID string) (LedgerCheckpoint, error) {
	return readLedgerCheckpoint(ctx, store.db, strings.TrimSpace(assetID))
}

// CaptureSnapshot 在账本和在途资金检查点稳定时形成可对账的钱包快照。
func (store *Store) CaptureSnapshot(
	ctx context.Context,
	reader tron.TokenBalanceReader,
	asset Asset,
	wallets []Wallet,
) (Snapshot, error) {
	before, err := store.LedgerCheckpoint(ctx, asset.ID)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot, err := CollectSnapshot(ctx, reader, asset, wallets)
	if err != nil {
		return Snapshot{}, err
	}
	after, err := store.LedgerCheckpoint(ctx, asset.ID)
	if err != nil {
		return Snapshot{}, err
	}
	if before != after {
		return Snapshot{}, ErrLedgerChanged
	}
	snapshot.Ledger = before
	return snapshot, nil
}

func readLedgerCheckpoint(
	ctx context.Context,
	querier checkpointQuerier,
	assetID string,
) (LedgerCheckpoint, error) {
	if assetID == "" {
		return LedgerCheckpoint{}, ErrLedgerUnavailable
	}
	var checkpoint LedgerCheckpoint
	err := querier.QueryRow(ctx, `
		WITH payout_in_flight AS (
			SELECT COUNT(*) AS count, COALESCE(SUM(amount), 0) AS amount
			FROM payouts
			WHERE asset_id = $1
			  AND (
			      status = 'confirming'
			      OR (status = 'ready_for_broadcast' AND broadcast_attempts > 0)
			  )
		), sweep_in_flight AS (
			SELECT COUNT(*) AS count, COALESCE(SUM(plan.amount), 0) AS amount
			FROM sweep_executions AS execution
			JOIN sweep_plans AS plan ON plan.id = execution.plan_id
			WHERE plan.asset_id = $1
			  AND (
			      execution.status = 'confirming'
			      OR (execution.status = 'ready_for_broadcast' AND execution.broadcast_attempts > 0)
			  )
		)
		SELECT account.id::TEXT,
		       COUNT(journal.id),
		       COALESCE(SUM(
		           CASE
		               WHEN journal.id IS NULL THEN 0
		               WHEN entry.side = account.normal_side THEN entry.amount
		               ELSE -entry.amount
		           END
		       ), 0)::TEXT,
		       payout_in_flight.count,
		       payout_in_flight.amount::TEXT,
		       sweep_in_flight.count,
		       sweep_in_flight.amount::TEXT
		FROM ledger_accounts AS account
		CROSS JOIN payout_in_flight
		CROSS JOIN sweep_in_flight
		LEFT JOIN journal_entries AS entry ON entry.account_id = account.id
		LEFT JOIN journal_transactions AS journal
		  ON journal.id = entry.transaction_id AND journal.status = 'posted'
		WHERE account.owner_type = 'platform'
		  AND account.owner_id = 'gateway'
		  AND account.asset_id = $1
		  AND account.code = 'custody'
		  AND account.normal_side = 'D'
		  AND account.status IN ('active', 'locked')
		GROUP BY account.id, account.normal_side,
		         payout_in_flight.count, payout_in_flight.amount,
		         sweep_in_flight.count, sweep_in_flight.amount
	`, assetID).Scan(
		&checkpoint.AccountID, &checkpoint.EntryCount,
		&checkpoint.Balance, &checkpoint.PayoutInFlightCount,
		&checkpoint.PayoutInFlightAmount, &checkpoint.SweepInFlightCount,
		&checkpoint.SweepInFlightAmount,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return LedgerCheckpoint{}, ErrLedgerUnavailable
	}
	if err != nil {
		return LedgerCheckpoint{}, fmt.Errorf("读取托管账本检查点: %w", err)
	}
	if !identity.ValidUUID(checkpoint.AccountID) || checkpoint.EntryCount < 0 ||
		checkpoint.PayoutInFlightCount < 0 || checkpoint.SweepInFlightCount < 0 {
		return LedgerCheckpoint{}, ErrLedgerUnavailable
	}
	if _, valid := parseAmount(checkpoint.Balance); !valid {
		return LedgerCheckpoint{}, ErrLedgerUnavailable
	}
	payoutAmount, valid := parseAmount(checkpoint.PayoutInFlightAmount)
	if !valid {
		return LedgerCheckpoint{}, ErrLedgerUnavailable
	}
	sweepAmount, valid := parseAmount(checkpoint.SweepInFlightAmount)
	if !valid {
		return LedgerCheckpoint{}, ErrLedgerUnavailable
	}
	if (checkpoint.PayoutInFlightCount == 0) != (payoutAmount.Sign() == 0) ||
		(checkpoint.SweepInFlightCount == 0) != (sweepAmount.Sign() == 0) {
		return LedgerCheckpoint{}, ErrLedgerUnavailable
	}
	return checkpoint, nil
}
