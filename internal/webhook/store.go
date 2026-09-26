package webhook

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/eightyun/stablecoin-gateway/internal/secretbox"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store 使用 PostgreSQL 保存端点与投递审计。
type Store struct {
	db *pgxpool.Pool
}

// Endpoint 是一次投递所需的活动端点数据。
type Endpoint struct {
	ID        string
	URL       string
	Encrypted secretbox.Encrypted
}

// Attempt 是一次 Webhook HTTP 请求的审计结果。
type Attempt struct {
	EventID          string
	EndpointID       string
	Attempt          int
	RequestTimestamp int64
	ResponseStatus   *int
	Error            string
	Duration         time.Duration
}

// DeliveryStore 定义 Handler 所需的持久化操作。
type DeliveryStore interface {
	PendingEndpoints(ctx context.Context, eventID, merchantID string) ([]Endpoint, error)
	RecordAttempt(ctx context.Context, attempt Attempt) error
}

// NewStore 创建 Webhook Store。
func NewStore(db *pgxpool.Pool) (*Store, error) {
	if db == nil {
		return nil, ErrDatabaseRequired
	}
	return &Store{db: db}, nil
}

// PendingEndpoints 返回尚未成功接收指定事件的活动端点。
func (store *Store) PendingEndpoints(ctx context.Context, eventID, merchantID string) ([]Endpoint, error) {
	rows, err := store.db.Query(ctx, `
		SELECT endpoint.id::TEXT, endpoint.url, endpoint.secret_ciphertext,
		       endpoint.secret_nonce, endpoint.encryption_key_version
		FROM merchant_webhook_endpoints AS endpoint
		WHERE endpoint.merchant_id = $2
		  AND endpoint.status = 'active'
		  AND NOT EXISTS (
		      SELECT 1
		      FROM webhook_delivery_attempts AS attempt
		      WHERE attempt.outbox_event_id = $1
		        AND attempt.endpoint_id = endpoint.id
		        AND attempt.response_status BETWEEN 200 AND 299
		  )
		ORDER BY endpoint.created_at, endpoint.id
	`, eventID, merchantID)
	if err != nil {
		return nil, fmt.Errorf("查询待投递 Webhook 端点: %w", err)
	}
	defer rows.Close()
	endpoints := make([]Endpoint, 0)
	for rows.Next() {
		var endpoint Endpoint
		if err := rows.Scan(
			&endpoint.ID, &endpoint.URL, &endpoint.Encrypted.Ciphertext,
			&endpoint.Encrypted.Nonce, &endpoint.Encrypted.Version,
		); err != nil {
			return nil, fmt.Errorf("读取 Webhook 端点: %w", err)
		}
		endpoints = append(endpoints, endpoint)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历 Webhook 端点: %w", err)
	}
	return endpoints, nil
}

// RecordAttempt 写入一次不可变投递审计。
func (store *Store) RecordAttempt(ctx context.Context, attempt Attempt) error {
	errorText := truncateUTF8(strings.TrimSpace(attempt.Error), 2048)
	var nullableError any
	if errorText != "" {
		nullableError = errorText
	}
	var responseStatus any
	if attempt.ResponseStatus != nil {
		responseStatus = *attempt.ResponseStatus
	}
	result, err := store.db.Exec(ctx, `
		INSERT INTO webhook_delivery_attempts (
			outbox_event_id, endpoint_id, attempt, request_timestamp,
			response_status, error, duration_ms
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, attempt.EventID, attempt.EndpointID, attempt.Attempt, attempt.RequestTimestamp,
		responseStatus, nullableError, max(attempt.Duration.Milliseconds(), 0))
	if err != nil {
		return fmt.Errorf("记录 Webhook 投递审计: %w", err)
	}
	if result.RowsAffected() != 1 {
		return errors.New("Webhook 投递审计写入数量异常")
	}
	return nil
}

func truncateUTF8(value string, maximumBytes int) string {
	value = strings.ToValidUTF8(value, "�")
	for len(value) > maximumBytes {
		_, size := utf8.DecodeLastRuneInString(value)
		value = value[:len(value)-size]
	}
	return value
}
