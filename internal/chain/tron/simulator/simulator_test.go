package simulator_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/eightyun/stablecoin-gateway/internal/chain/tron"
	"github.com/eightyun/stablecoin-gateway/internal/chain/tron/simulator"
)

const network = "tron-sim"

func TestBlockReadingAndSolidification(t *testing.T) {
	chain := newChain(t)
	first := transferBlock(1, "genesis", "block-1", "tx-1")
	second := transferBlock(2, "block-1", "block-2", "tx-2")
	for _, block := range []tron.Block{first, second} {
		if err := chain.AppendBlock(block); err != nil {
			t.Fatalf("AppendBlock() error = %v", err)
		}
	}
	first.Transfers[0].Amount = "999"
	first.Receipts[0].TransactionID = "changed"

	head, err := chain.Head(context.Background())
	if err != nil || head != second.Header {
		t.Fatalf("Head() = %+v, %v", head, err)
	}
	solidified, err := chain.SolidifiedHead(context.Background())
	if err != nil || solidified.Height != 0 {
		t.Fatalf("SolidifiedHead() = %+v, %v", solidified, err)
	}
	if _, err := chain.SolidifiedBlockByHeight(context.Background(), 1); !errors.Is(err, tron.ErrBlockNotFound) {
		t.Fatalf("未固化区块不应可读: %v", err)
	}

	// 按高度查询可由调用方乱序执行，重复查询必须返回同一事实。
	for _, height := range []uint64{2, 1, 2} {
		block, err := chain.BlockByHeight(context.Background(), height)
		if err != nil || block.Header.Height != height {
			t.Fatalf("BlockByHeight(%d) = %+v, %v", height, block, err)
		}
	}

	readBlock, err := chain.BlockByHeight(context.Background(), 1)
	if err != nil {
		t.Fatalf("BlockByHeight() error = %v", err)
	}
	readBlock.Transfers[0].Amount = "999"
	readBlock.Receipts[0].TransactionID = "changed"
	again, err := chain.BlockByHeight(context.Background(), 1)
	if err != nil || again.Transfers[0].Amount != "1000000" || again.Receipts[0].TransactionID != "tx-1" {
		t.Fatalf("读取结果污染了模拟链: %+v, %v", again, err)
	}

	state, err := chain.Transaction(context.Background(), "tx-1")
	if err != nil || state.Status != tron.TransactionSucceeded || state.Solidified {
		t.Fatalf("未固化交易状态 = %+v, %v", state, err)
	}
	if err := chain.Solidify(1); err != nil {
		t.Fatalf("Solidify() error = %v", err)
	}
	finalizedBlock, err := chain.SolidifiedBlockByHeight(context.Background(), 1)
	if err != nil || finalizedBlock.Header.Hash != "block-1" {
		t.Fatalf("已固化区块读取 = %+v, %v", finalizedBlock, err)
	}
	state, err = chain.Transaction(context.Background(), "tx-1")
	if err != nil || !state.Solidified {
		t.Fatalf("已固化交易状态 = %+v, %v", state, err)
	}
	if err := chain.Solidify(0); !errors.Is(err, simulator.ErrInvalidFinality) {
		t.Fatalf("固化高度倒退 error = %v", err)
	}
}

func TestReorganizationKeepsFinalizedPrefix(t *testing.T) {
	chain := newChain(t)
	if err := chain.AppendBlock(transferBlock(1, "genesis", "old-1", "old-tx")); err != nil {
		t.Fatalf("追加旧区块: %v", err)
	}
	if err := chain.AppendBlock(transferBlock(2, "old-1", "old-2", "old-tx-2")); err != nil {
		t.Fatalf("追加旧区块: %v", err)
	}

	newFirst := transferBlock(1, "genesis", "new-1", "new-tx")
	newSecond := transferBlock(2, "new-1", "new-2", "new-tx-2")
	if err := chain.ReplaceUnfinalized(1, []tron.Block{newFirst, newSecond}); err != nil {
		t.Fatalf("ReplaceUnfinalized() error = %v", err)
	}
	oldState, err := chain.Transaction(context.Background(), "old-tx")
	if err != nil || oldState.Status != tron.TransactionNotFound {
		t.Fatalf("被替换交易状态 = %+v, %v", oldState, err)
	}
	newState, err := chain.Transaction(context.Background(), "new-tx")
	if err != nil || newState.Status != tron.TransactionSucceeded || newState.Solidified {
		t.Fatalf("新交易状态 = %+v, %v", newState, err)
	}

	invalid := transferBlock(2, "wrong-parent", "broken", "other-tx")
	if err := chain.ReplaceUnfinalized(2, []tron.Block{invalid}); !errors.Is(err, simulator.ErrInvalidBlock) {
		t.Fatalf("替换无效区块 error = %v", err)
	}
	head, err := chain.Head(context.Background())
	if err != nil || head.Hash != "new-2" {
		t.Fatalf("失败的替换修改了链头: %+v, %v", head, err)
	}

	if err := chain.Solidify(1); err != nil {
		t.Fatalf("Solidify() error = %v", err)
	}
	if err := chain.ReplaceUnfinalized(1, nil); !errors.Is(err, simulator.ErrFinalizedBlock) {
		t.Fatalf("替换已固化区块 error = %v", err)
	}
}

