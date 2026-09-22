package indexer

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strings"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/chain/tron"
	"github.com/jackc/pgx/v5"
)

var (
	ErrInvalidScanner = errors.New("扫描器配置无效")
	ErrInvalidEvent   = errors.New("链事件内容无效")
	ErrEventConflict  = errors.New("链事件身份对应的历史事实冲突")
)

// Scanner 每次只处理一个已固化区块，不执行用户入账。
type Scanner struct {
	store         *CursorStore
	reader        tron.FinalizedReader
	network       string
	contract      string
	workerID      string
	leaseDuration time.Duration
}

// StepResult 描述一次扫描的持久化结果。
type StepResult struct {
	Processed  bool
	Height     uint64
	EventCount int
}

// NewScanner 创建只读取已固化区块的扫描器。游标须事先由 Ensure 建立。
func NewScanner(store *CursorStore, reader tron.FinalizedReader, network, contract, workerID string, leaseDuration time.Duration) (*Scanner, error) {
	if store == nil || reader == nil || strings.TrimSpace(network) == "" ||
		strings.TrimSpace(contract) == "" || strings.TrimSpace(workerID) == "" ||
		leaseDuration.Milliseconds() <= 0 {
		return nil, ErrInvalidScanner
	}
	return &Scanner{store: store, reader: reader, network: network, contract: contract, workerID: workerID, leaseDuration: leaseDuration}, nil
}

// Step 保存下一已固化区块的目标合约事件，并在同一事务中推进游标。
func (scanner *Scanner) Step(ctx context.Context) (result StepResult, err error) {
	if err = scanner.store.BindContract(ctx, scanner.network, scanner.contract); err != nil {
		return StepResult{}, err
	}
	claim, err := scanner.store.Claim(ctx, scanner.network, scanner.workerID, scanner.leaseDuration)
	if err != nil {
		return StepResult{}, err
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if releaseErr := scanner.store.Release(releaseCtx, claim); releaseErr != nil {
			err = errors.Join(err, releaseErr)
		}
	}()

	head, err := scanner.reader.SolidifiedHead(ctx)
	if err != nil {
		return StepResult{}, fmt.Errorf("读取已固化链头: %w", err)
	}
	if head.Height < uint64(claim.NextHeight) {
		return StepResult{}, nil
	}
	block, err := scanner.reader.SolidifiedBlockByHeight(ctx, uint64(claim.NextHeight))
	if err != nil {
		return StepResult{}, fmt.Errorf("读取已固化区块 %d: %w", claim.NextHeight, err)
	}
	if block.Header.Height != uint64(claim.NextHeight) || block.Header.Hash == "" ||
		block.Header.Hash == claim.PreviousHash || block.Header.ParentHash != claim.PreviousHash ||
		(block.Header.Height == head.Height && block.Header.Hash != head.Hash) {
		return StepResult{}, ErrInvalidBlock
	}
	transfers, err := scanner.validTransfers(block)
	if err != nil {
		return StepResult{}, err
	}

	transaction, err := scanner.store.db.Begin(ctx)
	if err != nil {
		return StepResult{}, fmt.Errorf("开始扫描事务: %w", err)
	}
	defer transaction.Rollback(context.Background())
	for _, transfer := range transfers {
		if err := insertEvent(ctx, transaction, block.Header, transfer); err != nil {
			return StepResult{}, err
		}
	}
	if err := scanner.store.AdvanceTx(ctx, transaction, claim, block.Header); err != nil {
		return StepResult{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return StepResult{}, fmt.Errorf("提交扫描事务: %w", err)
	}
	committed = true
	return StepResult{Processed: true, Height: block.Header.Height, EventCount: len(transfers)}, nil
}

func (scanner *Scanner) validTransfers(block tron.Block) ([]tron.Transfer, error) {
	receipts := make(map[string]tron.ExecutionOutcome, len(block.Receipts))
	for _, receipt := range block.Receipts {
		if receipt.TransactionID == "" ||
			(receipt.Outcome != tron.ExecutionSucceeded && receipt.Outcome != tron.ExecutionFailed) {
			return nil, ErrInvalidEvent
		}
		if _, exists := receipts[receipt.TransactionID]; exists {
			return nil, ErrInvalidEvent
		}
		receipts[receipt.TransactionID] = receipt.Outcome
	}
	var transfers []tron.Transfer
	positions := make(map[tron.EventID]struct{})
	for _, transfer := range block.Transfers {
		if transfer.ID.Contract != scanner.contract {
			continue
		}
		if transfer.ID.Network != scanner.network || transfer.ID.TransactionID == "" ||
			transfer.From == "" || transfer.To == "" ||
			receipts[transfer.ID.TransactionID] != tron.ExecutionSucceeded ||
			!validAmount(transfer.Amount) {
			return nil, ErrInvalidEvent
		}
		if _, exists := positions[transfer.ID]; exists {
			return nil, ErrInvalidEvent
		}
		positions[transfer.ID] = struct{}{}
		transfers = append(transfers, transfer)
	}
	return transfers, nil
}

func validAmount(value string) bool {
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

func insertEvent(ctx context.Context, transaction pgx.Tx, header tron.Header, transfer tron.Transfer) error {
	if header.Height > math.MaxInt64 {
		return ErrInvalidBlock
	}
	result, err := transaction.Exec(ctx, `
		INSERT INTO chain_events (
			network, contract, transaction_id, log_index, block_height,
			block_hash, from_address, to_address, amount
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (network, contract, transaction_id, log_index) DO NOTHING
	`, transfer.ID.Network, transfer.ID.Contract, transfer.ID.TransactionID,
		int64(transfer.ID.LogIndex), int64(header.Height), header.Hash,
		transfer.From, transfer.To, transfer.Amount)
	if err != nil {
		return fmt.Errorf("保存链事件: %w", err)
	}
	if result.RowsAffected() == 1 {
		return nil
	}
	var height int64
	var hash, from, to, amount string
	if err := transaction.QueryRow(ctx, `
		SELECT block_height, block_hash, from_address, to_address, amount::TEXT
		FROM chain_events
		WHERE network = $1 AND contract = $2 AND transaction_id = $3 AND log_index = $4
	`, transfer.ID.Network, transfer.ID.Contract, transfer.ID.TransactionID,
		int64(transfer.ID.LogIndex)).Scan(&height, &hash, &from, &to, &amount); err != nil {
		return fmt.Errorf("核对重复链事件: %w", err)
	}
	if height != int64(header.Height) || hash != header.Hash || from != transfer.From ||
		to != transfer.To || amount != transfer.Amount {
		return fmt.Errorf("%s/%s/%s/%d: %w", transfer.ID.Network, transfer.ID.Contract,
			transfer.ID.TransactionID, transfer.ID.LogIndex, ErrEventConflict)
	}
	return nil
}
