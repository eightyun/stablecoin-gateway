package outbox

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgreSQLStore 使用 PostgreSQL 管理 Outbox 租约和状态。
type PostgreSQLStore struct {
	db *pgxpool.Pool
}

// NewPostgreSQLStore 创建 PostgreSQL Outbox Store。
func NewPostgreSQLStore(db *pgxpool.Pool) (*PostgreSQLStore, error) {
	if db == nil {
		return nil, ErrStoreRequired
	}
	return &PostgreSQLStore{db: db}, nil
}

// Claim 使用行锁跳过其他 Worker 已领取的任务，并接管过期租约。
func (store *PostgreSQLStore) Claim(ctx context.Context, workerID string, limit int, leaseDuration time.Duration) ([]Event, error) {
	rows, err := store.db.Query(ctx, `
		WITH candidates AS (
			SELECT id
			FROM outbox_events
			WHERE (status = 'pending' AND available_at <= CURRENT_TIMESTAMP)
			   OR (status = 'processing' AND lease_until <= CURRENT_TIMESTAMP)
			ORDER BY available_at, created_at, id
			FOR UPDATE SKIP LOCKED
			LIMIT $1
		)
		UPDATE outbox_events AS event
		SET status = 'processing',
			attempts = event.attempts + 1,
			lease_owner = $2,
			lease_until = CURRENT_TIMESTAMP + ($3 * INTERVAL '1 millisecond'),
			updated_at = CURRENT_TIMESTAMP
		FROM candidates
		WHERE event.id = candidates.id
		RETURNING event.id, event.topic, event.payload, event.attempts, event.created_at
	`, limit, workerID, leaseDuration.Milliseconds())
	if err != nil {
		return nil, fmt.Errorf("领取 Outbox 事件: %w", err)
	}
	defer rows.Close()

	events := make([]Event, 0, limit)
	for rows.Next() {
		var event Event
		if err := rows.Scan(&event.ID, &event.Topic, &event.Payload, &event.Attempts, &event.CreatedAt); err != nil {
			return nil, fmt.Errorf("读取 Outbox 事件: %w", err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历 Outbox 事件: %w", err)
	}
	return events, nil
}

// MarkSucceeded 将当前 Worker 持有的任务标记为成功。
func (store *PostgreSQLStore) MarkSucceeded(ctx context.Context, eventID, workerID string) error {
	return store.updateLeasedEvent(ctx, eventID, workerID, `
		UPDATE outbox_events
		SET status = 'succeeded', lease_owner = NULL, lease_until = NULL,
			last_error = NULL, updated_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND status = 'processing' AND lease_owner = $2
	`)
}

// MarkRetry 释放租约并安排下一次处理时间。
func (store *PostgreSQLStore) MarkRetry(ctx context.Context, eventID, workerID string, retryAfter time.Duration, reason string) error {
	return store.updateLeasedEvent(ctx, eventID, workerID, `
		UPDATE outbox_events
		SET status = 'pending',
			available_at = CURRENT_TIMESTAMP + ($3 * INTERVAL '1 millisecond'),
			lease_owner = NULL,
			lease_until = NULL, last_error = $4, updated_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND status = 'processing' AND lease_owner = $2
	`, retryAfter.Milliseconds(), reason)
}

// MarkDead 将不可继续处理的任务标记为死信。
func (store *PostgreSQLStore) MarkDead(ctx context.Context, eventID, workerID, reason string) error {
	return store.updateLeasedEvent(ctx, eventID, workerID, `
		UPDATE outbox_events
		SET status = 'dead', lease_owner = NULL, lease_until = NULL,
			last_error = $3, updated_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND status = 'processing' AND lease_owner = $2
	`, reason)
}

func (store *PostgreSQLStore) updateLeasedEvent(
	ctx context.Context,
	eventID string,
	workerID string,
	query string,
	arguments ...any,
) error {
	queryArguments := []any{eventID, workerID}
	queryArguments = append(queryArguments, arguments...)
	result, err := store.db.Exec(ctx, query, queryArguments...)
	if err != nil {
		return fmt.Errorf("更新 Outbox 事件: %w", err)
	}

	affectedRows := result.RowsAffected()
	if affectedRows == 0 {
		return ErrLeaseLost
	}
	if affectedRows != 1 {
		return errors.New("Outbox 更新影响了多条记录")
	}
	return nil
}
