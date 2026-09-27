package wallet

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"

	"github.com/eightyun/stablecoin-gateway/internal/identity"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrDatabaseRequired = errors.New("数据库连接不能为空")
	ErrAssetUnavailable = errors.New("资产不存在或不可用")
	ErrNoActiveWallets  = errors.New("资产没有活动托管钱包")
	ErrWalletSetChanged = errors.New("采集期间活动钱包集合发生变化")
)

// Store 使用 PostgreSQL 保存托管钱包及其不可变余额快照。
type Store struct {
	db *pgxpool.Pool
}

// NewStore 创建钱包 Store。
func NewStore(db *pgxpool.Pool) (*Store, error) {
	if db == nil {
		return nil, ErrDatabaseRequired
	}
	return &Store{db: db}, nil
}

// ActiveWallets 返回活动资产及其全部活动托管钱包。
func (store *Store) ActiveWallets(ctx context.Context, assetID string) (Asset, []Wallet, error) {
	assetID = strings.TrimSpace(assetID)
	if assetID == "" {
		return Asset{}, nil, ErrAssetUnavailable
	}
	var asset Asset
	if err := store.db.QueryRow(ctx, `
		SELECT id, network, contract_address
		FROM assets
		WHERE id = $1 AND status = 'active'
	`, assetID).Scan(&asset.ID, &asset.Network, &asset.ContractAddress); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Asset{}, nil, ErrAssetUnavailable
		}
		return Asset{}, nil, fmt.Errorf("查询快照资产: %w", err)
	}
	rows, err := store.db.Query(ctx, `
		SELECT id::TEXT, asset_id, address, role
		FROM custody_wallets
		WHERE asset_id = $1 AND status = 'active'
		ORDER BY id
	`, asset.ID)
	if err != nil {
		return Asset{}, nil, fmt.Errorf("查询活动托管钱包: %w", err)
	}
	defer rows.Close()
	wallets := make([]Wallet, 0)
	for rows.Next() {
		var item Wallet
		if err := rows.Scan(&item.ID, &item.AssetID, &item.Address, &item.Role); err != nil {
			return Asset{}, nil, fmt.Errorf("读取活动托管钱包: %w", err)
		}
		wallets = append(wallets, item)
	}
	if err := rows.Err(); err != nil {
		return Asset{}, nil, fmt.Errorf("遍历活动托管钱包: %w", err)
	}
	if len(wallets) == 0 {
		return Asset{}, nil, ErrNoActiveWallets
	}
	return asset, wallets, nil
}

