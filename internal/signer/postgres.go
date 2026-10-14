package signer

import (
	"context"
	"errors"
	"fmt"

	"github.com/eightyun/stablecoin-gateway/internal/chain/tron"
	"github.com/eightyun/stablecoin-gateway/internal/identity"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresStore 为多实例生产 signer 提供跨进程幂等存储。
type PostgresStore struct {
	db *pgxpool.Pool
}

// NewPostgresStore 创建生产 signer 幂等存储。
func NewPostgresStore(db *pgxpool.Pool) (*PostgresStore, error) {
	if db == nil {
		return nil, ErrInvalidStore
	}
	return &PostgresStore{db: db}, nil
}

// Resolve 在数据库事务锁保护下返回已有结果或创建并持久化首次结果。
func (store *PostgresStore) Resolve(
	ctx context.Context,
	requestID string,
	requestHash string,
	create func() (tron.SignedTransaction, error),
) (tron.SignedTransaction, error) {
	if store == nil || store.db == nil || !identity.ValidUUID(requestID) || !validRequestHash(requestHash) || create == nil {
		return tron.SignedTransaction{}, ErrInvalidStore
	}
	tx, err := store.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return tron.SignedTransaction{}, fmt.Errorf("开始 signer 幂等事务: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, requestID); err != nil {
		return tron.SignedTransaction{}, fmt.Errorf("锁定 signer 请求: %w", err)
	}
	transaction, exists, err := loadPostgresRecord(ctx, tx, requestID, requestHash)
	if err != nil || exists {
		return transaction, err
	}
	transaction, err = create()
	if err != nil {
		return tron.SignedTransaction{}, err
	}
	if err := validateStoredTransaction(transaction); err != nil {
		return tron.SignedTransaction{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO signer_requests (
			request_id, request_hash, transaction_id, signed_transaction
		) VALUES ($1, $2, $3, $4)
	`, requestID, requestHash, transaction.ID, transaction.Payload); err != nil {
		return tron.SignedTransaction{}, fmt.Errorf("保存 signer 幂等结果: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return tron.SignedTransaction{}, fmt.Errorf("提交 signer 幂等结果: %w", err)
	}
	return transaction, nil
}

func loadPostgresRecord(
	ctx context.Context,
	tx pgx.Tx,
	requestID string,
	requestHash string,
) (tron.SignedTransaction, bool, error) {
	var storedHash string
	var transaction tron.SignedTransaction
	err := tx.QueryRow(ctx, `
		SELECT request_hash, transaction_id, signed_transaction
		FROM signer_requests
		WHERE request_id = $1
	`, requestID).Scan(&storedHash, &transaction.ID, &transaction.Payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return tron.SignedTransaction{}, false, nil
	}
	if err != nil {
		return tron.SignedTransaction{}, false, fmt.Errorf("读取 signer 幂等结果: %w", err)
	}
	if storedHash != requestHash {
		return tron.SignedTransaction{}, false, ErrIdempotencyConflict
	}
	if err := validateStoredTransaction(transaction); err != nil {
		return tron.SignedTransaction{}, false, err
	}
	return transaction, true, nil
}

func validRequestHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return false
			}
		}
	}
	return true
}

func validateStoredTransaction(transaction tron.SignedTransaction) error {
	if len(transaction.Payload) > maxRecordBytes {
		return ErrInvalidStore
	}
	if err := tron.ValidateSignedTransaction(transaction); err != nil {
		return ErrInvalidStore
	}
	return nil
}
