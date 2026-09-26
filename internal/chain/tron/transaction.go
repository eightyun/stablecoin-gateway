package tron

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
)

const maxSignedTransactionBytes = 1 << 20

var (
	ErrInvalidSignedTransaction = errors.New("TRON 已签名交易无效")
	signedTransactionIDPattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// ValidateSignedTransaction 校验 TRON JSON 交易、签名格式，并从 raw_data_hex 重算 txID。
func ValidateSignedTransaction(transaction SignedTransaction) error {
	if !signedTransactionIDPattern.MatchString(transaction.ID) || len(transaction.Payload) == 0 ||
		len(transaction.Payload) > maxSignedTransactionBytes || !json.Valid(transaction.Payload) {
		return ErrInvalidSignedTransaction
	}
	var signed struct {
		TransactionID string                     `json:"txID"`
		RawData       map[string]json.RawMessage `json:"raw_data"`
		RawDataHex    string                     `json:"raw_data_hex"`
		Signatures    []string                   `json:"signature"`
	}
	if err := json.Unmarshal(transaction.Payload, &signed); err != nil || signed.TransactionID != transaction.ID ||
		len(signed.RawData) == 0 || len(signed.Signatures) == 0 {
		return ErrInvalidSignedTransaction
	}
	rawData, err := hex.DecodeString(strings.TrimSpace(signed.RawDataHex))
	if err != nil || len(rawData) == 0 {
		return ErrInvalidSignedTransaction
	}
	digest := sha256.Sum256(rawData)
	if hex.EncodeToString(digest[:]) != transaction.ID {
		return ErrInvalidSignedTransaction
	}
	for _, signature := range signed.Signatures {
		decoded, err := hex.DecodeString(strings.TrimSpace(signature))
		if err != nil || len(decoded) != 65 {
			return ErrInvalidSignedTransaction
		}
	}
	return nil
}
