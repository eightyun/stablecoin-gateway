package signhttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/eightyun/stablecoin-gateway/internal/chain/tron"
)

const (
	testRequestID = "123e4567-e89b-42d3-a456-426614174000"
	testAddress   = "T9yD14Nj9j7xAB4dbGeiX9h8unkKHxuWwb"
	testTxID      = "6e340b9cffb37a989ca544e6bb780a2c78901d3fb33738768511a30617afa01d"
)

func TestClientSignsTransferOverHTTPS(t *testing.T) {
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
		_, _ = writer.Write([]byte(`{"transaction_id":"` + testTxID + `","signed_transaction":{"txID":"` + testTxID + `","raw_data":{"contract":[{}],"timestamp":1700000000000,"expiration":1700000060000},"raw_data_hex":"00","signature":["` + strings.Repeat("a", 130) + `"]}}`))
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL + "/signer", BearerToken: "secret"}, server.Client())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	transaction, err := client.SignTransfer(context.Background(), tron.TransferSignRequest{
		RequestID: testRequestID, Network: "tron-nile", ContractAddress: "contract",
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
	client, err := New(Config{BaseURL: server.URL, BearerToken: "secret"}, server.Client())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := client.SignTransfer(context.Background(), tron.TransferSignRequest{}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("SignTransfer() error = %v", err)
	}
}

func TestClientRejectsMismatchedTransactionID(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"transaction_id":"` + testTxID + `","signed_transaction":{"txID":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","raw_data":{"contract":[{}],"timestamp":1700000000000,"expiration":1700000060000},"raw_data_hex":"00","signature":["` + strings.Repeat("a", 130) + `"]}}`))
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, BearerToken: "secret"}, server.Client())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, err = client.SignTransfer(context.Background(), tron.TransferSignRequest{
		RequestID: testRequestID, Network: "tron-nile", ContractAddress: "contract",
		DestinationAddress: testAddress, Amount: "1",
	})
	if !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("SignTransfer() error = %v", err)
	}
}
