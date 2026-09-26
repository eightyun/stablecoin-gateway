package deposit

import (
	"errors"
	"testing"
	"time"
)

func TestValidatePoolIntent(t *testing.T) {
	valid := PoolIntentRequest{
		ID: "intent", MerchantID: "merchant", AssetID: "asset", IdempotencyKey: "key",
		MerchantReference: "order", ExpectedAmount: "100", ExpiresIn: 15 * time.Minute,
	}
	if err := validatePoolIntent(valid); err != nil {
		t.Fatalf("validatePoolIntent() error = %v", err)
	}
	invalid := valid
	invalid.ExpiresIn = time.Second
	if err := validatePoolIntent(invalid); !errors.Is(err, ErrInvalidPoolIntent) {
		t.Fatalf("validatePoolIntent() error = %v", err)
	}
}

func TestPoolIntentFingerprintIgnoresGeneratedID(t *testing.T) {
	request := PoolIntentRequest{
		ID: "first", MerchantID: "merchant", AssetID: "asset", IdempotencyKey: "key",
		MerchantReference: "order", ExpectedAmount: "100", ExpiresIn: 15 * time.Minute,
	}
	first, err := poolIntentFingerprint(request)
	if err != nil {
		t.Fatalf("poolIntentFingerprint() error = %v", err)
	}
	request.ID = "retry"
	second, err := poolIntentFingerprint(request)
	if err != nil || first != second {
		t.Fatalf("重试摘要 first=%q second=%q error=%v", first, second, err)
	}
}
