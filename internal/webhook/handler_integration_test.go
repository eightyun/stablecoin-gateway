//go:build integration

package webhook

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/database"
	"github.com/eightyun/stablecoin-gateway/internal/identity"
	"github.com/eightyun/stablecoin-gateway/internal/outbox"
	"github.com/eightyun/stablecoin-gateway/internal/secretbox"
)

func TestHandlerPersistsSuccessfulHTTPSDeliveryAndSkipsItOnRetry(t *testing.T) {
	databaseURL := os.Getenv("GATEWAY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("未设置 GATEWAY_TEST_DATABASE_URL")
	}
	ctx := context.Background()
	pool, err := database.Open(ctx, database.DefaultConfig(databaseURL, "webhook-integration-test"))
	if err != nil {
		t.Fatalf("连接测试数据库: %v", err)
	}
	t.Cleanup(pool.Close)
	merchantID := integrationUUID(t)
	if _, err := pool.Exec(ctx, `
		INSERT INTO merchants (id, name, status) VALUES ($1, 'webhook test merchant', 'active')
	`, merchantID); err != nil {
		t.Fatalf("创建测试商户: %v", err)
	}
	var requestCount atomic.Int32
	var credentials Credentials
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestCount.Add(1)
		body, readErr := io.ReadAll(request.Body)
		timestamp, parseErr := strconv.ParseInt(request.Header.Get(HeaderEventTimestamp), 10, 64)
		if readErr != nil || parseErr != nil || request.Header.Get(HeaderEventID) == "" ||
			request.Header.Get(HeaderSignature) != sign(timestamp, request.Header.Get(HeaderEventID), body, []byte(credentials.Secret)) {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	keyring, err := secretbox.NewKeyring(map[string][]byte{"v1": bytes.Repeat([]byte{7}, 32)}, "v1")
	if err != nil {
		t.Fatalf("NewKeyring() error = %v", err)
	}
	store, err := NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	credentials, err = store.ProvisionEndpoint(ctx, keyring, merchantID, "integration", server.URL)
	if err != nil {
		t.Fatalf("ProvisionEndpoint() error = %v", err)
	}
	var plaintextMatches int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM merchant_webhook_endpoints
		WHERE id = $1 AND secret_ciphertext = $2
	`, credentials.EndpointID, []byte(credentials.Secret)).Scan(&plaintextMatches); err != nil || plaintextMatches != 0 {
		t.Fatalf("明文 Secret 持久化检查 count=%d error=%v", plaintextMatches, err)
	}
	eventID := integrationUUID(t)
	payload := []byte(`{"merchant_id":"` + merchantID + `","deposit_intent_id":"deposit-1","amount":"100"}`)
	if _, err := pool.Exec(ctx, `
		INSERT INTO outbox_events (id, topic, aggregate_type, aggregate_id, payload, status)
		VALUES ($1, 'deposit.confirmed', 'deposit', 'deposit-1', $2::JSONB, 'pending')
	`, eventID, string(payload)); err != nil {
		t.Fatalf("创建测试 Outbox 事件: %v", err)
	}
	sender := &HTTPSender{client: server.Client(), maxResponseBodyBytes: 1024}
	handler, err := NewHandler(store, keyring, sender)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	outboxStore, err := outbox.NewPostgreSQLStore(pool)
	if err != nil {
		t.Fatalf("NewPostgreSQLStore() error = %v", err)
	}
	processor, err := outbox.NewProcessor(outboxStore, outbox.Config{
		WorkerID: "webhook-integration", BatchSize: 10, LeaseDuration: time.Minute,
		MaxAttempts: 3, BaseBackoff: time.Second, MaxBackoff: time.Minute,
	}, map[string]outbox.Handler{"deposit.confirmed": handler})
	if err != nil {
		t.Fatalf("NewProcessor() error = %v", err)
	}
	count, err := processor.RunOnce(ctx)
	if err != nil || count != 1 {
		t.Fatalf("RunOnce() = %d, %v", count, err)
	}
	if err := handler.Handle(ctx, outbox.Event{
		ID: eventID, Topic: "deposit.confirmed", Payload: payload, Attempts: 2, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("重复 Handle() error = %v", err)
	}
	if requestCount.Load() != 1 {
		t.Fatalf("Webhook 请求次数 = %d", requestCount.Load())
	}
	var status int
	if err := pool.QueryRow(ctx, `
		SELECT response_status FROM webhook_delivery_attempts
		WHERE outbox_event_id = $1 AND endpoint_id = $2
	`, eventID, credentials.EndpointID).Scan(&status); err != nil || status != http.StatusNoContent {
		t.Fatalf("投递审计 status=%d error=%v", status, err)
	}
	var outboxStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM outbox_events WHERE id = $1`, eventID).Scan(&outboxStatus); err != nil || outboxStatus != "succeeded" {
		t.Fatalf("Outbox 状态=%q error=%v", outboxStatus, err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE webhook_delivery_attempts SET duration_ms = duration_ms + 1
		WHERE outbox_event_id = $1 AND endpoint_id = $2
	`, eventID, credentials.EndpointID); err == nil {
		t.Fatal("投递审计允许被修改")
	}
}

func integrationUUID(t *testing.T) string {
	t.Helper()
	value, err := identity.NewUUID()
	if err != nil {
		t.Fatalf("生成 UUID: %v", err)
	}
	return value
}
