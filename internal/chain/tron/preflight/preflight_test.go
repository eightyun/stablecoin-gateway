package preflight

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/chain/tron"
)

type fullNodeStub struct {
	head     tron.Header
	metadata tron.TokenMetadata
}

func (stub fullNodeStub) Head(context.Context) (tron.Header, error) {
	return stub.head, nil
}

func (stub fullNodeStub) TokenMetadata(context.Context, string) (tron.TokenMetadata, error) {
	return stub.metadata, nil
}

type solidityNodeStub struct {
	head tron.Header
}

func (stub solidityNodeStub) SolidifiedHead(context.Context) (tron.Header, error) {
	return stub.head, nil
}

func TestCheckAcceptsHealthyTestnet(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	result, err := Check(context.Background(), fullNodeStub{
		head:     tron.Header{Height: 100, Timestamp: now.Add(-3 * time.Second)},
		metadata: tron.TokenMetadata{Symbol: "USDT", Decimals: 6},
	}, solidityNodeStub{head: tron.Header{Height: 81, Timestamp: now.Add(-60 * time.Second)}}, testConfig(), now)
	if err != nil || result.FinalizedLag != 19 || result.Symbol != "USDT" {
		t.Fatalf("Check() = %+v, %v", result, err)
	}
}

func TestCheckRejectsWrongAssetAndExcessiveLag(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	tests := []struct {
		name     string
		full     fullNodeStub
		solidity solidityNodeStub
	}{
		{
			name: "错误资产",
			full: fullNodeStub{head: tron.Header{Height: 100, Timestamp: now},
				metadata: tron.TokenMetadata{Symbol: "FAKE", Decimals: 6}},
			solidity: solidityNodeStub{head: tron.Header{Height: 81, Timestamp: now.Add(-time.Minute)}},
		},
		{
			name: "固化落后",
			full: fullNodeStub{head: tron.Header{Height: 200, Timestamp: now},
				metadata: tron.TokenMetadata{Symbol: "USDT", Decimals: 6}},
			solidity: solidityNodeStub{head: tron.Header{Height: 1, Timestamp: now.Add(-time.Minute)}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Check(context.Background(), test.full, test.solidity, testConfig(), now)
			if !errors.Is(err, ErrCheckFailed) {
				t.Fatalf("Check() error = %v", err)
			}
		})
	}
}

func testConfig() Config {
	return Config{
		Network: "tron-nile", ContractAddress: "TXYZopYRdj2D9XRtbG411XZZ3kM5VkAeBf",
		ExpectedSymbol: "USDT", ExpectedDecimals: 6,
		MaxFinalizedLag: 64, MaxHeadAge: 2 * time.Minute, MaxFutureSkew: 30 * time.Second,
	}
}
