package indexer

import (
	"errors"
	"testing"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/chain/tron"
)

func TestNewScannerRejectsInvalidConfiguration(t *testing.T) {
	if _, err := NewScanner(nil, nil, "tron", "contract", "worker", time.Minute); !errors.Is(err, ErrInvalidScanner) {
		t.Fatalf("NewScanner() error = %v", err)
	}
}

func TestValidAmount(t *testing.T) {
	for _, amount := range []string{"1", "1000000", "115792089237316195423570985008687907853269984665640564039457584007913129639935"} {
		if !validAmount(amount) {
			t.Errorf("有效金额 %q 被拒绝", amount)
		}
	}
	for _, amount := range []string{"", "0", "01", "-1", "1.0", "115792089237316195423570985008687907853269984665640564039457584007913129639936"} {
		if validAmount(amount) {
			t.Errorf("无效金额 %q 被接受", amount)
		}
	}
}

func TestScannerRejectsFailedReceipt(t *testing.T) {
	scanner := &Scanner{network: "tron", contract: "USDT"}
	block := tron.Block{
		Receipts:  []tron.Receipt{{TransactionID: "tx", Outcome: tron.ExecutionFailed}},
		Transfers: []tron.Transfer{{ID: tron.EventID{Network: "tron", Contract: "USDT", TransactionID: "tx"}, From: "a", To: "b", Amount: "1"}},
	}
	if _, err := scanner.validTransfers(block); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("失败交易中的 Transfer error = %v", err)
	}
}