func TestBroadcastUnknownAndRecovery(t *testing.T) {
	chain := newChain(t)
	transaction := tron.SignedTransaction{ID: "payout-tx", Payload: []byte("signed-transaction")}
	timeout := context.DeadlineExceeded
	if err := chain.FailNext(simulator.FailBroadcastAfterAccept, timeout); err != nil {
		t.Fatalf("FailNext() error = %v", err)
	}
	if err := chain.Broadcast(context.Background(), transaction); !errors.Is(err, timeout) {
		t.Fatalf("Broadcast() error = %v", err)
	}
	state, err := chain.Transaction(context.Background(), transaction.ID)
	if err != nil || state.Status != tron.TransactionPending {
		t.Fatalf("响应丢失后交易状态 = %+v, %v", state, err)
	}
	if err := chain.Broadcast(context.Background(), transaction); err != nil {
		t.Fatalf("原始交易重复广播 error = %v", err)
	}
	if err := chain.Broadcast(context.Background(), tron.SignedTransaction{ID: transaction.ID, Payload: []byte("different")}); !errors.Is(err, simulator.ErrTransactionConflict) {
		t.Fatalf("相同交易 ID 不同内容 error = %v", err)
	}

	block := tron.Block{
		Header:   tron.Header{Height: 1, Hash: "block-1", ParentHash: "genesis"},
		Receipts: []tron.Receipt{{TransactionID: transaction.ID, Outcome: tron.ExecutionFailed}},
	}
	if err := chain.AppendBlock(block); err != nil {
		t.Fatalf("AppendBlock() error = %v", err)
	}
	state, err = chain.Transaction(context.Background(), transaction.ID)
	if err != nil || state.Status != tron.TransactionFailed || state.Solidified {
		t.Fatalf("未固化失败交易状态 = %+v, %v", state, err)
	}
	if err := chain.Solidify(1); err != nil {
		t.Fatalf("Solidify() error = %v", err)
	}
	state, err = chain.Transaction(context.Background(), transaction.ID)
	if err != nil || state.Status != tron.TransactionFailed || !state.Solidified {
		t.Fatalf("固化失败交易状态 = %+v, %v", state, err)
	}
}

func TestRPCFailureIsNotNotFound(t *testing.T) {
	chain := newChain(t)
	timeout := context.DeadlineExceeded
	for _, test := range []struct {
		name  string
		point simulator.FailurePoint
		read  func() error
	}{
		{"Head", simulator.FailHead, func() error { _, err := chain.Head(context.Background()); return err }},
		{"SolidifiedHead", simulator.FailSolidifiedHead, func() error { _, err := chain.SolidifiedHead(context.Background()); return err }},
		{"BlockByHeight", simulator.FailBlockByHeight, func() error { _, err := chain.BlockByHeight(context.Background(), 0); return err }},
		{"SolidifiedBlockByHeight", simulator.FailSolidifiedBlockByHeight, func() error { _, err := chain.SolidifiedBlockByHeight(context.Background(), 0); return err }},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := chain.FailNext(test.point, timeout); err != nil {
				t.Fatalf("FailNext() error = %v", err)
			}
			if err := test.read(); !errors.Is(err, timeout) {
				t.Fatalf("读取故障 error = %v", err)
			}
		})
	}
	if err := chain.FailNext(simulator.FailTransaction, timeout); err != nil {
		t.Fatalf("FailNext() error = %v", err)
	}
	if _, err := chain.Transaction(context.Background(), "unknown"); !errors.Is(err, timeout) {
		t.Fatalf("RPC 超时 error = %v", err)
	}
	state, err := chain.Transaction(context.Background(), "unknown")
	if err != nil || state.Status != tron.TransactionNotFound {
		t.Fatalf("未找到交易状态 = %+v, %v", state, err)
	}
	if _, err := chain.BlockByHeight(context.Background(), 1); !errors.Is(err, tron.ErrBlockNotFound) {
		t.Fatalf("缺失区块 error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := chain.Head(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消的请求 error = %v", err)
	}

	transaction := tron.SignedTransaction{ID: "before-accept", Payload: []byte("signed")}
	if err := chain.FailNext(simulator.FailBroadcastBeforeAccept, timeout); err != nil {
		t.Fatalf("FailNext() error = %v", err)
	}
	if err := chain.Broadcast(context.Background(), transaction); !errors.Is(err, timeout) {
		t.Fatalf("广播前故障 error = %v", err)
	}
	state, err = chain.Transaction(context.Background(), transaction.ID)
	if err != nil || state.Status != tron.TransactionNotFound {
		t.Fatalf("广播前故障后的交易状态 = %+v, %v", state, err)
	}
}

