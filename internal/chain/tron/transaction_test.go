package tron

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestValidateSignedTransaction(t *testing.T) {
	rawData := []byte{0x01, 0x02, 0x03}
	digest := sha256.Sum256(rawData)
	transactionID := hex.EncodeToString(digest[:])
	transaction := SignedTransaction{
		ID: transactionID,
		Payload: []byte(`{"txID":"` + transactionID + `","raw_data":{"contract":[{}],"timestamp":1700000000000,"expiration":1700000060000},"raw_data_hex":"` +
			hex.EncodeToString(rawData) + `","signature":["` + strings.Repeat("a", 130) + `"]}`),
	}
	metadata, err := ParseSignedTransactionMetadata(transaction)
	if err != nil || metadata.ExpiresAt.Sub(metadata.CreatedAt) != time.Minute {
		t.Fatalf("ParseSignedTransactionMetadata() = %+v, %v", metadata, err)
	}
	transaction.ID = strings.Repeat("b", 64)
	if err := ValidateSignedTransaction(transaction); !errors.Is(err, ErrInvalidSignedTransaction) {
		t.Fatalf("ValidateSignedTransaction() error = %v", err)
	}
}
