package tron

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"strings"
	"time"
)

const trc20TransferSelector = "a9059cbb"

// TransferTransactionExpectation 将签名交易绑定到已审批的出款语义和费用策略。
type TransferTransactionExpectation struct {
	OwnerAddress         string
	ContractAddress      string
	DestinationAddress   string
	Amount               string
	Now                  time.Time
	MaxFeeLimit          int64
	MaxLifetime          time.Duration
	MinRemainingLifetime time.Duration
	MaxFutureSkew        time.Duration
}

// ValidateSignedTransferTransaction 验证签名交易只执行预期的单笔 TRC20 transfer。
func ValidateSignedTransferTransaction(transaction SignedTransaction, expected TransferTransactionExpectation) error {
	metadata, err := ParseSignedTransactionMetadata(transaction)
	if err != nil || expected.Now.IsZero() || expected.MaxFeeLimit <= 0 || expected.MaxLifetime <= 0 ||
		expected.MinRemainingLifetime <= 0 || expected.MaxFutureSkew < 0 {
		return ErrInvalidSignedTransaction
	}
	owner, err := NormalizeAddressHex(expected.OwnerAddress)
	if err != nil {
		return ErrInvalidSignedTransaction
	}
	contract, err := NormalizeAddressHex(expected.ContractAddress)
	if err != nil {
		return ErrInvalidSignedTransaction
	}
	destination, err := NormalizeAddressHex(expected.DestinationAddress)
	if err != nil {
		return ErrInvalidSignedTransaction
	}
	amount, ok := new(big.Int).SetString(strings.TrimSpace(expected.Amount), 10)
	if !ok || amount.Sign() <= 0 || amount.BitLen() > 256 || amount.Text(10) != expected.Amount {
		return ErrInvalidSignedTransaction
	}
	now := expected.Now.UTC()
	if metadata.CreatedAt.After(now.Add(expected.MaxFutureSkew)) ||
		metadata.ExpiresAt.Sub(metadata.CreatedAt) > expected.MaxLifetime ||
		metadata.ExpiresAt.Before(now.Add(expected.MinRemainingLifetime)) {
		return ErrInvalidSignedTransaction
	}

	var envelope map[string]json.RawMessage
	if json.Unmarshal(transaction.Payload, &envelope) != nil || !onlyJSONFields(envelope,
		"txID", "raw_data", "raw_data_hex", "signature", "visible") {
		return ErrInvalidSignedTransaction
	}
	rawDataJSON, exists := envelope["raw_data"]
	if !exists {
		return ErrInvalidSignedTransaction
	}
	var rawFields map[string]json.RawMessage
	if json.Unmarshal(rawDataJSON, &rawFields) != nil || !onlyJSONFields(rawFields,
		"ref_block_bytes", "ref_block_hash", "expiration", "contract", "timestamp", "fee_limit") {
		return ErrInvalidSignedTransaction
	}
	var rawData struct {
		RefBlockBytes string `json:"ref_block_bytes"`
		RefBlockHash  string `json:"ref_block_hash"`
		Expiration    int64  `json:"expiration"`
		Timestamp     int64  `json:"timestamp"`
		FeeLimit      int64  `json:"fee_limit"`
		Contracts     []struct {
			Type         string `json:"type"`
			PermissionID int64  `json:"Permission_id"`
			Parameter    struct {
				TypeURL string `json:"type_url"`
				Value   struct {
					OwnerAddress    string `json:"owner_address"`
					ContractAddress string `json:"contract_address"`
					Data            string `json:"data"`
					CallValue       int64  `json:"call_value"`
					CallTokenValue  int64  `json:"call_token_value"`
					TokenID         int64  `json:"token_id"`
				} `json:"value"`
			} `json:"parameter"`
		} `json:"contract"`
	}
	if decodeStrictJSON(rawDataJSON, &rawData) != nil || len(rawData.Contracts) != 1 ||
		rawData.RefBlockBytes == "" || rawData.RefBlockHash == "" ||
		rawData.FeeLimit <= 0 || rawData.FeeLimit > expected.MaxFeeLimit {
		return ErrInvalidSignedTransaction
	}
	contractCall := rawData.Contracts[0]
	if contractCall.Type != "TriggerSmartContract" || contractCall.PermissionID != 0 ||
		contractCall.Parameter.TypeURL != "type.googleapis.com/protocol.TriggerSmartContract" ||
		contractCall.Parameter.Value.CallValue != 0 || contractCall.Parameter.Value.CallTokenValue != 0 ||
		contractCall.Parameter.Value.TokenID != 0 {
		return ErrInvalidSignedTransaction
	}
	actualOwner, ownerErr := NormalizeAddressHex(contractCall.Parameter.Value.OwnerAddress)
	actualContract, contractErr := NormalizeAddressHex(contractCall.Parameter.Value.ContractAddress)
	if ownerErr != nil || contractErr != nil || actualOwner != owner || actualContract != contract {
		return ErrInvalidSignedTransaction
	}
	expectedData := make([]byte, 4+32+32)
	selector, _ := hex.DecodeString(trc20TransferSelector)
	copy(expectedData[:4], selector)
	destinationBytes, _ := hex.DecodeString(destination[2:])
	copy(expectedData[4+12:4+32], destinationBytes)
	amount.FillBytes(expectedData[4+32:])
	if !strings.EqualFold(contractCall.Parameter.Value.Data, hex.EncodeToString(expectedData)) {
		return ErrInvalidSignedTransaction
	}
	return nil
}

func decodeStrictJSON(input []byte, output any) error {
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrInvalidSignedTransaction
	}
	return nil
}

func onlyJSONFields(fields map[string]json.RawMessage, allowed ...string) bool {
	allowedFields := make(map[string]struct{}, len(allowed))
	for _, field := range allowed {
		allowedFields[field] = struct{}{}
	}
	for field := range fields {
		if _, ok := allowedFields[field]; !ok {
			return false
		}
	}
	return true
}
