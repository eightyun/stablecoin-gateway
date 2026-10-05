package acceptance

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/sweep"
	"github.com/eightyun/stablecoin-gateway/internal/wallet"
)

const (
	testSourceAddress      = "410000000000000000000000000000000000000000"
	testDestinationAddress = "411111111111111111111111111111111111111111"
)

func TestValidateSweepWallets(t *testing.T) {
	asset := wallet.Asset{ID: "usdt-trc20-nile", Network: "tron-nile"}
	wallets := []wallet.Wallet{
		{ID: "source", AssetID: asset.ID, Address: testSourceAddress, Role: "deposit"},
		{ID: "destination", AssetID: asset.ID, Address: testDestinationAddress, Role: "hot"},
	}
	if err := ValidateSweepWallets(
		asset, wallets, asset.Network, testSourceAddress, testDestinationAddress,
	); err != nil {
		t.Fatalf("ValidateSweepWallets() error = %v", err)
	}
	if err := ValidateSweepWallets(
		asset, wallets, "tron-mainnet", testSourceAddress, testDestinationAddress,
	); !errors.Is(err, ErrInvalidSweepWallets) {
		t.Fatalf("错误网络 ValidateSweepWallets() error = %v", err)
	}
}

func TestWaitForSweep(t *testing.T) {
	reader := &executionReaderStub{states: []sweep.ExecutionState{
		{PlanID: "plan", Status: "planned"},
		{PlanID: "plan", Status: "confirming"},
		{PlanID: "plan", Status: "succeeded", TransactionID: "transaction"},
	}}
	state, err := WaitForSweep(context.Background(), reader, "plan", time.Millisecond)
	if err != nil || state.TransactionID != "transaction" {
		t.Fatalf("WaitForSweep() = %+v, %v", state, err)
	}
}

func TestWaitForSweepReturnsFailure(t *testing.T) {
	reader := &executionReaderStub{states: []sweep.ExecutionState{
		{PlanID: "plan", Status: "failed", FailureReason: "execution_failed"},
	}}
	state, err := WaitForSweep(context.Background(), reader, "plan", time.Millisecond)
	if !errors.Is(err, ErrSweepFailed) || state.FailureReason != "execution_failed" {
		t.Fatalf("WaitForSweep() = %+v, %v", state, err)
	}
}

type executionReaderStub struct {
	states []sweep.ExecutionState
	index  int
}

func (stub *executionReaderStub) GetExecution(context.Context, string) (sweep.ExecutionState, error) {
	state := stub.states[stub.index]
	if stub.index < len(stub.states)-1 {
		stub.index++
	}
	return state, nil
}
