//go:build integration

package indexer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/chain/tron"
	"github.com/eightyun/stablecoin-gateway/internal/database"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCursorStoreInitializeAndAdvance(t *testing.T) {
	store, pool, network := newCursorFixture(t)
	ctx := context.Background()
	if err := store.Ensure(ctx, network, 1, "genesis"); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	if err := store.Ensure(ctx, network, 1, "genesis"); err != nil {
		t.Fatalf("重复 Ensure() error = %v", err)
	}
	if err := store.Ensure(ctx, network, 2, "other"); !errors.Is(err, ErrAnchorConflict) {
		t.Fatalf("不同锚点 Ensure() error = %v", err)
	}

	first, err := store.Claim(ctx, network, "worker-a", time.Minute)
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if first.NextHeight != 1 || first.PreviousHash != "genesis" || first.LeaseEpoch != 1 {
		t.Fatalf("首次租约 = %+v", first)
	}
	if _, err := store.Claim(ctx, network, "worker-b", time.Minute); !errors.Is(err, ErrLeaseUnavailable) {
		t.Fatalf("重复领取 error = %v", err)
	}
	if err := store.Release(ctx, first); err != nil {
		t.Fatalf("Release() error = %v", err)
	}

	second, err := store.Claim(ctx, network, "worker-b", time.Minute)
	if err != nil {
		t.Fatalf("再次 Claim() error = %v", err)
	}
	if second.LeaseEpoch != first.LeaseEpoch+1 {
		t.Fatalf("租约代数未递增: first=%d second=%d", first.LeaseEpoch, second.LeaseEpoch)
	}
	databaseTransaction, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("开始事务: %v", err)
	}
	defer databaseTransaction.Rollback(ctx)
	if err := store.AdvanceTx(ctx, databaseTransaction, second, tron.Header{
		Height: 1, Hash: "block-1", ParentHash: "genesis",
	}); err != nil {
		t.Fatalf("AdvanceTx() error = %v", err)
	}
	if err := databaseTransaction.Commit(ctx); err != nil {
		t.Fatalf("提交游标事务: %v", err)
	}

	next, err := store.Claim(ctx, network, "worker-c", time.Minute)
	if err != nil {
		t.Fatalf("推进后 Claim() error = %v", err)
	}
	if next.NextHeight != 2 || next.PreviousHash != "block-1" {
		t.Fatalf("推进后的游标 = %+v", next)
	}
}

