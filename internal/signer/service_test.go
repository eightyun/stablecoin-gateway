package signer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/eightyun/stablecoin-gateway/internal/chain/tron"
)

const (
	testOwner       = "TMVQGm1qAQYVdetCeGRRkTWYYrLXuHK2HC"
	testContract    = "TXYZopYRdj2D9XRtbG411XZZ3kM5VkAeBf"
	testDestination = "TXLAQ63Xg1NAzckPwKHvzw7CSEmLMEqcdj"
	testRequestID   = "123e4567-e89b-12d3-a456-426614174000"
)

type builderStub struct {
	now   time.Time
	calls int
}

func (builder *builderStub) BuildTransfer(_ context.Context, request tron.TransferSignRequest) (tron.UnsignedTransaction, error) {
	builder.calls++
	destination, _ := tron.NormalizeAddressHex(request.DestinationAddress)
	amount := "f4240"
	if request.Amount == "1000001" {
		amount = "f4241"
	}
	data := "a9059cbb" + strings.Repeat("0", 24) + destination[2:] + strings.Repeat("0", 64-len(amount)) + amount
	rawData := []byte("signer-service-test")
	digest := sha256.Sum256(rawData)
	id := hex.EncodeToString(digest[:])
	payload, _ := json.Marshal(map[string]any{
		"txID": id,
		"raw_data": map[string]any{
			"ref_block_bytes": "0001", "ref_block_hash": "0011223344556677",
			"expiration": builder.now.Add(time.Minute).UnixMilli(), "timestamp": builder.now.UnixMilli(),
			"fee_limit": int64(100_000_000),
			"contract": []any{map[string]any{
				"parameter": map[string]any{
					"value": map[string]any{
						"owner_address": testOwner, "contract_address": testContract, "data": data,
					},
					"type_url": "type.googleapis.com/protocol.TriggerSmartContract",
				},
				"type": "TriggerSmartContract",
			}},
		},
		"raw_data_hex": hex.EncodeToString(rawData), "visible": true,
	})
	return tron.UnsignedTransaction{ID: id, Payload: payload, RawData: rawData}, nil
}

type keyStub struct {
	calls int
}

func (key *keyStub) Address() string { return testOwner }
func (key *keyStub) SignDigest(digest []byte) ([]byte, error) {
	key.calls++
	privateKeyBytes := make([]byte, 32)
	privateKeyBytes[31] = 1
	privateKey := secp256k1.PrivKeyFromBytes(privateKeyBytes)
	defer privateKey.Zero()
	compact := ecdsa.SignCompact(privateKey, digest, false)
	signature := make([]byte, 65)
	copy(signature[:64], compact[1:])
	signature[64] = compact[0] - 27
	return signature, nil
}

func TestServicePersistsIdempotentSignedTransaction(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	builder := &builderStub{now: now}
	key := &keyStub{}
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := NewFileStore(directory)
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}
	service, err := NewService(ServiceConfig{
		Network: "tron-nile", OwnerAddress: testOwner, AllowedContracts: []string{testContract},
		MaxAmount: "2000000", MaxFeeLimit: 100_000_000, MaxTransactionLifetime: 10 * time.Minute,
	}, builder, key, store)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	service.now = func() time.Time { return now }
	request := validTransferRequest()
	first, err := service.SignTransfer(context.Background(), request)
	if err != nil {
		t.Fatalf("SignTransfer() error = %v", err)
	}
	second, err := service.SignTransfer(context.Background(), request)
	if err != nil || first.ID != second.ID || string(first.Payload) != string(second.Payload) {
		t.Fatalf("第二次 SignTransfer() = %+v, %v", second, err)
	}
	if builder.calls != 1 || key.calls != 1 {
		t.Fatalf("builder calls = %d, key calls = %d", builder.calls, key.calls)
	}
	reopenedStore, err := NewFileStore(store.directory)
	if err != nil {
		t.Fatalf("重开 FileStore error = %v", err)
	}
	restartedBuilder := &builderStub{now: now}
	restartedKey := &keyStub{}
	restartedService, err := NewService(ServiceConfig{
		Network: "tron-nile", OwnerAddress: testOwner, AllowedContracts: []string{testContract},
		MaxAmount: "2000000", MaxFeeLimit: 100_000_000, MaxTransactionLifetime: 10 * time.Minute,
	}, restartedBuilder, restartedKey, reopenedStore)
	if err != nil {
		t.Fatal(err)
	}
	restartedService.now = func() time.Time { return now }
	recovered, err := restartedService.SignTransfer(context.Background(), request)
	if err != nil || string(recovered.Payload) != string(first.Payload) || restartedBuilder.calls != 0 || restartedKey.calls != 0 {
		t.Fatalf("重启恢复 = %+v, %v, builder=%d, key=%d", recovered, err, restartedBuilder.calls, restartedKey.calls)
	}
}

func TestServiceRejectsIdempotencyConflictAndPolicyViolation(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	builder := &builderStub{now: now}
	key := &keyStub{}
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := NewFileStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(ServiceConfig{
		Network: "tron-nile", OwnerAddress: testOwner, AllowedContracts: []string{testContract},
		MaxAmount: "2000000", MaxFeeLimit: 100_000_000, MaxTransactionLifetime: 10 * time.Minute,
	}, builder, key, store)
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return now }
	request := validTransferRequest()
	if _, err := service.SignTransfer(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	request.Amount = "1000001"
	if _, err := service.SignTransfer(context.Background(), request); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("冲突 error = %v", err)
	}
	request.RequestID = "223e4567-e89b-12d3-a456-426614174000"
	request.Amount = "2000001"
	if _, err := service.SignTransfer(context.Background(), request); !errors.Is(err, ErrPolicyDenied) {
		t.Fatalf("超限 error = %v", err)
	}
	request.Amount = "1000000"
	request.ContractAddress = testDestination
	if _, err := service.SignTransfer(context.Background(), request); !errors.Is(err, ErrPolicyDenied) {
		t.Fatalf("非白名单 error = %v", err)
	}
}

func validTransferRequest() tron.TransferSignRequest {
	return tron.TransferSignRequest{
		RequestID: testRequestID, Network: "tron-nile", ContractAddress: testContract,
		DestinationAddress: testDestination, Amount: "1000000",
	}
}
