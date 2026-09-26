package tron

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const (
	testTransferOwner       = "T9yD14Nj9j7xAB4dbGeiX9h8unkKHxuWwb"
	testTransferContract    = "TXYZopYRdj2D9XRtbG411XZZ3kM5VkAeBf"
	testTransferDestination = "TXLAQ63Xg1NAzckPwKHvzw7CSEmLMEqcdj"
)

func TestValidateSignedTransferTransaction(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	transaction := transferTransactionForTest(t, now, 100_000_000, time.Minute, false)
	expected := transferExpectationForTest(now)
	if err := ValidateSignedTransferTransaction(transaction, expected); err != nil {
		t.Fatalf("ValidateSignedTransferTransaction() error = %v", err)
	}

	tests := []struct {
		name        string
		transaction SignedTransaction
		expected    TransferTransactionExpectation
	}{
		{name: "错误付款地址", transaction: transaction, expected: func() TransferTransactionExpectation {
			value := expected
			value.OwnerAddress = testTransferDestination
			return value
		}()},
		{name: "错误合约", transaction: transaction, expected: func() TransferTransactionExpectation {
			value := expected
			value.ContractAddress = testTransferDestination
			return value
		}()},
		{name: "错误收款地址", transaction: transaction, expected: func() TransferTransactionExpectation {
			value := expected
			value.DestinationAddress = testTransferOwner
			return value
		}()},
		{name: "错误金额", transaction: transaction, expected: func() TransferTransactionExpectation {
			value := expected
			value.Amount = "1000001"
			return value
		}()},
		{name: "费用超限", transaction: transferTransactionForTest(t, now, 100_000_001, time.Minute, false), expected: expected},
		{name: "有效期过长", transaction: transferTransactionForTest(t, now, 100_000_000, 11*time.Minute, false), expected: expected},
		{name: "多个合约", transaction: transferTransactionForTest(t, now, 100_000_000, time.Minute, true), expected: expected},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateSignedTransferTransaction(test.transaction, test.expected); err != ErrInvalidSignedTransaction {
				t.Fatalf("ValidateSignedTransferTransaction() error = %v", err)
			}
		})
	}
}

func transferExpectationForTest(now time.Time) TransferTransactionExpectation {
	return TransferTransactionExpectation{
		OwnerAddress: testTransferOwner, ContractAddress: testTransferContract,
		DestinationAddress: testTransferDestination, Amount: "1000000", Now: now,
		MaxFeeLimit: 100_000_000, MaxLifetime: 10 * time.Minute,
		MinRemainingLifetime: 15 * time.Second, MaxFutureSkew: 30 * time.Second,
	}
}

func transferTransactionForTest(
	t *testing.T,
	now time.Time,
	feeLimit int64,
	lifetime time.Duration,
	duplicateContract bool,
) SignedTransaction {
	t.Helper()
	destination, err := NormalizeAddressHex(testTransferDestination)
	if err != nil {
		t.Fatal(err)
	}
	data := trc20TransferSelector + strings.Repeat("0", 24) + destination[2:] +
		strings.Repeat("0", 64-len("f4240")) + "f4240"
	contract := map[string]any{
		"parameter": map[string]any{
			"value": map[string]any{
				"owner_address": testTransferOwner, "contract_address": testTransferContract, "data": data,
			},
			"type_url": "type.googleapis.com/protocol.TriggerSmartContract",
		},
		"type": "TriggerSmartContract",
	}
	contracts := []any{contract}
	if duplicateContract {
		contracts = append(contracts, contract)
	}
	rawDataBytes := []byte("signed-transfer-test")
	digest := sha256.Sum256(rawDataBytes)
	id := hex.EncodeToString(digest[:])
	payload, err := json.Marshal(map[string]any{
		"txID": id,
		"raw_data": map[string]any{
			"ref_block_bytes": "0001", "ref_block_hash": "0011223344556677",
			"expiration": now.Add(lifetime).UnixMilli(), "timestamp": now.UnixMilli(),
			"fee_limit": feeLimit, "contract": contracts,
		},
		"raw_data_hex": hex.EncodeToString(rawDataBytes),
		"signature":    []string{strings.Repeat("a", 130)},
		"visible":      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return SignedTransaction{ID: id, Payload: payload}
}
