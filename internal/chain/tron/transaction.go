package tron

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
)

const maxSignedTransactionBytes = 1 << 20

var (
	ErrInvalidSignedTransaction = errors.New("TRON 已签名交易无效")
	signedTransactionIDPattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// SignedTransactionMetadata 是广播恢复所需的不可变交易时间边界。
type SignedTransactionMetadata struct {
	CreatedAt time.Time
	ExpiresAt time.Time
}

// ValidateSignedTransaction 校验 TRON JSON 交易、签名格式，并从 raw_data_hex 重算 txID。
func ValidateSignedTransaction(transaction SignedTransaction) error {
	_, err := ParseSignedTransactionMetadata(transaction)
	return err
}

// ParseSignedTransactionMetadata 校验签名交易并返回链上时间与过期时间。
func ParseSignedTransactionMetadata(transaction SignedTransaction) (SignedTransactionMetadata, error) {
	if !signedTransactionIDPattern.MatchString(transaction.ID) || len(transaction.Payload) == 0 ||
		len(transaction.Payload) > maxSignedTransactionBytes || !json.Valid(transaction.Payload) {
		return SignedTransactionMetadata{}, ErrInvalidSignedTransaction
	}
	var signed struct {
		TransactionID string `json:"txID"`
		RawData       struct {
			Contracts  []json.RawMessage `json:"contract"`
			Timestamp  int64             `json:"timestamp"`
			Expiration int64             `json:"expiration"`
		} `json:"raw_data"`
		RawDataHex string   `json:"raw_data_hex"`
		Signatures []string `json:"signature"`
	}
	if err := json.Unmarshal(transaction.Payload, &signed); err != nil || signed.TransactionID != transaction.ID ||
		len(signed.RawData.Contracts) == 0 || signed.RawData.Timestamp <= 0 ||
		signed.RawData.Expiration <= signed.RawData.Timestamp || len(signed.Signatures) == 0 {
		return SignedTransactionMetadata{}, ErrInvalidSignedTransaction
	}
	rawData, err := hex.DecodeString(strings.TrimSpace(signed.RawDataHex))
	if err != nil || len(rawData) == 0 {
		return SignedTransactionMetadata{}, ErrInvalidSignedTransaction
	}
	digest := sha256.Sum256(rawData)
	if hex.EncodeToString(digest[:]) != transaction.ID {
		return SignedTransactionMetadata{}, ErrInvalidSignedTransaction
	}
	for _, signature := range signed.Signatures {
		decoded, err := hex.DecodeString(strings.TrimSpace(signature))
		if err != nil || len(decoded) != 65 {
			return SignedTransactionMetadata{}, ErrInvalidSignedTransaction
		}
	}
	createdAt := time.UnixMilli(signed.RawData.Timestamp).UTC()
	expiresAt := time.UnixMilli(signed.RawData.Expiration).UTC()
	if createdAt.Year() < 2018 || expiresAt.Year() > 9999 {
		return SignedTransactionMetadata{}, ErrInvalidSignedTransaction
	}
	return SignedTransactionMetadata{CreatedAt: createdAt, ExpiresAt: expiresAt}, nil
}
