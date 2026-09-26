//go:build integration

package indexer

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/chain/tron"
	"github.com/eightyun/stablecoin-gateway/internal/chain/tron/simulator"
)

func TestScannerPersistsOnlyFinalizedBlocks(t *testing.T) {
	store, pool, network := newCursorFixture(t)
	ctx := context.Background()
	chain := newScannerChain(t, network)
	first := scannerBlock(network, 1, "genesis", "block-1", "tx-1", "100")
	if err := chain.AppendBlock(first); err != nil {
		t.Fatalf("追加区块: %v", err)
	}
	if err := store.Ensure(ctx, network, 1, "genesis"); err != nil {
		t.Fatalf("建立游标: %v", err)
	}
	scanner := newTestScanner(t, store, chain, network, "USDT")
	result, err := scanner.Step(ctx)
	if err != nil || result.Processed {
		t.Fatalf("未固化扫描 = %+v, %v", result, err)
	}
	if err := chain.Solidify(1); err != nil {
		t.Fatalf("固化区块: %v", err)
	}
	result, err = scanner.Step(ctx)
	if err != nil || !result.Processed || result.Height != 1 || result.EventCount != 1 {
		t.Fatalf("已固化扫描 = %+v, %v", result, err)
	}
	result, err = scanner.Step(ctx)
	if err != nil || result.Processed {
		t.Fatalf("重复扫描 = %+v, %v", result, err)
	}
	var count, nextHeight int64
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM chain_events WHERE network = $1`, network).Scan(&count); err != nil {
		t.Fatalf("读取事件数: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT next_height FROM chain_scan_cursors WHERE network = $1`, network).Scan(&nextHeight); err != nil {
		t.Fatalf("读取游标: %v", err)
	}
	if count != 1 || nextHeight != 2 {
		t.Fatalf("事件数=%d 游标=%d", count, nextHeight)
	}
}

func TestScannerAdvancesEmptyFinalizedBlock(t *testing.T) {
	store, pool, network := newCursorFixture(t)
	ctx := context.Background()
	chain := newScannerChain(t, network)
	if err := chain.AppendBlock(tron.Block{Header: tron.Header{Height: 1, Hash: "block-1", ParentHash: "genesis", Timestamp: time.Unix(1, 0).UTC()}}); err != nil {
		t.Fatalf("追加空区块: %v", err)
	}
	if err := chain.Solidify(1); err != nil {
		t.Fatalf("固化空区块: %v", err)
	}
	if err := store.Ensure(ctx, network, 1, "genesis"); err != nil {
		t.Fatalf("建立游标: %v", err)
	}
	result, err := newTestScanner(t, store, chain, network, "USDT").Step(ctx)
	if err != nil || !result.Processed || result.EventCount != 0 {
		t.Fatalf("空区块扫描 = %+v, %v", result, err)
	}
	var nextHeight int64
	if err := pool.QueryRow(ctx, `SELECT next_height FROM chain_scan_cursors WHERE network = $1`, network).Scan(&nextHeight); err != nil {
		t.Fatalf("读取游标: %v", err)
	}
	if nextHeight != 2 {
		t.Fatalf("空区块后游标=%d", nextHeight)
	}
}

