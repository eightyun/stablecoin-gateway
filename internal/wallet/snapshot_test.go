package wallet

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/chain/tron"
)

func TestCollectSnapshot(t *testing.T) {
	header := snapshotHeader(10, "a")
	reader := &balanceReaderStub{
		headers: []tron.Header{header, header},
		balances: map[string]string{
			"411111111111111111111111111111111111111111": "7",
			"412222222222222222222222222222222222222222": "5",
		},
	}
	asset := Asset{ID: "usdt-tron", Network: "tron-nile", ContractAddress: "contract"}
	wallets := []Wallet{
		{ID: "223e4567-e89b-42d3-a456-426614174000", AssetID: asset.ID, Address: "412222222222222222222222222222222222222222", Role: "hot"},
		{ID: "123e4567-e89b-42d3-a456-426614174000", AssetID: asset.ID, Address: "411111111111111111111111111111111111111111", Role: "deposit"},
	}
	snapshot, err := CollectSnapshot(context.Background(), reader, asset, wallets)
	if err != nil {
		t.Fatalf("CollectSnapshot() error = %v", err)
	}
	if snapshot.ID == "" || snapshot.Block != header || snapshot.TotalBalance != "12" ||
		len(snapshot.Balances) != 2 || snapshot.Balances[0].WalletID != wallets[1].ID ||
		snapshot.Balances[0].Amount != "7" || snapshot.Balances[1].Amount != "5" {
		t.Fatalf("CollectSnapshot() = %+v", snapshot)
	}
}

func TestCollectSnapshotRejectsChangedHead(t *testing.T) {
	reader := &balanceReaderStub{
		headers:  []tron.Header{snapshotHeader(10, "a"), snapshotHeader(11, "b")},
		balances: map[string]string{"411111111111111111111111111111111111111111": "1"},
	}
	_, err := CollectSnapshot(context.Background(), reader,
		Asset{ID: "usdt-tron", Network: "tron-nile", ContractAddress: "contract"},
		[]Wallet{{
			ID: "123e4567-e89b-42d3-a456-426614174000", AssetID: "usdt-tron",
			Address: "411111111111111111111111111111111111111111", Role: "hot",
		}},
	)
	if !errors.Is(err, ErrHeadChanged) {
		t.Fatalf("CollectSnapshot() error = %v", err)
	}
}

func TestCollectSnapshotRejectsInvalidInputsAndBalances(t *testing.T) {
	header := snapshotHeader(10, "a")
	asset := Asset{ID: "usdt-tron", Network: "tron-nile", ContractAddress: "contract"}
	wallet := Wallet{
		ID: "123e4567-e89b-42d3-a456-426614174000", AssetID: asset.ID,
		Address: "411111111111111111111111111111111111111111", Role: "hot",
	}
	tests := []struct {
		name    string
		wallets []Wallet
		amount  string
	}{
		{name: "没有钱包"},
		{name: "重复钱包", wallets: []Wallet{wallet, wallet}, amount: "1"},
		{name: "非规范金额", wallets: []Wallet{wallet}, amount: "01"},
		{name: "负数金额", wallets: []Wallet{wallet}, amount: "-1"},
		{name: "超过 uint256", wallets: []Wallet{wallet}, amount: "1" + strings.Repeat("0", 78)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader := &balanceReaderStub{
				headers:  []tron.Header{header, header},
				balances: map[string]string{wallet.Address: test.amount},
			}
			if _, err := CollectSnapshot(context.Background(), reader, asset, test.wallets); !errors.Is(err, ErrInvalidSnapshot) {
				t.Fatalf("CollectSnapshot() error = %v", err)
			}
		})
	}
}

func TestCollectSnapshotReturnsReaderError(t *testing.T) {
	want := errors.New("rpc unavailable")
	header := snapshotHeader(10, "a")
	reader := &balanceReaderStub{headers: []tron.Header{header}, balanceErr: want}
	_, err := CollectSnapshot(context.Background(), reader,
		Asset{ID: "usdt-tron", Network: "tron-nile", ContractAddress: "contract"},
		[]Wallet{{
			ID: "123e4567-e89b-42d3-a456-426614174000", AssetID: "usdt-tron",
			Address: "411111111111111111111111111111111111111111", Role: "hot",
		}},
	)
	if !errors.Is(err, want) {
		t.Fatalf("CollectSnapshot() error = %v", err)
	}
}

type balanceReaderStub struct {
	headers    []tron.Header
	balances   map[string]string
	balanceErr error
	onBalance  func()
	readIndex  int
}

func (stub *balanceReaderStub) SolidifiedHead(context.Context) (tron.Header, error) {
	if stub.readIndex >= len(stub.headers) {
		return tron.Header{}, errors.New("unexpected head read")
	}
	header := stub.headers[stub.readIndex]
	stub.readIndex++
	return header, nil
}

func (stub *balanceReaderStub) TokenBalance(_ context.Context, _, ownerAddress string) (string, error) {
	if stub.balanceErr != nil {
		return "", stub.balanceErr
	}
	if stub.onBalance != nil {
		callback := stub.onBalance
		stub.onBalance = nil
		callback()
	}
	return stub.balances[ownerAddress], nil
}

func snapshotHeader(height uint64, hashCharacter string) tron.Header {
	return tron.Header{
		Height: height, Hash: strings.Repeat(hashCharacter, 64),
		Timestamp: time.Unix(1_700_000_000, 0).UTC(),
	}
}
