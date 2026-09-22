// Package simulator 提供不依赖真实节点和钱包的确定性 TRON 适配器。
package simulator

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/big"
	"sync"

	"github.com/eightyun/stablecoin-gateway/internal/chain/tron"
)

var (
	ErrInvalidChain        = errors.New("模拟链配置无效")
	ErrInvalidBlock        = errors.New("模拟区块无效")
	ErrFinalizedBlock      = errors.New("不能替换已固化区块")
	ErrInvalidFinality     = errors.New("固化高度无效")
	ErrInvalidTransaction  = errors.New("已签名交易无效")
	ErrTransactionConflict = errors.New("同一交易 ID 对应不同签名内容")
	ErrInvalidFailurePoint = errors.New("故障注入位置无效")
)

// FailurePoint 指定下一次链操作发生故障的位置。
type FailurePoint uint8

const (
	FailHead FailurePoint = iota + 1
	FailSolidifiedHead
	FailBlockByHeight
	FailTransaction
	FailBroadcastBeforeAccept
	FailBroadcastAfterAccept
)

// Simulator 保留一条可替换未固化后缀的规范链。
type Simulator struct {
	mu          sync.Mutex
	network     string
	blocks      []tron.Block
	solidified  uint64
	broadcasted map[string][]byte
	failures    map[FailurePoint][]error
}

var _ tron.Reader = (*Simulator)(nil)
var _ tron.Broadcaster = (*Simulator)(nil)

// New 创建含创世块的模拟链。相同输入和操作序列产生相同结果。
func New(network, genesisHash string) (*Simulator, error) {
	if network == "" || genesisHash == "" {
		return nil, ErrInvalidChain
	}
	return &Simulator{
		network:     network,
		blocks:      []tron.Block{{Header: tron.Header{Hash: genesisHash}}},
		broadcasted: make(map[string][]byte),
		failures:    make(map[FailurePoint][]error),
	}, nil
}

// AppendBlock 追加下一高度的区块，并验证区块与当前规范链连续。
func (simulator *Simulator) AppendBlock(block tron.Block) error {
	simulator.mu.Lock()
	defer simulator.mu.Unlock()
	if err := validateBlock(simulator.network, simulator.blocks, block); err != nil {
		return err
	}
	simulator.blocks = append(simulator.blocks, cloneBlock(block))
	return nil
}

// ReplaceUnfinalized 原子替换指定高度起的未固化后缀，可用于演练微分叉。
func (simulator *Simulator) ReplaceUnfinalized(fromHeight uint64, replacement []tron.Block) error {
	simulator.mu.Lock()
	defer simulator.mu.Unlock()
	if fromHeight <= simulator.solidified {
		return ErrFinalizedBlock
	}
	if fromHeight == 0 || fromHeight > uint64(len(simulator.blocks)) {
		return ErrInvalidBlock
	}

	candidate := append([]tron.Block(nil), simulator.blocks[:fromHeight]...)
	for _, block := range replacement {
		if err := validateBlock(simulator.network, candidate, block); err != nil {
			return err
		}
		candidate = append(candidate, cloneBlock(block))
	}
	simulator.blocks = candidate
	return nil
}

// Solidify 将已存在的区块标记为固化；固化高度只能单调前进。
func (simulator *Simulator) Solidify(height uint64) error {
	simulator.mu.Lock()
	defer simulator.mu.Unlock()
	if height < simulator.solidified || height >= uint64(len(simulator.blocks)) {
		return ErrInvalidFinality
	}
	simulator.solidified = height
	return nil
}

// FailNext 使指定位置的下一次操作返回错误；同一位置的故障按添加顺序消费。
func (simulator *Simulator) FailNext(point FailurePoint, failure error) error {
	if point < FailHead || point > FailBroadcastAfterAccept || failure == nil {
		return ErrInvalidFailurePoint
	}
	simulator.mu.Lock()
	defer simulator.mu.Unlock()
	simulator.failures[point] = append(simulator.failures[point], failure)
	return nil
}

// Head 返回最新可见区块，不保证固化。
func (simulator *Simulator) Head(ctx context.Context) (tron.Header, error) {
	simulator.mu.Lock()
	defer simulator.mu.Unlock()
	if err := simulator.readError(ctx, FailHead); err != nil {
		return tron.Header{}, err
	}
	return simulator.blocks[len(simulator.blocks)-1].Header, nil
}

// SolidifiedHead 返回当前已固化区块。
func (simulator *Simulator) SolidifiedHead(ctx context.Context) (tron.Header, error) {
	simulator.mu.Lock()
	defer simulator.mu.Unlock()
	if err := simulator.readError(ctx, FailSolidifiedHead); err != nil {
		return tron.Header{}, err
	}
	return simulator.blocks[simulator.solidified].Header, nil
}

// BlockByHeight 按高度读取当前规范链上的区块，返回独立副本。
func (simulator *Simulator) BlockByHeight(ctx context.Context, height uint64) (tron.Block, error) {
	simulator.mu.Lock()
	defer simulator.mu.Unlock()
	if err := simulator.readError(ctx, FailBlockByHeight); err != nil {
		return tron.Block{}, err
	}
	if height >= uint64(len(simulator.blocks)) {
		return tron.Block{}, tron.ErrBlockNotFound
	}
	return cloneBlock(simulator.blocks[height]), nil
}

