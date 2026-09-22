// Package indexer 管理 TRON 已固化区块扫描的持久化游标。
package indexer

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/chain/tron"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrDatabaseRequired  = errors.New("数据库连接不能为空")
	ErrInvalidCursor     = errors.New("扫描游标配置无效")
	ErrAnchorConflict    = errors.New("扫描起点与已有锚点不一致")
	ErrLeaseUnavailable  = errors.New("扫描游标租约不可用")
	ErrLeaseLost         = errors.New("扫描游标租约已失效")
	ErrInvalidBlock      = errors.New("区块与扫描游标不连续")
	ErrTransactionNeeded = errors.New("推进游标需要数据库事务")
)

// Claim 是一次扫描租约的带栅栏令牌快照。
type Claim struct {
	Network      string
	WorkerID     string
	NextHeight   int64
	PreviousHash string
	LeaseEpoch   int64
}

// CursorStore 将租约和游标存于 PostgreSQL。
type CursorStore struct {
	db *pgxpool.Pool
}

// NewCursorStore 创建 PostgreSQL 游标仓储。
func NewCursorStore(db *pgxpool.Pool) (*CursorStore, error) {
	if db == nil {
		return nil, ErrDatabaseRequired
	}
	return &CursorStore{db: db}, nil
}

// Ensure 只在首次启动时创建游标；已有游标必须与配置的扫描锚点一致。
func (store *CursorStore) Ensure(ctx context.Context, network string, startHeight int64, anchorHash string) error {
	if strings.TrimSpace(network) == "" || startHeight <= 0 || strings.TrimSpace(anchorHash) == "" {
		return ErrInvalidCursor
	}
	_, err := store.db.Exec(ctx, `
		INSERT INTO chain_scan_cursors (
			network, start_height, anchor_hash, next_height, previous_hash
		) VALUES ($1, $2, $3, $2, $3)
		ON CONFLICT (network) DO NOTHING
	`, network, startHeight, anchorHash)
	if err != nil {
		return fmt.Errorf("初始化扫描游标: %w", err)
	}

	var existingHeight int64
	var existingHash string
	if err := store.db.QueryRow(ctx, `
		SELECT start_height, anchor_hash
		FROM chain_scan_cursors
		WHERE network = $1
	`, network).Scan(&existingHeight, &existingHash); err != nil {
		return fmt.Errorf("核对扫描锚点: %w", err)
	}
	if existingHeight != startHeight || existingHash != anchorHash {
		return ErrAnchorConflict
	}
	return nil
}

// Claim 原子领取游标。过期租约可由其他 Worker 接管，租约代数用于阻止旧持有者推进。
func (store *CursorStore) Claim(ctx context.Context, network, workerID string, duration time.Duration) (Claim, error) {
	if strings.TrimSpace(network) == "" || strings.TrimSpace(workerID) == "" || duration.Milliseconds() <= 0 {
		return Claim{}, ErrInvalidCursor
	}
	claim := Claim{Network: network, WorkerID: workerID}
	err := store.db.QueryRow(ctx, `
		UPDATE chain_scan_cursors
		SET lease_owner = $2,
			lease_until = clock_timestamp() + ($3 * INTERVAL '1 millisecond'),
			lease_epoch = lease_epoch + 1,
			updated_at = clock_timestamp()
		WHERE network = $1
		  AND (lease_owner IS NULL OR lease_until <= clock_timestamp())
		RETURNING next_height, previous_hash, lease_epoch
	`, network, workerID, duration.Milliseconds()).Scan(
		&claim.NextHeight, &claim.PreviousHash, &claim.LeaseEpoch,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Claim{}, ErrLeaseUnavailable
	}
	if err != nil {
		return Claim{}, fmt.Errorf("领取扫描游标: %w", err)
	}
	return claim, nil
}

// AdvanceTx 在调用方事务中推进游标。链事件必须先在同一事务中持久化。
func (store *CursorStore) AdvanceTx(ctx context.Context, transaction pgx.Tx, claim Claim, block tron.Header) error {
	if transaction == nil {
		return ErrTransactionNeeded
	}
	if claim.Network == "" || claim.WorkerID == "" || claim.LeaseEpoch <= 0 ||
		claim.NextHeight <= 0 || claim.NextHeight >= math.MaxInt64 ||
		uint64(claim.NextHeight) != block.Height ||
		claim.PreviousHash == "" || block.Hash == "" || block.Hash == claim.PreviousHash ||
		block.ParentHash != claim.PreviousHash {
		return ErrInvalidBlock
	}
	result, err := transaction.Exec(ctx, `
		UPDATE chain_scan_cursors
		SET next_height = next_height + 1,
			previous_hash = $6,
			lease_owner = NULL,
			lease_until = NULL,
			updated_at = clock_timestamp()
		WHERE network = $1
		  AND lease_owner = $2
		  AND lease_epoch = $3
		  AND lease_until > clock_timestamp()
		  AND next_height = $4
		  AND previous_hash = $5
	`, claim.Network, claim.WorkerID, claim.LeaseEpoch, claim.NextHeight, claim.PreviousHash, block.Hash)
	if err != nil {
		return fmt.Errorf("推进扫描游标: %w", err)
	}
	if result.RowsAffected() != 1 {
		return ErrLeaseLost
	}
	return nil
}

// Release 释放当前租约。租约被接管后不会影响新的持有者。
func (store *CursorStore) Release(ctx context.Context, claim Claim) error {
	result, err := store.db.Exec(ctx, `
		UPDATE chain_scan_cursors
		SET lease_owner = NULL,
			lease_until = NULL,
			updated_at = clock_timestamp()
		WHERE network = $1
		  AND lease_owner = $2
		  AND lease_epoch = $3
	`, claim.Network, claim.WorkerID, claim.LeaseEpoch)
	if err != nil {
		return fmt.Errorf("释放扫描游标租约: %w", err)
	}
	if result.RowsAffected() != 1 {
		return ErrLeaseLost
	}
	return nil
}
