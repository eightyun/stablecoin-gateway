//go:build integration

package outbox

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/database"
	"github.com/eightyun/stablecoin-gateway/internal/identity"
)

func TestPostgreSQLStoreClaimsOnlyRequestedTopics(t *testing.T) {
	databaseURL := os.Getenv("GATEWAY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("未设置 GATEWAY_TEST_DATABASE_URL")
	}
	ctx := context.Background()
	pool, err := database.Open(ctx, database.DefaultConfig(databaseURL, "outbox-integration-test"))
	if err != nil {
		t.Fatalf("连接测试数据库: %v", err)
	}
	t.Cleanup(pool.Close)
	depositID, err := identity.NewUUID()
	if err != nil {
		t.Fatalf("生成充值事件 ID: %v", err)
	}
	internalID, err := identity.NewUUID()
	if err != nil {
		t.Fatalf("生成内部事件 ID: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO outbox_events (id, topic, aggregate_type, aggregate_id, payload, status)
		VALUES
			($1, 'deposit.confirmed', 'deposit', 'deposit-1', '{}', 'pending'),
			($2, 'internal.audit', 'audit', 'audit-1', '{}', 'pending')
	`, depositID, internalID); err != nil {
		t.Fatalf("创建测试 Outbox 事件: %v", err)
	}
	store, err := NewPostgreSQLStore(pool)
	if err != nil {
		t.Fatalf("NewPostgreSQLStore() error = %v", err)
	}
	events, err := store.Claim(ctx, "worker-1", []string{"deposit.confirmed"}, 10, time.Minute)
	if err != nil || len(events) != 1 || events[0].ID != depositID {
		t.Fatalf("Claim() = %+v, %v", events, err)
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM outbox_events WHERE id = $1`, internalID).Scan(&status); err != nil || status != "pending" {
		t.Fatalf("未订阅事件状态=%q error=%v", status, err)
	}
}