func TestScannerEventConflictRollsBackWholeBlock(t *testing.T) {
	store, pool, network := newCursorFixture(t)
	ctx := context.Background()
	chain := newScannerChain(t, network)
	block := scannerBlock(network, 1, "genesis", "block-1", "tx-1", "100")
	block.Receipts = append(block.Receipts, tron.Receipt{TransactionID: "tx-2", Outcome: tron.ExecutionSucceeded})
	block.Transfers = append(block.Transfers, tron.Transfer{
		ID:   tron.EventID{Network: network, Contract: "USDT", TransactionID: "tx-2", LogIndex: 1},
		From: "buyer-2", To: "deposit", Amount: "200",
	})
	if err := chain.AppendBlock(block); err != nil {
		t.Fatalf("追加区块: %v", err)
	}
	if err := chain.Solidify(1); err != nil {
		t.Fatalf("固化区块: %v", err)
	}
	if err := store.Ensure(ctx, network, 1, "genesis"); err != nil {
		t.Fatalf("建立游标: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO chain_events (network, contract, transaction_id, log_index, block_height, block_hash, block_time, from_address, to_address, amount)
		VALUES ($1, 'USDT', 'tx-2', 1, 1, 'block-1', to_timestamp(1), 'buyer-2', 'deposit', 999)
	`, network); err != nil {
		t.Fatalf("设置冲突历史事件: %v", err)
	}
	scanner := newTestScanner(t, store, chain, network, "USDT")
	if _, err := scanner.Step(ctx); !errors.Is(err, ErrEventConflict) {
		t.Fatalf("冲突扫描 error = %v", err)
	}
	var count, nextHeight int64
	var leaseOwner *string
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM chain_events WHERE network = $1`, network).Scan(&count); err != nil {
		t.Fatalf("读取事件数: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT next_height, lease_owner FROM chain_scan_cursors WHERE network = $1`, network).Scan(&nextHeight, &leaseOwner); err != nil {
		t.Fatalf("读取游标: %v", err)
	}
	if count != 1 || nextHeight != 1 || leaseOwner != nil {
		t.Fatalf("回滚后事件数=%d 游标=%d 租约=%v", count, nextHeight, leaseOwner)
	}
}

func TestScannerIdenticalEventCanBeReplayed(t *testing.T) {
	store, pool, network := newCursorFixture(t)
	ctx := context.Background()
	chain := newScannerChain(t, network)
	if err := chain.AppendBlock(scannerBlock(network, 1, "genesis", "block-1", "tx-1", "100")); err != nil {
		t.Fatalf("追加区块: %v", err)
	}
	if err := chain.Solidify(1); err != nil {
		t.Fatalf("固化区块: %v", err)
	}
	if err := store.Ensure(ctx, network, 1, "genesis"); err != nil {
		t.Fatalf("建立游标: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO chain_events (network, contract, transaction_id, log_index, block_height, block_hash, block_time, from_address, to_address, amount)
		VALUES ($1, 'USDT', 'tx-1', 0, 1, 'block-1', to_timestamp(1), 'buyer', 'deposit', 100)
	`, network); err != nil {
		t.Fatalf("设置已有事件: %v", err)
	}
	result, err := newTestScanner(t, store, chain, network, "USDT").Step(ctx)
	if err != nil || !result.Processed {
		t.Fatalf("重放相同事件 = %+v, %v", result, err)
	}
	var count int64
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM chain_events WHERE network = $1`, network).Scan(&count); err != nil {
		t.Fatalf("读取事件数: %v", err)
	}
	if count != 1 {
		t.Fatalf("重复事件数=%d", count)
	}
}

func TestScannerReadFailureCanRetry(t *testing.T) {
	store, pool, network := newCursorFixture(t)
	ctx := context.Background()
	chain := newScannerChain(t, network)
	if err := chain.AppendBlock(scannerBlock(network, 1, "genesis", "block-1", "tx-1", "100")); err != nil {
		t.Fatalf("追加区块: %v", err)
	}
	if err := chain.Solidify(1); err != nil {
		t.Fatalf("固化区块: %v", err)
	}
	if err := store.Ensure(ctx, network, 1, "genesis"); err != nil {
		t.Fatalf("建立游标: %v", err)
	}
	scanner := newTestScanner(t, store, chain, network, "USDT")
	if err := chain.FailNext(simulator.FailSolidifiedBlockByHeight, context.DeadlineExceeded); err != nil {
		t.Fatalf("注入读取故障: %v", err)
	}
	if _, err := scanner.Step(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("读取故障 error = %v", err)
	}
	var nextHeight int64
	if err := pool.QueryRow(ctx, `SELECT next_height FROM chain_scan_cursors WHERE network = $1`, network).Scan(&nextHeight); err != nil {
		t.Fatalf("读取游标: %v", err)
	}
	if nextHeight != 1 {
		t.Fatalf("故障后游标=%d", nextHeight)
	}
	if result, err := scanner.Step(ctx); err != nil || !result.Processed {
		t.Fatalf("重试扫描 = %+v, %v", result, err)
	}
}

func TestScannerRejectsContractDrift(t *testing.T) {
	store, _, network := newCursorFixture(t)
	ctx := context.Background()
	chain := newScannerChain(t, network)
	if err := store.Ensure(ctx, network, 1, "genesis"); err != nil {
		t.Fatalf("建立游标: %v", err)
	}
	if _, err := newTestScanner(t, store, chain, network, "USDT").Step(ctx); err != nil {
		t.Fatalf("首次绑定合约: %v", err)
	}
	if _, err := newTestScanner(t, store, chain, network, "OTHER").Step(ctx); !errors.Is(err, ErrContractConflict) {
		t.Fatalf("合约漂移 error = %v", err)
	}
}

func TestScannerRejectsAdvancedUnboundCursor(t *testing.T) {
	store, pool, network := newCursorFixture(t)
	ctx := context.Background()
	chain := newScannerChain(t, network)
	if err := store.Ensure(ctx, network, 1, "genesis"); err != nil {
		t.Fatalf("建立游标: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE chain_scan_cursors SET next_height = 2, previous_hash = 'old-block'
		WHERE network = $1
	`, network); err != nil {
		t.Fatalf("模拟旧版已推进游标: %v", err)
	}
	if _, err := newTestScanner(t, store, chain, network, "USDT").Step(ctx); !errors.Is(err, ErrContractUnbound) {
		t.Fatalf("未绑定旧游标 error = %v", err)
	}
}

func newScannerChain(t *testing.T, network string) *simulator.Simulator {
	t.Helper()
	chain, err := simulator.New(network, "genesis")
	if err != nil {
		t.Fatalf("创建模拟链: %v", err)
	}
	return chain
}

func newTestScanner(t *testing.T, store *CursorStore, chain tron.FinalizedReader, network, contract string) *Scanner {
	t.Helper()
	scanner, err := NewScanner(store, chain, network, contract, "scanner-test", time.Minute)
	if err != nil {
		t.Fatalf("创建扫描器: %v", err)
	}
	return scanner
}

func scannerBlock(network string, height uint64, parent, hash, transactionID, amount string) tron.Block {
	return tron.Block{
		Header:   tron.Header{Height: height, Hash: hash, ParentHash: parent, Timestamp: time.Unix(int64(height), 0).UTC()},
		Receipts: []tron.Receipt{{TransactionID: transactionID, Outcome: tron.ExecutionSucceeded}},
		Transfers: []tron.Transfer{{
			ID:   tron.EventID{Network: network, Contract: "USDT", TransactionID: transactionID},
			From: "buyer", To: "deposit", Amount: amount,
		}},
	}
}