// Transaction 查询当前规范链上的执行结果；未发现不能解释成失败。
func (simulator *Simulator) Transaction(ctx context.Context, transactionID string) (tron.TransactionState, error) {
	simulator.mu.Lock()
	defer simulator.mu.Unlock()
	if err := simulator.readError(ctx, FailTransaction); err != nil {
		return tron.TransactionState{}, err
	}
	if transactionID == "" {
		return tron.TransactionState{}, ErrInvalidTransaction
	}
	for index := len(simulator.blocks) - 1; index >= 0; index-- {
		block := simulator.blocks[index]
		for _, receipt := range block.Receipts {
			if receipt.TransactionID != transactionID {
				continue
			}
			status := tron.TransactionFailed
			if receipt.Outcome == tron.ExecutionSucceeded {
				status = tron.TransactionSucceeded
			}
			return tron.TransactionState{
				Status:     status,
				Block:      block.Header,
				Solidified: block.Header.Height <= simulator.solidified,
			}, nil
		}
	}
	if _, found := simulator.broadcasted[transactionID]; found {
		return tron.TransactionState{Status: tron.TransactionPending}, nil
	}
	return tron.TransactionState{Status: tron.TransactionNotFound}, nil
}

// Broadcast 接收已签名交易；注入接收后故障时，交易已记录但调用方只得到未知结果。
func (simulator *Simulator) Broadcast(ctx context.Context, transaction tron.SignedTransaction) error {
	simulator.mu.Lock()
	defer simulator.mu.Unlock()
	if err := simulator.readError(ctx, FailBroadcastBeforeAccept); err != nil {
		return err
	}
	if transaction.ID == "" || len(transaction.Payload) == 0 {
		return ErrInvalidTransaction
	}
	if existing, found := simulator.broadcasted[transaction.ID]; found {
		if !bytes.Equal(existing, transaction.Payload) {
			return ErrTransactionConflict
		}
	} else {
		simulator.broadcasted[transaction.ID] = bytes.Clone(transaction.Payload)
	}
	if err := simulator.readError(ctx, FailBroadcastAfterAccept); err != nil {
		return err
	}
	return nil
}

func (simulator *Simulator) readError(ctx context.Context, point FailurePoint) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	queue := simulator.failures[point]
	if len(queue) == 0 {
		return nil
	}
	simulator.failures[point] = queue[1:]
	return queue[0]
}

func validateBlock(network string, chain []tron.Block, block tron.Block) error {
	previous := chain[len(chain)-1].Header
	if block.Header.Height != previous.Height+1 ||
		block.Header.Hash == "" ||
		block.Header.ParentHash != previous.Hash {
		return fmt.Errorf("区块高度或父哈希不连续: %w", ErrInvalidBlock)
	}
	for _, existing := range chain {
		if existing.Header.Hash == block.Header.Hash {
			return fmt.Errorf("区块哈希重复: %w", ErrInvalidBlock)
		}
	}

	receipts := make(map[string]tron.ExecutionOutcome, len(block.Receipts))
	for _, receipt := range block.Receipts {
		if receipt.TransactionID == "" ||
			(receipt.Outcome != tron.ExecutionSucceeded && receipt.Outcome != tron.ExecutionFailed) {
			return fmt.Errorf("执行结果无效: %w", ErrInvalidBlock)
		}
		if _, exists := receipts[receipt.TransactionID]; exists {
			return fmt.Errorf("区块中交易重复: %w", ErrInvalidBlock)
		}
		for _, existing := range chain {
			for _, previousReceipt := range existing.Receipts {
				if previousReceipt.TransactionID == receipt.TransactionID {
					return fmt.Errorf("规范链上交易重复: %w", ErrInvalidBlock)
				}
			}
		}
		receipts[receipt.TransactionID] = receipt.Outcome
	}

	type logPosition struct {
		transactionID string
		index         uint32
	}
	positions := make(map[logPosition]struct{}, len(block.Transfers))
	for _, transfer := range block.Transfers {
		if transfer.ID.Network != network || transfer.ID.Contract == "" ||
			transfer.ID.TransactionID == "" || transfer.From == "" || transfer.To == "" ||
			receipts[transfer.ID.TransactionID] != tron.ExecutionSucceeded || !positiveInteger(transfer.Amount) {
			return fmt.Errorf("TRC20 日志无效: %w", ErrInvalidBlock)
		}
		position := logPosition{transactionID: transfer.ID.TransactionID, index: transfer.ID.LogIndex}
		if _, exists := positions[position]; exists {
			return fmt.Errorf("TRC20 日志位置重复: %w", ErrInvalidBlock)
		}
		positions[position] = struct{}{}
	}
	return nil
}

func positiveInteger(value string) bool {
	// TRC20 的 uint256 最多 78 位十进制数字；超长输入不进入大整数解析。
	if value == "" || len(value) > 78 || value[0] == '0' {
		return false
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	amount, ok := new(big.Int).SetString(value, 10)
	return ok && amount.Sign() > 0 && amount.BitLen() <= 256
}

func cloneBlock(block tron.Block) tron.Block {
	block.Receipts = append([]tron.Receipt(nil), block.Receipts...)
	block.Transfers = append([]tron.Transfer(nil), block.Transfers...)
	return block
}
