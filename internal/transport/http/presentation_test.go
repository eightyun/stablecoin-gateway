package http

import (
	"testing"

	"github.com/eightyun/stablecoin-gateway/internal/deposit"
	"github.com/eightyun/stablecoin-gateway/internal/payout"
)

const (
	presentationHexZero    = "410000000000000000000000000000000000000000"
	presentationBase58Zero = "T9yD14Nj9j7xAB4dbGeiX9h8unkKHxuWwb"
	presentationHexAddress = "415a523b449890854c8fc460ab602df9f31fe4293f"
	presentationBase58     = "TJCnKsPa7y5okkXvQAidZBzqx3QyQ6sxMW"
)

func TestPresentDepositAndBalanceTRONAddresses(t *testing.T) {
	intent, err := presentDepositIntent(deposit.IntentDetails{
		Network: "tron-nile", ContractAddress: presentationHexZero,
		DepositAddress: presentationHexAddress,
	})
	if err != nil || intent.ContractAddress != presentationBase58Zero || intent.DepositAddress != presentationBase58 {
		t.Fatalf("presentDepositIntent() = %+v, %v", intent, err)
	}
	balances, err := presentBalances([]deposit.Balance{{
		Network: "tron-nile", ContractAddress: presentationHexAddress,
	}})
	if err != nil || len(balances) != 1 || balances[0].ContractAddress != presentationBase58 {
		t.Fatalf("presentBalances() = %+v, %v", balances, err)
	}
}

func TestPresentPayoutTRONAddresses(t *testing.T) {
	details, err := presentPayout(payout.Details{
		Network: "tron-nile", ContractAddress: presentationHexZero,
		DestinationAddress: presentationHexAddress,
	})
	if err != nil || details.ContractAddress != presentationBase58Zero || details.DestinationAddress != presentationBase58 {
		t.Fatalf("presentPayout() = %+v, %v", details, err)
	}
}

func TestPresentTRONAddressRejectsCorruptInternalValue(t *testing.T) {
	if _, err := presentDepositIntent(deposit.IntentDetails{
		Network: "tron-nile", ContractAddress: "invalid", DepositAddress: presentationHexAddress,
	}); err == nil {
		t.Fatal("presentDepositIntent() 应拒绝损坏的内部地址")
	}
	value, err := presentChainAddress("evm-base", "0x1234")
	if err != nil || value != "0x1234" {
		t.Fatalf("非 TRON 地址 = %q, %v", value, err)
	}
}
