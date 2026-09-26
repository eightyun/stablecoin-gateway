package nodehttp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/chain/tron"
)

const (
	builderOwner       = "T9yD14Nj9j7xAB4dbGeiX9h8unkKHxuWwb"
	builderContract    = "TXYZopYRdj2D9XRtbG411XZZ3kM5VkAeBf"
	builderDestination = "TXLAQ63Xg1NAzckPwKHvzw7CSEmLMEqcdj"
)

func TestBuilderBuildsValidatedTransfer(t *testing.T) {
	now := time.Now().UTC()
	rawData := []byte("builder-test-raw-data")
	digest := sha256.Sum256(rawData)
	transactionID := hex.EncodeToString(digest[:])
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/wallet/triggersmartcontract" {
			t.Fatalf("path = %s", request.URL.Path)
		}
		var body struct {
			Parameter string `json:"parameter"`
			FeeLimit  int64  `json:"fee_limit"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil ||
			len(body.Parameter) != 128 || body.FeeLimit != 100_000_000 {
			t.Fatalf("请求 = %+v, %v", body, err)
		}
		destinationHex, _ := tron.NormalizeAddressHex(builderDestination)
		data := "a9059cbb" + strings.Repeat("0", 24) + destinationHex[2:] +
			strings.Repeat("0", 64-len("f4240")) + "f4240"
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(mustJSON(t, map[string]any{
			"result": map[string]any{"result": true},
			"transaction": map[string]any{
				"txID": transactionID,
				"raw_data": map[string]any{
					"ref_block_bytes": "0001", "ref_block_hash": "0011223344556677",
					"expiration": now.Add(time.Minute).UnixMilli(), "timestamp": now.UnixMilli(),
					"fee_limit": int64(100_000_000),
					"contract": []any{map[string]any{
						"parameter": map[string]any{
							"value": map[string]any{
								"owner_address": builderOwner, "contract_address": builderContract, "data": data,
							},
							"type_url": "type.googleapis.com/protocol.TriggerSmartContract",
						},
						"type": "TriggerSmartContract",
					}},
				},
				"raw_data_hex": hex.EncodeToString(rawData), "visible": true,
			},
		})))
	}))
	defer server.Close()
	builder, err := NewBuilder(BuilderConfig{
		BaseURL: server.URL, Network: "tron-nile", OwnerAddress: builderOwner,
		FeeLimit: 100_000_000, MaxTransactionLifetime: 10 * time.Minute,
	}, server.Client())
	if err != nil {
		t.Fatalf("NewBuilder() error = %v", err)
	}
	transaction, err := builder.BuildTransfer(context.Background(), tron.TransferSignRequest{
		Network: "tron-nile", ContractAddress: builderContract,
		DestinationAddress: builderDestination, Amount: "1000000",
	})
	if err != nil || transaction.ID != transactionID || string(transaction.RawData) != string(rawData) {
		t.Fatalf("BuildTransfer() = %+v, %v", transaction, err)
	}
}

func TestBuilderRejectsWrongNetwork(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	builder, err := NewBuilder(BuilderConfig{
		BaseURL: server.URL, Network: "tron-nile", OwnerAddress: builderOwner,
		FeeLimit: 100_000_000, MaxTransactionLifetime: 10 * time.Minute,
	}, server.Client())
	if err != nil {
		t.Fatalf("NewBuilder() error = %v", err)
	}
	_, err = builder.BuildTransfer(context.Background(), tron.TransferSignRequest{Network: "tron-mainnet"})
	if err != ErrBuildRejected {
		t.Fatalf("BuildTransfer() error = %v", err)
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
