package deposit

import (
	"errors"
	"testing"
	"time"
)

func TestNewStoreRejectsNilDatabase(t *testing.T) {
	if _, err := NewStore(nil); !errors.Is(err, ErrDatabaseRequired) {
		t.Fatalf("NewStore() error = %v", err)
	}
}

func TestIntentValidation(t *testing.T) {
	valid := Intent{
		ID: "intent", MerchantID: "merchant", AssetID: "asset", DepositAddressID: "address",
		IdempotencyKey: "key", MerchantReference: "order", ExpectedAmount: "1", ExpiresAt: time.Now().Add(time.Hour),
	}
	if err := validateIntent(valid); err != nil {
		t.Fatalf("validateIntent() error = %v", err)
	}
	for _, amount := range []string{"", "0", "01", "-1", "1.0", "115792089237316195423570985008687907853269984665640564039457584007913129639936"} {
		invalid := valid
		invalid.ExpectedAmount = amount
		if err := validateIntent(invalid); !errors.Is(err, ErrInvalidIntent) {
			t.Errorf("金额 %q error = %v", amount, err)
		}
	}
}

func TestNormalizeAddress(t *testing.T) {
	address := "41A614F803B6FD780986A42C78EC9C7F77E6DED13C"
	normalized, err := normalizeAddress(address)
	if err != nil || normalized != "41a614f803b6fd780986a42c78ec9c7f77e6ded13c" {
		t.Fatalf("normalizeAddress() = %q, %v", normalized, err)
	}
	if _, err := normalizeAddress("TInvalid"); !errors.Is(err, ErrInvalidAddress) {
		t.Fatalf("无效 normalizeAddress() error = %v", err)
	}
}
