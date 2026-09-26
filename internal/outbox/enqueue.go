package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

var (
	ErrTransactionRequired = errors.New("数据库事务不能为空")
	ErrInvalidPendingEvent = errors.New("待写入 Outbox 事件无效")
)

// PendingEvent 表示需要与业务状态同事务创建的 Outbox 事件。
type PendingEvent struct {
	ID            string
	Topic         string
	AggregateType string
	AggregateID   string
	Payload       []byte
}

// EnqueueInTransaction 在调用方事务中创建待处理事件，但不提交事务。
func EnqueueInTransaction(ctx context.Context, transaction pgx.Tx, event PendingEvent) error {
	if transaction == nil {
		return ErrTransactionRequired
	}
	if err := validatePendingEvent(event); err != nil {
		return err
	}
	_, err := transaction.Exec(ctx, `
		INSERT INTO outbox_events (id, topic, aggregate_type, aggregate_id, payload, status)
		VALUES ($1, $2, $3, $4, $5::JSONB, 'pending')
	`, event.ID, event.Topic, event.AggregateType, event.AggregateID, string(event.Payload))
	if err != nil {
		return fmt.Errorf("写入 Outbox 事件: %w", err)
	}
	return nil
}

func validatePendingEvent(event PendingEvent) error {
	if strings.TrimSpace(event.ID) == "" || strings.TrimSpace(event.Topic) == "" ||
		strings.TrimSpace(event.AggregateType) == "" || strings.TrimSpace(event.AggregateID) == "" ||
		!json.Valid(event.Payload) {
		return ErrInvalidPendingEvent
	}
	return nil
}
