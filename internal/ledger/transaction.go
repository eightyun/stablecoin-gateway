package ledger

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

var (
	ErrTransactionIDRequired = errors.New("账务交易 ID 不能为空")
	ErrIdempotencyRequired   = errors.New("幂等键不能为空")
	ErrRequesterRequired     = errors.New("请求方不能为空")
	ErrReferenceRequired     = errors.New("业务引用不能为空")
	ErrEntriesRequired       = errors.New("账务交易至少需要两条分录")
	ErrAccountIDRequired     = errors.New("账户 ID 不能为空")
	ErrAssetIDRequired       = errors.New("资产 ID 不能为空")
	ErrInvalidSide           = errors.New("借贷方向无效")
	ErrInvalidAmount         = errors.New("分录金额必须大于 0")
	ErrAmountOverflow        = errors.New("分录金额累计溢出")
	ErrUnbalancedTransaction = errors.New("账务交易借贷不平衡")
)

// Side 表示分录的借贷方向。
type Side uint8

const (
	Debit Side = iota + 1
	Credit
)

// Entry 表示一条不可变账务分录，金额使用资产最小单位。
type Entry struct {
	AccountID string
	AssetID   string
	Side      Side
	Amount    int64
}

// Transaction 表示一次需要原子过账的完整账务交易。
type Transaction struct {
	ID             string
	RequesterType  string
	RequesterID    string
	IdempotencyKey string
	ReferenceType  string
	ReferenceID    string
	Entries        []Entry
}

type totals struct {
	debit  int64
	credit int64
}

// ValidateTransaction 校验交易基础字段，并按资产检查借贷平衡。
func ValidateTransaction(transaction Transaction) error {
	if strings.TrimSpace(transaction.ID) == "" {
		return ErrTransactionIDRequired
	}
	if strings.TrimSpace(transaction.IdempotencyKey) == "" {
		return ErrIdempotencyRequired
	}
	if strings.TrimSpace(transaction.RequesterType) == "" || strings.TrimSpace(transaction.RequesterID) == "" {
		return ErrRequesterRequired
	}
	if strings.TrimSpace(transaction.ReferenceType) == "" || strings.TrimSpace(transaction.ReferenceID) == "" {
		return ErrReferenceRequired
	}
	if len(transaction.Entries) < 2 {
		return ErrEntriesRequired
	}

	assetTotals := make(map[string]totals)
	for index, entry := range transaction.Entries {
		if strings.TrimSpace(entry.AccountID) == "" {
			return fmt.Errorf("第 %d 条分录: %w", index+1, ErrAccountIDRequired)
		}
		assetID := strings.TrimSpace(entry.AssetID)
		if assetID == "" {
			return fmt.Errorf("第 %d 条分录: %w", index+1, ErrAssetIDRequired)
		}
		if entry.Amount <= 0 {
			return fmt.Errorf("第 %d 条分录: %w", index+1, ErrInvalidAmount)
		}

		assetTotal := assetTotals[assetID]
		switch entry.Side {
		case Debit:
			if assetTotal.debit > math.MaxInt64-entry.Amount {
				return fmt.Errorf("资产 %s: %w", assetID, ErrAmountOverflow)
			}
			assetTotal.debit += entry.Amount
		case Credit:
			if assetTotal.credit > math.MaxInt64-entry.Amount {
				return fmt.Errorf("资产 %s: %w", assetID, ErrAmountOverflow)
			}
			assetTotal.credit += entry.Amount
		default:
			return fmt.Errorf("第 %d 条分录: %w", index+1, ErrInvalidSide)
		}
		assetTotals[assetID] = assetTotal
	}

	for assetID, assetTotal := range assetTotals {
		if assetTotal.debit != assetTotal.credit {
			return fmt.Errorf("资产 %s 借方 %d 贷方 %d: %w", assetID, assetTotal.debit, assetTotal.credit, ErrUnbalancedTransaction)
		}
	}

	return nil
}
