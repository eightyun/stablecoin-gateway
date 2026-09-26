package payout

import (
	"errors"
	"testing"

	"github.com/eightyun/stablecoin-gateway/internal/chain/tron"
)

func TestValidateSigningClaim(t *testing.T) {
	claim := SigningClaim{
		PayoutID: "123e4567-e89b-42d3-a456-426614174000",
		WorkerID: "signer-1", LeaseEpoch: 1,
	}
	if err := validateSigningClaim(claim); err != nil {
		t.Fatalf("validateSigningClaim() error = %v", err)
	}
	claim.LeaseEpoch = 0
	if err := validateSigningClaim(claim); !errors.Is(err, ErrInvalidSigningClaim) {
		t.Fatalf("validateSigningClaim() error = %v", err)
	}
}

func TestSignedTransactionValidationConstants(t *testing.T) {
	valid := tron.SignedTransaction{
		ID:      "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Payload: []byte("signed"),
	}
	if !transactionIDPattern.MatchString(valid.ID) || len(valid.Payload) > maxSignedTransactionBytes {
		t.Fatal("有效签名交易被拒绝")
	}
	if transactionIDPattern.MatchString("ABC") {
		t.Fatal("无效交易 ID 被接受")
	}
}
