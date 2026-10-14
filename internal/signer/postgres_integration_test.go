//go:build integration

package signer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/chain/tron"
	"github.com/eightyun/stablecoin-gateway/internal/database"
	"github.com/eightyun/stablecoin-gateway/internal/identity"
)

func TestPostgresStorePersistsAndSerializesIdempotentResult(t *testing.T) {
	databaseURL := os.Getenv("GATEWAY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("未设置 GATEWAY_TEST_DATABASE_URL")
	}
	ctx := context.Background()
	pool, err := database.Open(ctx, database.DefaultConfig(databaseURL, "signer-store-integration-test"))
	if err != nil {
		t.Fatalf("连接测试数据库: %v", err)
	}
	t.Cleanup(pool.Close)
	store, err := NewPostgresStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	requestID, err := identity.NewUUID()
	if err != nil {
		t.Fatal(err)
	}
	requestHash := hex.EncodeToString(make([]byte, 32))
	transaction := validStoredTransaction(t)
	var calls atomic.Int32
	create := func() (tron.SignedTransaction, error) {
		calls.Add(1)
		time.Sleep(25 * time.Millisecond)
		return transaction, nil
	}
	results := make([]tron.SignedTransaction, 2)
	errorsFound := make([]error, 2)
	var wait sync.WaitGroup
	for index := range results {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			results[index], errorsFound[index] = store.Resolve(ctx, requestID, requestHash, create)
		}(index)
	}
	wait.Wait()
	if errorsFound[0] != nil || errorsFound[1] != nil || calls.Load() != 1 {
		t.Fatalf("Resolve errors=%v calls=%d", errorsFound, calls.Load())
	}
	if results[0].ID != transaction.ID || string(results[1].Payload) != string(transaction.Payload) {
		t.Fatalf("Resolve results=%+v", results)
	}
	conflictHash := hex.EncodeToString([]byte("11111111111111111111111111111111"))
	if _, err := store.Resolve(ctx, requestID, conflictHash, create); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("冲突 Resolve() error = %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM signer_requests WHERE request_id = $1`, requestID); err == nil {
		t.Fatal("signer 幂等记录应不可删除")
	}
}

func validStoredTransaction(t *testing.T) tron.SignedTransaction {
	t.Helper()
	nonce, err := identity.NewUUID()
	if err != nil {
		t.Fatal(err)
	}
	rawData := []byte("postgres-signer-store-test:" + nonce)
	digest := sha256.Sum256(rawData)
	transactionID := hex.EncodeToString(digest[:])
	now := time.Now().UTC().Truncate(time.Millisecond)
	payload, err := json.Marshal(map[string]any{
		"txID": transactionID,
		"raw_data": map[string]any{
			"contract":  []any{map[string]any{"type": "TriggerSmartContract"}},
			"timestamp": now.UnixMilli(), "expiration": now.Add(time.Minute).UnixMilli(),
		},
		"raw_data_hex": hex.EncodeToString(rawData),
		"signature":    []string{hex.EncodeToString(make([]byte, 65))},
	})
	if err != nil {
		t.Fatal(err)
	}
	return tron.SignedTransaction{ID: transactionID, Payload: payload}
}
