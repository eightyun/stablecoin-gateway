package ledger

import (
	"errors"
	"math"
	"testing"
)

func TestValidateTransaction(t *testing.T) {
	tests := []struct {
		name        string
		transaction Transaction
		wantErr     error
	}{
		{
			name: "单资产借贷平衡",
			transaction: Transaction{
				ID:             "journal-1",
				RequesterType:  "merchant",
				RequesterID:    "merchant-1",
				IdempotencyKey: "deposit:1",
				ReferenceType:  "deposit",
				ReferenceID:    "deposit-1",
				Entries: []Entry{
					{AccountID: "custody", AssetID: "usdt-tron", Side: Debit, Amount: 1_000_000},
					{AccountID: "merchant", AssetID: "usdt-tron", Side: Credit, Amount: 1_000_000},
				},
			},
		},
		{
			name: "多资产分别平衡",
			transaction: Transaction{
				ID:             "journal-2",
				RequesterType:  "merchant",
				RequesterID:    "merchant-1",
				IdempotencyKey: "exchange:1",
				ReferenceType:  "exchange",
				ReferenceID:    "exchange-1",
				Entries: []Entry{
					{AccountID: "usdt-a", AssetID: "usdt-tron", Side: Debit, Amount: 10},
					{AccountID: "usdt-b", AssetID: "usdt-tron", Side: Credit, Amount: 10},
					{AccountID: "usdc-a", AssetID: "usdc-base", Side: Debit, Amount: 20},
					{AccountID: "usdc-b", AssetID: "usdc-base", Side: Credit, Amount: 20},
				},
			},
		},
		{
			name: "交易 ID 为空",
			transaction: Transaction{
				IdempotencyKey: "deposit:2",
				Entries:        []Entry{{}, {}},
			},
			wantErr: ErrTransactionIDRequired,
		},
		{
			name: "幂等键为空",
			transaction: Transaction{
				ID:      "journal-3",
				Entries: []Entry{{}, {}},
			},
			wantErr: ErrIdempotencyRequired,
		},
		{
			name: "借贷不平衡",
			transaction: Transaction{
				ID:             "journal-4",
				RequesterType:  "merchant",
				RequesterID:    "merchant-1",
				IdempotencyKey: "deposit:4",
				ReferenceType:  "deposit",
				ReferenceID:    "deposit-4",
				Entries: []Entry{
					{AccountID: "custody", AssetID: "usdt-tron", Side: Debit, Amount: 10},
					{AccountID: "merchant", AssetID: "usdt-tron", Side: Credit, Amount: 9},
				},
			},
			wantErr: ErrUnbalancedTransaction,
		},
		{
			name: "累计金额溢出",
			transaction: Transaction{
				ID:             "journal-5",
				RequesterType:  "merchant",
				RequesterID:    "merchant-1",
				IdempotencyKey: "deposit:5",
				ReferenceType:  "deposit",
				ReferenceID:    "deposit-5",
				Entries: []Entry{
					{AccountID: "custody-a", AssetID: "usdt-tron", Side: Debit, Amount: math.MaxInt64},
					{AccountID: "custody-b", AssetID: "usdt-tron", Side: Debit, Amount: 1},
					{AccountID: "merchant", AssetID: "usdt-tron", Side: Credit, Amount: math.MaxInt64},
				},
			},
			wantErr: ErrAmountOverflow,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateTransaction(test.transaction)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("ValidateTransaction() error = %v, 期望 %v", err, test.wantErr)
			}
		})
	}
}
