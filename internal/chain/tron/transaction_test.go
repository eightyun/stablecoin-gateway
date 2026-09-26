package tron

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func TestValidateSignedTransaction(t *testing.T) {
	rawData := []byte{0x01, 0x02, 0x03}
	digest := sha256.Sum256(rawData)
	transactionID := hex.EncodeToString(digest[:])
	transaction := SignedTransaction{
		ID: transactionID,
		Payload: []byte(`{"txID":"` + transactionID + `","raw_data":{"contract":[]},"raw_data_hex":"` +
			hex.EncodeToString(rawData) + `","signature":["` + strings.Repeat("a", 130) + `"]}`),
	}
	if err := ValidateSignedTransaction(transaction); err != nil {
		t.Fatalf("ValidateSignedTransaction() error = %v", err)
	}
	transaction.ID = strings.Repeat("b", 64)
	if err := ValidateSignedTransaction(transaction); !errors.Is(err, ErrInvalidSignedTransaction) {
		t.Fatalf("ValidateSignedTransaction() error = %v", err)
	}
}