func TestAppendRejectsInvalidReceiptsAndEvents(t *testing.T) {
	tests := []struct {
		name  string
		block tron.Block
	}{
		{
			name: "交易失败却含 Transfer",
			block: func() tron.Block {
				block := transferBlock(1, "genesis", "block-1", "tx-1")
				block.Receipts[0].Outcome = tron.ExecutionFailed
				return block
			}(),
		},
		{
			name: "日志位置重复",
			block: func() tron.Block {
				block := transferBlock(1, "genesis", "block-1", "tx-1")
				block.Transfers = append(block.Transfers, block.Transfers[0])
				return block
			}(),
		},
		{
			name: "金额不是正整数",
			block: func() tron.Block {
				block := transferBlock(1, "genesis", "block-1", "tx-1")
				block.Transfers[0].Amount = "0"
				return block
			}(),
		},
		{
			name: "金额超出 uint256",
			block: func() tron.Block {
				block := transferBlock(1, "genesis", "block-1", "tx-1")
				block.Transfers[0].Amount = "115792089237316195423570985008687907853269984665640564039457584007913129639936"
				return block
			}(),
		},
		{
			name: "网络不匹配",
			block: func() tron.Block {
				block := transferBlock(1, "genesis", "block-1", "tx-1")
				block.Transfers[0].ID.Network = "other"
				return block
			}(),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			chain := newChain(t)
			if err := chain.AppendBlock(test.block); !errors.Is(err, simulator.ErrInvalidBlock) {
				t.Fatalf("AppendBlock() error = %v", err)
			}
		})
	}
}

func TestConcurrentReadsDuringAppend(t *testing.T) {
	chain := newChain(t)
	const blockCount = 20
	var waitGroup sync.WaitGroup
	for reader := 0; reader < 4; reader++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			for attempt := 0; attempt < blockCount; attempt++ {
				if _, err := chain.Head(context.Background()); err != nil {
					t.Errorf("并发 Head() error = %v", err)
				}
				if _, err := chain.Transaction(context.Background(), "missing"); err != nil {
					t.Errorf("并发 Transaction() error = %v", err)
				}
			}
		}()
	}
	parent := "genesis"
	for height := uint64(1); height <= blockCount; height++ {
		hash := fmt.Sprintf("block-%d", height)
		if err := chain.AppendBlock(transferBlock(height, parent, hash, fmt.Sprintf("tx-%d", height))); err != nil {
			t.Fatalf("AppendBlock(%d) error = %v", height, err)
		}
		parent = hash
	}
	waitGroup.Wait()
	head, err := chain.Head(context.Background())
	if err != nil || head.Height != blockCount {
		t.Fatalf("最终链头 = %+v, %v", head, err)
	}
}

func newChain(t *testing.T) *simulator.Simulator {
	t.Helper()
	chain, err := simulator.New(network, "genesis")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return chain
}

func transferBlock(height uint64, parent, hash, transactionID string) tron.Block {
	return tron.Block{
		Header:   tron.Header{Height: height, Hash: hash, ParentHash: parent},
		Receipts: []tron.Receipt{{TransactionID: transactionID, Outcome: tron.ExecutionSucceeded}},
		Transfers: []tron.Transfer{{
			ID: tron.EventID{
				Network:       network,
				Contract:      "usdt-contract",
				TransactionID: transactionID,
				LogIndex:      0,
			},
			From:   "sender",
			To:     "deposit-address",
			Amount: "1000000",
		}},
	}
}
