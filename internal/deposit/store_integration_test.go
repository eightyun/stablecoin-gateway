//go:build integration

package deposit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/database"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestStoreCreatesIntentAndMatchesExactPayment(t *testing.T) {
	store, pool, fixture := newDepositFixture(t)
	ctx := context.Background()
	address := Address{ID: randomUUID(t), MerchantID: fixture.merchantID, AssetID: fixture.assetID, Address: fixture.address}
	created, err := store.RegisterAddress(ctx, address)
	if err != nil || !created.Created {
		t.Fatalf("RegisterAddress() = %+v, %v", created, err)
	}
	retryAddress := address
	retryAddress.ID = randomUUID(t)
	if result, err := store.RegisterAddress(ctx, retryAddress); err != nil || result.Created || result.ID != address.ID {
		t.Fatalf("重复 RegisterAddress() = %+v, %v", result, err)
	}

	intent := Intent{
		ID: randomUUID(t), MerchantID: fixture.merchantID, AssetID: fixture.assetID,
		DepositAddressID: address.ID, IdempotencyKey: "idem-1", MerchantReference: "order-1",
		ExpectedAmount: "1000000", ExpiresAt: time.Now().Add(time.Hour).UTC(),
	}
	result, err := store.CreateIntent(ctx, intent)
	if err != nil || !result.Created {
		t.Fatalf("CreateIntent() = %+v, %v", result, err)
	}
	if retry, err := store.CreateIntent(ctx, intent); err != nil || retry.Created || retry.ID != intent.ID {
		t.Fatalf("重复 CreateIntent() = %+v, %v", retry, err)
	}
	conflict := intent
	conflict.ExpectedAmount = "2000000"
	if _, err := store.CreateIntent(ctx, conflict); !errors.Is(err, ErrIntentConflict) {
		t.Fatalf("冲突 CreateIntent() error = %v", err)
	}

	insertChainEvent(t, pool, fixture, "tx-exact", 0, 1, time.Now().Add(time.Minute), "1000000")
	matched, err := store.MatchNext(ctx)
	if err != nil || !matched.Processed || matched.MatchStatus != "matched" ||
		matched.IntentStatus != "paid" || matched.ReceivedAmount != "1000000" {
		t.Fatalf("MatchNext() = %+v, %v", matched, err)
	}
	if empty, err := store.MatchNext(ctx); err != nil || empty.Processed {
		t.Fatalf("重复 MatchNext() = %+v, %v", empty, err)
	}
}

func TestStoreAccumulatesPartialAndOverpayment(t *testing.T) {
	store, pool, fixture := newDepositFixture(t)
	ctx := context.Background()
	addressID := randomUUID(t)
	if _, err := store.RegisterAddress(ctx, Address{ID: addressID, MerchantID: fixture.merchantID, AssetID: fixture.assetID, Address: fixture.address}); err != nil {
		t.Fatalf("RegisterAddress() error = %v", err)
	}
	intent := Intent{
		ID: randomUUID(t), MerchantID: fixture.merchantID, AssetID: fixture.assetID,
		DepositAddressID: addressID, IdempotencyKey: "idem-partial", MerchantReference: "order-partial",
		ExpectedAmount: "100", ExpiresAt: time.Now().Add(time.Hour).UTC(),
	}
	if _, err := store.CreateIntent(ctx, intent); err != nil {
		t.Fatalf("CreateIntent() error = %v", err)
	}
	blockTime := time.Now().Add(time.Minute)
	insertChainEvent(t, pool, fixture, "tx-partial-1", 0, 1, blockTime, "40")
	insertChainEvent(t, pool, fixture, "tx-partial-2", 0, 2, blockTime.Add(time.Second), "70")
	first, err := store.MatchNext(ctx)
	if err != nil || first.IntentStatus != "partially_paid" || first.ReceivedAmount != "40" {
		t.Fatalf("首次 MatchNext() = %+v, %v", first, err)
	}
	second, err := store.MatchNext(ctx)
	if err != nil || second.IntentStatus != "overpaid" || second.ReceivedAmount != "110" {
		t.Fatalf("再次 MatchNext() = %+v, %v", second, err)
	}
}

