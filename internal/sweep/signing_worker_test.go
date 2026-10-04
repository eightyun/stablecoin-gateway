package sweep

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/chain/tron"
)

type sweepSigningQueueStub struct {
	claim       SigningClaim
	claimErr    error
	completed   bool
	releaseCode string
}

func (stub *sweepSigningQueueStub) ClaimSigning(context.Context, string, string, time.Duration) (SigningClaim, error) {
	return stub.claim, stub.claimErr
}

func (stub *sweepSigningQueueStub) CompleteSigning(
	context.Context, SigningClaim, BalanceObservation, tron.SignedTransaction,
) error {
	stub.completed = true
	return nil
}

func (stub *sweepSigningQueueStub) ReleaseSigning(_ context.Context, _ SigningClaim, code string) error {
	stub.releaseCode = code
	return nil
}

type sweepBalanceReaderStub struct {
	headers  []tron.Header
	balance  string
	position int
}

func (stub *sweepBalanceReaderStub) SolidifiedHead(context.Context) (tron.Header, error) {
	header := stub.headers[stub.position]
	stub.position++
	return header, nil
}

func (stub *sweepBalanceReaderStub) TokenBalance(context.Context, string, string) (string, error) {
	return stub.balance, nil
}

type sweepSignerFunc func(context.Context, tron.SweepSignRequest) (tron.SignedTransaction, error)

func (function sweepSignerFunc) SignSweep(ctx context.Context, request tron.SweepSignRequest) (tron.SignedTransaction, error) {
	return function(ctx, request)
}

func TestSweepSigningOperationPersistsResultAfterBalancePreflight(t *testing.T) {
	claim := signingWorkerClaim()
	queue := &sweepSigningQueueStub{claim: claim}
	header := tron.Header{Height: 12, Hash: strings.Repeat("a", 64), Timestamp: time.Now().UTC()}
	operation := sweepSigningOperation{
		queue: queue, reader: &sweepBalanceReaderStub{headers: []tron.Header{header, header}, balance: "150"},
		signer: sweepSignerFunc(func(_ context.Context, request tron.SweepSignRequest) (tron.SignedTransaction, error) {
			if request.RequestID != claim.PlanID || request.SourceAddress != claim.SourceAddress || request.Amount != "100" {
				t.Fatalf("SignSweep() request = %+v", request)
			}
			return tron.SignedTransaction{ID: strings.Repeat("b", 64), Payload: []byte("signed")}, nil
		}),
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)), workerID: "worker-1",
		network: "tron-nile", leaseDuration: time.Minute,
	}
	worked, err := operation.RunOnce(context.Background())
	if err != nil || !worked || !queue.completed || queue.releaseCode != "" {
		t.Fatalf("RunOnce() = %v, %v; queue=%+v", worked, err, queue)
	}
}

func TestSweepSigningOperationReleasesInsufficientBalance(t *testing.T) {
	claim := signingWorkerClaim()
	queue := &sweepSigningQueueStub{claim: claim}
	header := tron.Header{Height: 12, Hash: strings.Repeat("a", 64), Timestamp: time.Now().UTC()}
	operation := sweepSigningOperation{
		queue: queue, reader: &sweepBalanceReaderStub{headers: []tron.Header{header, header}, balance: "99"},
		signer: sweepSignerFunc(func(context.Context, tron.SweepSignRequest) (tron.SignedTransaction, error) {
			t.Fatal("余额不足时不应调用 signer")
			return tron.SignedTransaction{}, nil
		}),
		logger: slog.Default(), workerID: "worker-1", network: "tron-nile", leaseDuration: time.Minute,
	}
	worked, err := operation.RunOnce(context.Background())
	if !worked || !errors.Is(err, ErrInsufficientSourceBalance) || queue.releaseCode != "insufficient_source_balance" {
		t.Fatalf("RunOnce() = %v, %v; queue=%+v", worked, err, queue)
	}
}

func signingWorkerClaim() SigningClaim {
	return SigningClaim{
		PlanID: "123e4567-e89b-42d3-a456-426614174000", WorkerID: "worker-1", LeaseEpoch: 1,
		Network: "tron-nile", SourceAddress: "source", ContractAddress: "contract",
		DestinationAddress: "destination", Amount: "100",
	}
}
