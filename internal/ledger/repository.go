package ledger

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

var (
	ErrDatabaseRequired    = errors.New("数据库连接不能为空")
	ErrIdempotencyConflict = errors.New("幂等键已被不同请求使用")
)

// PostResult 表示一次原子过账的结果。
type PostResult struct {
	TransactionID string
	Created       bool
}

// Repository 定义账务交易的持久化能力。
type Repository interface {
	Post(ctx context.Context, transaction Transaction) (PostResult, error)
}

// PostgreSQLRepository 使用 PostgreSQL 原子写入账务交易和分录。
type PostgreSQLRepository struct {
	db *sql.DB
}

// NewPostgreSQLRepository 创建 PostgreSQL 账本仓储。
func NewPostgreSQLRepository(db *sql.DB) (*PostgreSQLRepository, error) {
	if db == nil {
		return nil, ErrDatabaseRequired
	}
	return &PostgreSQLRepository{db: db}, nil
}

// Post 校验并原子写入交易。相同幂等请求返回首次创建的交易。
func (repository *PostgreSQLRepository) Post(ctx context.Context, transaction Transaction) (result PostResult, err error) {
	if err := ValidateTransaction(transaction); err != nil {
		return PostResult{}, err
	}

	requestHash, err := requestFingerprint(transaction)
	if err != nil {
		return PostResult{}, err
	}

	databaseTransaction, err := repository.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return PostResult{}, fmt.Errorf("开始账务事务: %w", err)
	}
	defer func() {
		if err == nil {
			return
		}
		rollbackErr := databaseTransaction.Rollback()
		if rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("回滚账务事务: %w", rollbackErr))
		}
	}()

	var transactionID string
	err = databaseTransaction.QueryRowContext(ctx, `
		INSERT INTO journal_transactions (
			id, requester_type, requester_id, idempotency_key,
			reference_type, reference_id, request_hash
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (requester_type, requester_id, idempotency_key) DO NOTHING
		RETURNING id
	`,
		transaction.ID,
		transaction.RequesterType,
		transaction.RequesterID,
		transaction.IdempotencyKey,
		transaction.ReferenceType,
		transaction.ReferenceID,
		requestHash,
	).Scan(&transactionID)
	if errors.Is(err, sql.ErrNoRows) {
		result, err = existingPostResult(ctx, databaseTransaction, transaction, requestHash)
		if err != nil {
			return PostResult{}, err
		}
		if err = databaseTransaction.Commit(); err != nil {
			return PostResult{}, fmt.Errorf("提交幂等查询事务: %w", err)
		}
		return result, nil
	}
	if err != nil {
		return PostResult{}, fmt.Errorf("创建账务交易: %w", err)
	}

	for index, entry := range transaction.Entries {
		_, err = databaseTransaction.ExecContext(ctx, `
			INSERT INTO journal_entries (
				transaction_id, line_no, account_id, asset_id, side, amount
			) VALUES ($1, $2, $3, $4, $5, $6)
		`,
			transactionID,
			index+1,
			entry.AccountID,
			entry.AssetID,
			databaseSide(entry.Side),
			entry.Amount,
		)
		if err != nil {
			return PostResult{}, fmt.Errorf("写入第 %d 条账务分录: %w", index+1, err)
		}
	}

	if err = databaseTransaction.Commit(); err != nil {
		return PostResult{}, fmt.Errorf("提交账务事务: %w", err)
	}

	return PostResult{TransactionID: transactionID, Created: true}, nil
}

func existingPostResult(ctx context.Context, databaseTransaction *sql.Tx, transaction Transaction, requestHash string) (PostResult, error) {
	var transactionID string
	var existingHash string
	err := databaseTransaction.QueryRowContext(ctx, `
		SELECT id, request_hash
		FROM journal_transactions
		WHERE requester_type = $1
		  AND requester_id = $2
		  AND idempotency_key = $3
	`, transaction.RequesterType, transaction.RequesterID, transaction.IdempotencyKey).Scan(&transactionID, &existingHash)
	if err != nil {
		return PostResult{}, fmt.Errorf("查询幂等账务交易: %w", err)
	}
	if existingHash != requestHash {
		return PostResult{}, ErrIdempotencyConflict
	}
	return PostResult{TransactionID: transactionID, Created: false}, nil
}

func requestFingerprint(transaction Transaction) (string, error) {
	payload := struct {
		RequesterType string
		RequesterID   string
		ReferenceType string
		ReferenceID   string
		Entries       []Entry
	}{
		RequesterType: transaction.RequesterType,
		RequesterID:   transaction.RequesterID,
		ReferenceType: transaction.ReferenceType,
		ReferenceID:   transaction.ReferenceID,
		Entries:       transaction.Entries,
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("生成账务请求摘要: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func databaseSide(side Side) string {
	if side == Debit {
		return "D"
	}
	return "C"
}