// SaveSnapshot 在可串行化事务中重新核对钱包集合并原子保存快照。
func (store *Store) SaveSnapshot(ctx context.Context, snapshot Snapshot) (err error) {
	if err := validateSnapshot(snapshot); err != nil {
		return err
	}
	transaction, err := store.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return fmt.Errorf("开始保存钱包快照事务: %w", err)
	}
	defer func() {
		if rollbackErr := transaction.Rollback(context.Background()); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			err = errors.Join(err, fmt.Errorf("回滚钱包快照事务: %w", rollbackErr))
		}
	}()

	var network, contractAddress string
	if err = transaction.QueryRow(ctx, `
		SELECT network, contract_address
		FROM assets
		WHERE id = $1 AND status = 'active'
	`, snapshot.Asset.ID).Scan(&network, &contractAddress); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrAssetUnavailable
		}
		return fmt.Errorf("锁定快照资产: %w", err)
	}
	if network != snapshot.Asset.Network || contractAddress != snapshot.Asset.ContractAddress {
		return ErrInvalidSnapshot
	}
	checkpoint, err := readLedgerCheckpoint(ctx, transaction, snapshot.Asset.ID)
	if err != nil {
		return err
	}
	if checkpoint.InFlightPayouts != 0 {
		return ErrPayoutInFlight
	}
	if checkpoint != snapshot.Ledger {
		return ErrLedgerChanged
	}

	currentWalletIDs, err := activeWalletIDs(ctx, transaction, snapshot.Asset.ID)
	if err != nil {
		return err
	}
	snapshotWalletIDs := make([]string, 0, len(snapshot.Balances))
	for _, balance := range snapshot.Balances {
		snapshotWalletIDs = append(snapshotWalletIDs, balance.WalletID)
	}
	sort.Strings(snapshotWalletIDs)
	if !equalStrings(currentWalletIDs, snapshotWalletIDs) {
		return ErrWalletSetChanged
	}

	if _, err = transaction.Exec(ctx, `
		INSERT INTO wallet_balance_snapshot_runs (
			id, asset_id, block_height, block_hash, block_time, wallet_count, total_balance,
			ledger_account_id, ledger_entry_count, ledger_balance
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`, snapshot.ID, snapshot.Asset.ID, int64(snapshot.Block.Height), snapshot.Block.Hash,
		snapshot.Block.Timestamp, len(snapshot.Balances), snapshot.TotalBalance,
		snapshot.Ledger.AccountID, snapshot.Ledger.EntryCount, snapshot.Ledger.Balance); err != nil {
		return fmt.Errorf("保存钱包快照运行: %w", err)
	}
	for _, balance := range snapshot.Balances {
		if _, err = transaction.Exec(ctx, `
			INSERT INTO wallet_balance_snapshots (run_id, wallet_id, asset_id, balance)
			VALUES ($1, $2, $3, $4)
		`, snapshot.ID, balance.WalletID, snapshot.Asset.ID, balance.Amount); err != nil {
			return fmt.Errorf("保存钱包余额快照: %w", err)
		}
	}
	if err = transaction.Commit(ctx); err != nil {
		return fmt.Errorf("提交钱包快照事务: %w", err)
	}
	return nil
}

func activeWalletIDs(ctx context.Context, transaction pgx.Tx, assetID string) ([]string, error) {
	rows, err := transaction.Query(ctx, `
		SELECT id::TEXT
		FROM custody_wallets
		WHERE asset_id = $1 AND status = 'active'
		ORDER BY id
	`, assetID)
	if err != nil {
		return nil, fmt.Errorf("复核活动托管钱包: %w", err)
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("读取活动托管钱包标识: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历活动托管钱包标识: %w", err)
	}
	return ids, nil
}

func validateSnapshot(snapshot Snapshot) error {
	if !identity.ValidUUID(snapshot.ID) || !validHeader(snapshot.Block) || len(snapshot.Balances) == 0 ||
		strings.TrimSpace(snapshot.Asset.ID) == "" || strings.TrimSpace(snapshot.Asset.Network) == "" ||
		strings.TrimSpace(snapshot.Asset.ContractAddress) == "" ||
		!identity.ValidUUID(snapshot.Ledger.AccountID) || snapshot.Ledger.EntryCount < 0 ||
		snapshot.Ledger.InFlightPayouts != 0 {
		return ErrInvalidSnapshot
	}
	if _, valid := parseAmount(snapshot.Ledger.Balance); !valid {
		return ErrInvalidSnapshot
	}
	seen := make(map[string]struct{}, len(snapshot.Balances))
	total := new(big.Int)
	for _, balance := range snapshot.Balances {
		if !identity.ValidUUID(balance.WalletID) {
			return ErrInvalidSnapshot
		}
		if _, exists := seen[balance.WalletID]; exists {
			return ErrInvalidSnapshot
		}
		seen[balance.WalletID] = struct{}{}
		amount, valid := parseAmount(balance.Amount)
		if !valid {
			return ErrInvalidSnapshot
		}
		total.Add(total, amount)
		if total.BitLen() > 256 || len(total.String()) > 78 {
			return ErrInvalidSnapshot
		}
	}
	if total.String() != snapshot.TotalBalance {
		return ErrInvalidSnapshot
	}
	return nil
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
