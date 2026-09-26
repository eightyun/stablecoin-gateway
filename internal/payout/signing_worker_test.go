package payout

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/chain/tron"
)

type signingQueueStub struct {
	claim                SigningClaim
	claimErr             error
	completedClaim       SigningClaim
	completedTransaction tron.SignedTransaction
	releasedClaim        SigningClaim
	releaseCode          string
}

func (stub *signingQueueStub) ClaimSigning(context.Context, string, time.Duration) (SigningClaim, error) {
	return stub.claim, stub.claimErr
}

func (stub *signingQueueStub) CompleteSigning(_ context.Context, claim SigningClaim, transaction tron.SignedTransaction) error {
	stub.completedClaim = claim
	stub.completedTransaction = transaction
	return nil
}

func (stub *signingQueueStub) ReleaseSigning(_ context.Context, claim SigningClaim, code string) error {
	stub.releasedClaim = claim
	stub.releaseCode = code
	return nil
}

type transferSignerFunc func(context.Context, tron.TransferSignRequest) (tron.SignedTransaction, error)

func (function transferSignerFunc) SignTransfer(ctx context.Context, request tron.TransferSignRequest) (tron.SignedTransaction, error) {
	return function(ctx, request)
}

func TestSigningOperationPersistsSignerResult(t *testing.T) {
	claim := SigningClaim{
		PayoutID: "123e4567-e89b-42d3-a456-426614174000", WorkerID: "worker-1", LeaseEpoch: 1,
		Network: "tron-nile", ContractAddress: "contract", DestinationAddress: "destination", Amount: "100",
	}
	queue := &signingQueueStub{claim: claim}
	want := tron.SignedTransaction{
		ID:      "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Payload: []byte("signed"),
	}
	operation := signingOperation{
		queue: queue,
		signer: transferSignerFunc(func(_ context.Context, request tron.TransferSignRequest) (tron.SignedTransaction, error) {
			if request.RequestID != claim.PayoutID || request.Network != claim.Network ||
				request.ContractAddress != claim.ContractAddress || request.DestinationAddress != claim.DestinationAddress ||
				request.Amount != claim.Amount {
				t.Fatalf("签名请求 = %+v", request)
			}
			return want, nil
		}),
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)), workerID: "worker-1", leaseDuration: time.Minute,
	}
	worked, err := operation.RunOnce(context.Background())
	if err != nil || !worked || queue.completedClaim != claim || queue.completedTransaction.ID != want.ID {
		t.Fatalf("RunOnce() = %v, %v; queue=%+v", worked, err, queue)
	}
}

func TestSigningOperationReleasesFailedSignerClaim(t *testing.T) {
	claim := SigningClaim{PayoutID: "123e4567-e89b-42d3-a456-426614174000", WorkerID: "worker-1", LeaseEpoch: 1}
	queue := &signingQueueStub{claim: claim}
	signerErr := errors.New("temporary signer failure")
	operation := signingOperation{
		queue: queue,
		signer: transferSignerFunc(func(context.Context, tron.TransferSignRequest) (tron.SignedTransaction, error) {
			return tron.SignedTransaction{}, signerErr
		}),
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)), workerID: "worker-1", leaseDuration: time.Minute,
	}
	worked, err := operation.RunOnce(context.Background())
	if !worked || !errors.Is(err, signerErr) || queue.releasedClaim != claim || queue.releaseCode != "signer_error" {
		t.Fatalf("RunOnce() = %v, %v; queue=%+v", worked, err, queue)
	}
}

func TestSigningOperationIdlesWithoutJobs(t *testing.T) {
	operation := signingOperation{
		queue: &signingQueueStub{claimErr: ErrNoSigningJob},
		signer: transferSignerFunc(func(context.Context, tron.TransferSignRequest) (tron.SignedTransaction, error) {
			t.Fatal("空队列不应调用签名器")
			return tron.SignedTransaction{}, nil
		}),
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)), workerID: "worker-1", leaseDuration: time.Minute,
	}
	worked, err := operation.RunOnce(context.Background())
	if err != nil || worked {
		t.Fatalf("RunOnce() = %v, %v", worked, err)
	}
}

func TestNewSigningWorkerRejectsInvalidConfiguration(t *testing.T) {
	if _, err := NewSigningWorker(nil, nil, nil, "", SigningWorkerConfig{}); !errors.Is(err, ErrInvalidSigningWorker) {
		t.Fatalf("NewSigningWorker() error = %v", err)
	}
}