func TestStoreRoutesUnmatchedAndLateEventsToReview(t *testing.T) {
	t.Run("没有意图", func(t *testing.T) {
		store, pool, fixture := newDepositFixture(t)
		ctx := context.Background()
		if _, err := store.RegisterAddress(ctx, Address{ID: randomUUID(t), MerchantID: fixture.merchantID, AssetID: fixture.assetID, Address: fixture.address}); err != nil {
			t.Fatalf("RegisterAddress() error = %v", err)
		}
		insertChainEvent(t, pool, fixture, "tx-no-intent", 0, 1, time.Now(), "10")
		result, err := store.MatchNext(ctx)
		if err != nil || result.MatchStatus != "review" || result.Reason != "no_intent" {
			t.Fatalf("MatchNext() = %+v, %v", result, err)
		}
	})

	t.Run("超过有效期", func(t *testing.T) {
		store, pool, fixture := newDepositFixture(t)
		ctx := context.Background()
		addressID := randomUUID(t)
		if _, err := store.RegisterAddress(ctx, Address{ID: addressID, MerchantID: fixture.merchantID, AssetID: fixture.assetID, Address: fixture.address}); err != nil {
			t.Fatalf("RegisterAddress() error = %v", err)
		}
		expiresAt := time.Now().Add(time.Hour).UTC()
		if _, err := store.CreateIntent(ctx, Intent{
			ID: randomUUID(t), MerchantID: fixture.merchantID, AssetID: fixture.assetID,
			DepositAddressID: addressID, IdempotencyKey: "idem-late", MerchantReference: "order-late",
			ExpectedAmount: "10", ExpiresAt: expiresAt,
		}); err != nil {
			t.Fatalf("CreateIntent() error = %v", err)
		}
		insertChainEvent(t, pool, fixture, "tx-late", 0, 1, expiresAt.Add(time.Second), "10")
		result, err := store.MatchNext(ctx)
		if err != nil || result.MatchStatus != "review" || result.Reason != "after_expiry" {
			t.Fatalf("MatchNext() = %+v, %v", result, err)
		}
	})

	t.Run("资产已停用", func(t *testing.T) {
		store, pool, fixture := newDepositFixture(t)
		ctx := context.Background()
		addressID := randomUUID(t)
		if _, err := store.RegisterAddress(ctx, Address{ID: addressID, MerchantID: fixture.merchantID, AssetID: fixture.assetID, Address: fixture.address}); err != nil {
			t.Fatalf("RegisterAddress() error = %v", err)
		}
		if _, err := store.CreateIntent(ctx, Intent{
			ID: randomUUID(t), MerchantID: fixture.merchantID, AssetID: fixture.assetID,
			DepositAddressID: addressID, IdempotencyKey: "idem-disabled", MerchantReference: "order-disabled",
			ExpectedAmount: "10", ExpiresAt: time.Now().Add(time.Hour),
		}); err != nil {
			t.Fatalf("CreateIntent() error = %v", err)
		}
		if _, err := pool.Exec(ctx, `UPDATE assets SET status = 'disabled' WHERE id = $1`, fixture.assetID); err != nil {
			t.Fatalf("停用资产: %v", err)
		}
		insertChainEvent(t, pool, fixture, "tx-disabled", 0, 1, time.Now().Add(time.Minute), "10")
		result, err := store.MatchNext(ctx)
		if err != nil || result.MatchStatus != "review" || result.Reason != "asset_disabled" {
			t.Fatalf("MatchNext() = %+v, %v", result, err)
		}
	})
}

func TestStoreExpiresDueIntents(t *testing.T) {
	store, pool, fixture := newDepositFixture(t)
	ctx := context.Background()
	addressID := randomUUID(t)
	if _, err := store.RegisterAddress(ctx, Address{ID: addressID, MerchantID: fixture.merchantID, AssetID: fixture.assetID, Address: fixture.address}); err != nil {
		t.Fatalf("RegisterAddress() error = %v", err)
	}
	intentID := randomUUID(t)
	if _, err := store.CreateIntent(ctx, Intent{
		ID: intentID, MerchantID: fixture.merchantID, AssetID: fixture.assetID,
		DepositAddressID: addressID, IdempotencyKey: "idem-expire", MerchantReference: "order-expire",
		ExpectedAmount: "10", ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("CreateIntent() error = %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE deposit_intents SET expires_at = clock_timestamp() - INTERVAL '1 second' WHERE id = $1`, intentID); err != nil {
		t.Fatalf("设置到期时间: %v", err)
	}
	count, err := store.ExpireDue(ctx, 10)
	if err != nil || count != 1 {
		t.Fatalf("ExpireDue() = %d, %v", count, err)
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM deposit_intents WHERE id = $1`, intentID).Scan(&status); err != nil || status != "expired" {
		t.Fatalf("到期状态 = %q, %v", status, err)
	}
}

type depositFixture struct {
	network    string
	contract   string
	assetID    string
	merchantID string
	address    string
}

func newDepositFixture(t *testing.T) (*Store, *pgxpool.Pool, depositFixture) {
	t.Helper()
	databaseURL := os.Getenv("GATEWAY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("未设置 GATEWAY_TEST_DATABASE_URL")
	}
	pool, err := database.Open(context.Background(), database.DefaultConfig(databaseURL, "deposit-integration-test"))
	if err != nil {
		t.Fatalf("连接测试数据库: %v", err)
	}
	t.Cleanup(pool.Close)
	store, err := NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	fixture := depositFixture{
		network: "tron-" + randomHex(t, 6), contract: "41" + randomHex(t, 20),
		assetID: "asset-" + randomHex(t, 6), merchantID: randomUUID(t), address: "41" + randomHex(t, 20),
	}
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO assets (id, network, contract_address, symbol, decimals, status)
		VALUES ($1, $2, $3, 'USDT', 6, 'active')
	`, fixture.assetID, fixture.network, fixture.contract); err != nil {
		t.Fatalf("创建测试资产: %v", err)
	}
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO merchants (id, name, status) VALUES ($1, 'test merchant', 'active')
	`, fixture.merchantID); err != nil {
		t.Fatalf("创建测试商户: %v", err)
	}
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO chain_scan_cursors (network, start_height, anchor_hash, next_height, previous_hash, tracked_contract)
		VALUES ($1, 1, 'genesis', 1, 'genesis', $2)
	`, fixture.network, fixture.contract); err != nil {
		t.Fatalf("创建测试扫描游标: %v", err)
	}
	return store, pool, fixture
}

func insertChainEvent(t *testing.T, pool *pgxpool.Pool, fixture depositFixture, transactionID string, logIndex, height int64, blockTime time.Time, amount string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO chain_events (
			network, contract, transaction_id, log_index, block_height,
			block_hash, block_time, from_address, to_address, amount
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`, fixture.network, fixture.contract, transactionID, logIndex, height,
		"block-"+transactionID, blockTime.UTC(), "41"+randomHex(t, 20), fixture.address, amount); err != nil {
		t.Fatalf("插入链事件: %v", err)
	}
}

func randomUUID(t *testing.T) string {
	t.Helper()
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		t.Fatalf("生成 UUID: %v", err)
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(raw)
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]
}

func randomHex(t *testing.T, size int) string {
	t.Helper()
	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		t.Fatalf("生成随机值: %v", err)
	}
	return hex.EncodeToString(raw)
}
