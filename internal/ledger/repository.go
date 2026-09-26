package ledger

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrDatabaseRequired    = errors.New("数据库连接不能为空")
	ErrTransactionRequired = errors.New("数据库事务不能为空")
	ErrIdempotencyConflict = errors.New("幂等键或业务引用已被不同请求使用")
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
	db *pgxpool.Pool
}

// NewPostgreSQLRepository 创建 PostgreSQL 账本仓储。
func NewPostgreSQLRepository(db *pgxpool.Pool) (*PostgreSQLRepository, error) {
	if db == nil {
		return nil, ErrDatabaseRequired
	}
	return &PostgreSQLRepository{db: db}, nil
}

// Post 校验并原子写入交易。相同幂等请求返回首次创建的交易。
func (repository *PostgreSQLRepository) Post(ctx context.Context, transaction Transaction) (result PostResult, err error) {
	databaseTransaction, err := repository.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return PostResult{}, fmt.Errorf("开始账务事务: %w", err)
	}
	defer func() {
		rollbackErr := databaseTransaction.Rollback(context.Background())
		if rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			err = errors.Join(err, fmt.Errorf("回滚账务事务: %w", rollbackErr))
		}
	}()
	result, err = PostInTransaction(ctx, databaseTransaction, transaction)
	if err != nil {
		return PostResult{}, err
	}
	if err = databaseTransaction.Commit(ctx); err != nil {
		return PostResult{}, fmt.Errorf("提交账务事务: %w", err)
	}
	return result, nil
}

// PostInTransaction 在调用方事务中校验并原子写入账务交易和分录，但不提交事务。
func PostInTransaction(ctx context.Context, databaseTransaction pgx.Tx, transaction Transaction) (PostResult, error) {
	if databaseTransaction == nil {
		return PostResult{}, ErrTransactionRequired
	}
	if err := ValidateTransaction(transaction); err != nil {
		return PostResult{}, err
	}

	requestHash, err := requestFingerprint(transaction)
	if err != nil {
		return PostResult{}, err
	}

	var transactionID string
	err = databaseTransaction.QueryRow(ctx, `
		INSERT INTO journal_transactions (
			id, requester_type, requester_id, idempotency_key,
			reference_type, reference_id, request_hash, status
		) VALUES ($1, $2, $3, $4, $5, $6, $7, 'draft')
		ON CONFLICT DO NOTHING
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
	if errors.Is(err, pgx.ErrNoRows) {
		return existingPostResult(ctx, databaseTransaction, transaction, requestHash)
	}
	if err != nil {
		return PostResult{}, fmt.Errorf("创建账务交易: %w", err)
	}

	for index, entry := range transaction.Entries {
		_, err = databaseTransaction.Exec(ctx, `
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

	commandTag, err := databaseTransaction.Exec(ctx, `
		UPDATE journal_transactions
		SET status = 'posted', posted_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND status = 'draft'
	`, transactionID)
	if err != nil {
		return PostResult{}, fmt.Errorf("过账账务交易: %w", err)
	}
	if commandTag.RowsAffected() != 1 {
		return PostResult{}, errors.New("账务交易状态已变化")
	}

	return PostResult{TransactionID: transactionID, Created: true}, nil
}

func existingPostResult(ctx context.Context, databaseTransaction pgx.Tx, transaction Transaction, requestHash string) (PostResult, error) {
	transactionID, existingHash, err := findByIdempotencyKey(ctx, databaseTransaction, transaction)
	if errors.Is(err, pgx.ErrNoRows) {
		transactionID, existingHash, err = findByReference(ctx, databaseTransaction, transaction)
	}
	if err != nil {
		return PostResult{}, fmt.Errorf("查询幂等账务交易: %w", err)
	}
	if existingHash != requestHash {
		return PostResult{}, ErrIdempotencyConflict
	}
	return PostResult{TransactionID: transactionID, Created: false}, nil
}

func findByIdempotencyKey(ctx context.Context, databaseTransaction pgx.Tx, transaction Transaction) (string, string, error) {
	var transactionID string
	var requestHash string
	err := databaseTransaction.QueryRow(ctx, `
		SELECT id, request_hash
		FROM journal_transactions
		WHERE requester_type = $1
		  AND requester_id = $2
		  AND idempotency_key = $3
	`, transaction.RequesterType, transaction.RequesterID, transaction.IdempotencyKey).Scan(&transactionID, &requestHash)
	return transactionID, requestHash, err
}

func findByReference(ctx context.Context, databaseTransaction pgx.Tx, transaction Transaction) (string, string, error) {
	var transactionID string
	var requestHash string
	err := databaseTransaction.QueryRow(ctx, `
		SELECT id, request_hash
		FROM journal_transactions
		WHERE requester_type = $1
		  AND requester_id = $2
		  AND reference_type = $3
		  AND reference_id = $4
	`,
		transaction.RequesterType,
		transaction.RequesterID,
		transaction.ReferenceType,
		transaction.ReferenceID,
	).Scan(&transactionID, &requestHash)
	return transactionID, requestHash, err
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
