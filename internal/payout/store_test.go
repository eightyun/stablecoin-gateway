package payout

import (
	"errors"
	"testing"
)

func TestValidateRequest(t *testing.T) {
	request := Request{
		ID: "123e4567-e89b-42d3-a456-426614174000", MerchantID: "123e4567-e89b-42d3-a456-426614174001",
		AssetID: "usdt-tron", IdempotencyKey: "idem-1", MerchantReference: "order-1",
		DestinationAddress: "T9yD14Nj9j7xAB4dbGeiX9h8unkKHxuWwb", Amount: "1000000",
	}
	if amount, err := validateRequest(request); err != nil || amount != 1_000_000 {
		t.Fatalf("validateRequest() = %d, %v", amount, err)
	}
	request.Amount = "010"
	if _, err := validateRequest(request); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("validateRequest() error = %v", err)
	}
}

func TestRequestFingerprintIgnoresGeneratedAndIdempotencyIDs(t *testing.T) {
	request := Request{
		ID: "123e4567-e89b-42d3-a456-426614174000", MerchantID: "123e4567-e89b-42d3-a456-426614174001",
		AssetID: "usdt-tron", IdempotencyKey: "idem-1", MerchantReference: "order-1",
		DestinationAddress: "T9yD14Nj9j7xAB4dbGeiX9h8unkKHxuWwb", Amount: "1000000",
	}
	first, err := requestFingerprint(request)
	if err != nil {
		t.Fatalf("requestFingerprint() error = %v", err)
	}
	request.ID = "123e4567-e89b-42d3-a456-426614174099"
	request.IdempotencyKey = "idem-retry"
	second, err := requestFingerprint(request)
	if err != nil || first != second {
		t.Fatalf("重试摘要 first=%q second=%q error=%v", first, second, err)
	}
}