func TestCursorStoreExpiredLeaseCannotAdvance(t *testing.T) {
	store, pool, network := newCursorFixture(t)
	ctx := context.Background()
	if err := store.Ensure(ctx, network, 1, "genesis"); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	oldClaim, err := store.Claim(ctx, network, "worker-a", time.Minute)
	if err != nil {
		t.Fatalf("首次 Claim() error = %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE chain_scan_cursors
		SET lease_until = CURRENT_TIMESTAMP - INTERVAL '1 second'
		WHERE network = $1
	`, network); err != nil {
		t.Fatalf("模拟过期租约: %v", err)
	}
	newClaim, err := store.Claim(ctx, network, "worker-b", time.Minute)
	if err != nil {
		t.Fatalf("过期接管 Claim() error = %v", err)
	}
	if newClaim.LeaseEpoch <= oldClaim.LeaseEpoch {
		t.Fatalf("接管未更新栅栏令牌: old=%+v new=%+v", oldClaim, newClaim)
	}

	databaseTransaction, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("开始事务: %v", err)
	}
	defer databaseTransaction.Rollback(ctx)
	block := tron.Header{Height: 1, Hash: "block-1", ParentHash: "genesis"}
	if err := store.AdvanceTx(ctx, databaseTransaction, oldClaim, block); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("旧持有者 AdvanceTx() error = %v", err)
	}
	if err := store.Release(ctx, oldClaim); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("旧持有者 Release() error = %v", err)
	}
	if err := store.AdvanceTx(ctx, databaseTransaction, newClaim, block); err != nil {
		t.Fatalf("新持有者 AdvanceTx() error = %v", err)
	}
	if err := databaseTransaction.Commit(ctx); err != nil {
		t.Fatalf("提交事务: %v", err)
	}
}

func TestCursorStoreWrongHashAndRollback(t *testing.T) {
	store, pool, network := newCursorFixture(t)
	ctx := context.Background()
	if err := store.Ensure(ctx, network, 1, "genesis"); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	claim, err := store.Claim(ctx, network, "worker-a", time.Minute)
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	databaseTransaction, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("开始事务: %v", err)
	}
	if err := store.AdvanceTx(ctx, databaseTransaction, claim, tron.Header{
		Height: 1, Hash: "block-1", ParentHash: "wrong-parent",
	}); !errors.Is(err, ErrInvalidBlock) {
		t.Fatalf("错误父哈希 AdvanceTx() error = %v", err)
	}
	if err := store.AdvanceTx(ctx, databaseTransaction, claim, tron.Header{
		Height: 1, Hash: "block-1", ParentHash: "genesis",
	}); err != nil {
		t.Fatalf("正确区块 AdvanceTx() error = %v", err)
	}
	if err := databaseTransaction.Rollback(ctx); err != nil {
		t.Fatalf("回滚游标事务: %v", err)
	}
	if _, err := store.Claim(ctx, network, "worker-b", time.Minute); !errors.Is(err, ErrLeaseUnavailable) {
		t.Fatalf("回滚后租约不应丢失: %v", err)
	}
	if err := store.Release(ctx, claim); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	next, err := store.Claim(ctx, network, "worker-b", time.Minute)
	if err != nil {
		t.Fatalf("重新 Claim() error = %v", err)
	}
	if next.NextHeight != 1 || next.PreviousHash != "genesis" {
		t.Fatalf("回滚后游标意外前进: %+v", next)
	}
}

func TestCursorStoreRejectsLeaseExpiredAfterTransactionStarted(t *testing.T) {
	store, pool, network := newCursorFixture(t)
	ctx := context.Background()
	if err := store.Ensure(ctx, network, 1, "genesis"); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	claim, err := store.Claim(ctx, network, "worker-a", time.Minute)
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	databaseTransaction, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("开始事务: %v", err)
	}
	defer databaseTransaction.Rollback(ctx)
	var transactionStartedAt time.Time
	if err := databaseTransaction.QueryRow(ctx, `SELECT CURRENT_TIMESTAMP`).Scan(&transactionStartedAt); err != nil {
		t.Fatalf("读取事务开始时间: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE chain_scan_cursors SET lease_until = $2 WHERE network = $1
	`, network, transactionStartedAt.Add(50*time.Millisecond)); err != nil {
		t.Fatalf("模拟事务开始后租约过期: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := store.AdvanceTx(ctx, databaseTransaction, claim, tron.Header{
		Height: 1, Hash: "block-1", ParentHash: "genesis",
	}); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("过期租约 AdvanceTx() error = %v", err)
	}
}

func TestCursorStoreConcurrentClaim(t *testing.T) {
	store, _, network := newCursorFixture(t)
	ctx := context.Background()
	if err := store.Ensure(ctx, network, 1, "genesis"); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}

	const workers = 8
	var waitGroup sync.WaitGroup
	results := make(chan Claim, workers)
	errorsChannel := make(chan error, workers)
	for index := 0; index < workers; index++ {
		waitGroup.Add(1)
		go func(worker int) {
			defer waitGroup.Done()
			claim, err := store.Claim(ctx, network, "worker-"+string(rune('a'+worker)), time.Minute)
			if err != nil {
				errorsChannel <- err
				return
			}
			results <- claim
		}(index)
	}
	waitGroup.Wait()
	close(results)
	close(errorsChannel)

	if len(results) != 1 {
		t.Fatalf("并发领取成功次数 = %d, 期望 1", len(results))
	}
	for err := range errorsChannel {
		if !errors.Is(err, ErrLeaseUnavailable) {
			t.Errorf("并发领取错误 = %v", err)
		}
	}
}

func newCursorFixture(t *testing.T) (*CursorStore, *pgxpool.Pool, string) {
	t.Helper()
	databaseURL := os.Getenv("GATEWAY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("未设置 GATEWAY_TEST_DATABASE_URL")
	}
	pool, err := database.Open(context.Background(), database.DefaultConfig(databaseURL, "cursor-integration-test"))
	if err != nil {
		t.Fatalf("连接测试数据库: %v", err)
	}
	t.Cleanup(pool.Close)
	store, err := NewCursorStore(pool)
	if err != nil {
		t.Fatalf("NewCursorStore() error = %v", err)
	}
	identifier := make([]byte, 8)
	if _, err := rand.Read(identifier); err != nil {
		t.Fatalf("生成测试网络标识: %v", err)
	}
	return store, pool, "tron-integration-" + hex.EncodeToString(identifier)
}
