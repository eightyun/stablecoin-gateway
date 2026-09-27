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
	ErrPayoutInFlight    = errors.New("存在待广播或待确认出款")
)

// LedgerCheckpoint 是钱包余额采集期间保持稳定的托管账本状态。
type LedgerCheckpoint struct {
	AccountID       string
	EntryCount      int64
	Balance         string
	InFlightPayouts int64
}

type checkpointQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// LedgerCheckpoint 返回指定资产当前的托管账本检查点。
func (store *Store) LedgerCheckpoint(ctx context.Context, assetID string) (LedgerCheckpoint, error) {
	return readLedgerCheckpoint(ctx, store.db, strings.TrimSpace(assetID))
}

// CaptureSnapshot 只在账本稳定且没有链上在途出款时形成可对账的钱包快照。
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
	if before.InFlightPayouts != 0 {
		return Snapshot{}, ErrPayoutInFlight
	}
	snapshot, err := CollectSnapshot(ctx, reader, asset, wallets)
	if err != nil {
		return Snapshot{}, err
	}
	after, err := store.LedgerCheckpoint(ctx, asset.ID)
	if err != nil {
		return Snapshot{}, err
	}
	if after.InFlightPayouts != 0 {
		return Snapshot{}, ErrPayoutInFlight
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
		SELECT account.id::TEXT,
		       COUNT(journal.id),
		       COALESCE(SUM(
		           CASE
		               WHEN journal.id IS NULL THEN 0
		               WHEN entry.side = account.normal_side THEN entry.amount
		               ELSE -entry.amount
		           END
		       ), 0)::TEXT,
		       (
		           SELECT COUNT(*)
		           FROM payouts
		           WHERE asset_id = $1
		             AND status IN ('ready_for_broadcast', 'confirming')
		       )
		FROM ledger_accounts AS account
		LEFT JOIN journal_entries AS entry ON entry.account_id = account.id
		LEFT JOIN journal_transactions AS journal
		  ON journal.id = entry.transaction_id AND journal.status = 'posted'
		WHERE account.owner_type = 'platform'
		  AND account.owner_id = 'gateway'
		  AND account.asset_id = $1
		  AND account.code = 'custody'
		  AND account.normal_side = 'D'
		  AND account.status IN ('active', 'locked')
		GROUP BY account.id, account.normal_side
	`, assetID).Scan(
		&checkpoint.AccountID, &checkpoint.EntryCount,
		&checkpoint.Balance, &checkpoint.InFlightPayouts,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return LedgerCheckpoint{}, ErrLedgerUnavailable
	}
	if err != nil {
		return LedgerCheckpoint{}, fmt.Errorf("读取托管账本检查点: %w", err)
	}
	if !identity.ValidUUID(checkpoint.AccountID) || checkpoint.EntryCount < 0 ||
		checkpoint.InFlightPayouts < 0 {
		return LedgerCheckpoint{}, ErrLedgerUnavailable
	}
	if _, valid := parseAmount(checkpoint.Balance); !valid {
		return LedgerCheckpoint{}, ErrLedgerUnavailable
	}
	return checkpoint, nil
}
