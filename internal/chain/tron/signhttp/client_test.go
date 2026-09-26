package signhttp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/chain/tron"
)

const (
	testRequestID    = "123e4567-e89b-42d3-a456-426614174000"
	testAddress      = "T9yD14Nj9j7xAB4dbGeiX9h8unkKHxuWwb"
	testContract     = "TXYZopYRdj2D9XRtbG411XZZ3kM5VkAeBf"
	testOtherAddress = "TXLAQ63Xg1NAzckPwKHvzw7CSEmLMEqcdj"
)

func TestClientSignsTransferOverHTTPS(t *testing.T) {
	responseBody, testTxID := signedTransferResponse(t, testAddress, "1000000")
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/signer/v1/tron/transfers:sign" {
			t.Fatalf("请求 = %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer secret" || request.Header.Get("Idempotency-Key") != testRequestID {
			t.Fatalf("请求头 = %+v", request.Header)
		}
		var body map[string]string
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body["amount"] != "1000000" ||
			body["destination_address"] != testAddress {
			t.Fatalf("请求体 = %+v, %v", body, err)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write(responseBody)
	}))
	defer server.Close()
	client, err := New(testClientConfig(server.URL+"/signer"), server.Client())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	transaction, err := client.SignTransfer(context.Background(), tron.TransferSignRequest{
		RequestID: testRequestID, Network: "tron-nile", ContractAddress: testContract,
		DestinationAddress: testAddress, Amount: "1000000",
	})
	if err != nil || transaction.ID != testTxID || !strings.Contains(string(transaction.Payload), `"signature"`) {
		t.Fatalf("SignTransfer() = %+v, %v", transaction, err)
	}
}

func TestClientRejectsInvalidConfigurationAndRequest(t *testing.T) {
	if _, err := New(Config{BaseURL: "http://signer.example", BearerToken: "secret"}, nil); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("New() error = %v", err)
	}
	server := httptest.NewTLSServer(http.NotFoundHandler())
	defer server.Close()
	client, err := New(testClientConfig(server.URL), server.Client())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := client.SignTransfer(context.Background(), tron.TransferSignRequest{}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("SignTransfer() error = %v", err)
	}
}

func TestClientRejectsMismatchedTransactionID(t *testing.T) {
	responseBody, _ := signedTransferResponse(t, testAddress, "1000000")
	var response map[string]json.RawMessage
	if err := json.Unmarshal(responseBody, &response); err != nil {
		t.Fatal(err)
	}
	response["transaction_id"] = json.RawMessage(`"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"`)
	responseBody, _ = json.Marshal(response)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write(responseBody)
	}))
	defer server.Close()
	client, err := New(testClientConfig(server.URL), server.Client())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, err = client.SignTransfer(context.Background(), tron.TransferSignRequest{
		RequestID: testRequestID, Network: "tron-nile", ContractAddress: testContract,
		DestinationAddress: testAddress, Amount: "1",
	})
	if !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("SignTransfer() error = %v", err)
	}
}

func TestClientRejectsSemanticallyDifferentTransaction(t *testing.T) {
	responseBody, _ := signedTransferResponse(t, testAddress, "1000000")
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write(responseBody)
	}))
	defer server.Close()
	client, err := New(testClientConfig(server.URL), server.Client())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, err = client.SignTransfer(context.Background(), tron.TransferSignRequest{
		RequestID: testRequestID, Network: "tron-nile", ContractAddress: testContract,
		DestinationAddress: testOtherAddress, Amount: "1000000",
	})
	if !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("SignTransfer() error = %v", err)
	}
}

func testClientConfig(baseURL string) Config {
	return Config{
		BaseURL: baseURL, BearerToken: "secret", ExpectedOwnerAddress: testAddress,
		MaxFeeLimit: 100_000_000, MaxTransactionLifetime: 10 * time.Minute,
	}
}

func signedTransferResponse(t *testing.T, destination, amount string) ([]byte, string) {
	t.Helper()
	now := time.Now().UTC()
	destinationHex, err := tron.NormalizeAddressHex(destination)
	if err != nil {
		t.Fatal(err)
	}
	amountValue := "f4240"
	if amount != "1000000" {
		t.Fatalf("测试辅助函数暂不支持金额 %s", amount)
	}
	data := "a9059cbb" + strings.Repeat("0", 24) + destinationHex[2:] +
		strings.Repeat("0", 64-len(amountValue)) + amountValue
	rawBytes := []byte("signhttp-semantic-test")
	digest := sha256.Sum256(rawBytes)
	transactionID := hex.EncodeToString(digest[:])
	transaction := map[string]any{
		"txID": transactionID,
		"raw_data": map[string]any{
			"ref_block_bytes": "0001", "ref_block_hash": "0011223344556677",
			"expiration": now.Add(time.Minute).UnixMilli(), "timestamp": now.UnixMilli(),
			"fee_limit": int64(100_000_000),
			"contract": []any{map[string]any{
				"parameter": map[string]any{
					"value": map[string]any{
						"owner_address": testAddress, "contract_address": testContract, "data": data,
					},
					"type_url": "type.googleapis.com/protocol.TriggerSmartContract",
				},
				"type": "TriggerSmartContract",
			}},
		},
		"raw_data_hex": hex.EncodeToString(rawBytes),
		"signature":    []string{strings.Repeat("a", 130)},
		"visible":      true,
	}
	response, err := json.Marshal(map[string]any{
		"transaction_id": transactionID, "signed_transaction": transaction,
	})
	if err != nil {
		t.Fatal(err)
	}
	return response, transactionID
}
