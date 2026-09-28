package screening

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

type queueStub struct {
	claim          Claim
	claimErr       error
	completedClaim Claim
	completed      Result
	releasedClaim  Claim
	releaseCode    string
}

func (stub *queueStub) Claim(context.Context, string, time.Duration) (Claim, error) {
	return stub.claim, stub.claimErr
}

func (stub *queueStub) Complete(_ context.Context, claim Claim, result Result) error {
	stub.completedClaim = claim
	stub.completed = result
	return nil
}

func (stub *queueStub) Release(_ context.Context, claim Claim, code string) error {
	stub.releasedClaim = claim
	stub.releaseCode = code
	return nil
}

type providerFunc func(context.Context, Request) (Result, error)

func (function providerFunc) Screen(ctx context.Context, request Request) (Result, error) {
	return function(ctx, request)
}

func TestWorkerOperationPersistsResult(t *testing.T) {
	claim := Claim{PayoutID: screeningTestPayoutID, WorkerID: "worker-1", LeaseEpoch: 1, Request: validScreeningRequest()}
	want := Result{Provider: "provider", Decision: DecisionAllow}
	queue := &queueStub{claim: claim}
	operation := workerOperation{
		queue: queue,
		provider: providerFunc(func(_ context.Context, request Request) (Result, error) {
			if request.RequestID != claim.Request.RequestID {
				t.Fatalf("Screen() request = %+v", request)
			}
			return want, nil
		}),
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)), workerID: "worker-1", leaseDuration: time.Minute,
	}
	worked, err := operation.RunOnce(context.Background())
	if err != nil || !worked || queue.completedClaim.PayoutID != claim.PayoutID || queue.completed.Decision != DecisionAllow {
		t.Fatalf("RunOnce() = %v, %v queue=%+v", worked, err, queue)
	}
}

func TestWorkerOperationReleasesProviderFailure(t *testing.T) {
	claim := Claim{PayoutID: screeningTestPayoutID, WorkerID: "worker-1", LeaseEpoch: 1}
	want := errors.New("provider unavailable")
	queue := &queueStub{claim: claim}
	operation := workerOperation{
		queue: queue,
		provider: providerFunc(func(context.Context, Request) (Result, error) {
			return Result{}, want
		}),
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)), workerID: "worker-1", leaseDuration: time.Minute,
	}
	worked, err := operation.RunOnce(context.Background())
	if !worked || !errors.Is(err, want) || queue.releasedClaim.PayoutID != claim.PayoutID || queue.releaseCode != "provider_error" {
		t.Fatalf("RunOnce() = %v, %v queue=%+v", worked, err, queue)
	}
}

func TestWorkerOperationIdlesWithoutJobs(t *testing.T) {
	operation := workerOperation{
		queue: &queueStub{claimErr: ErrNoScreeningJob},
		provider: providerFunc(func(context.Context, Request) (Result, error) {
			t.Fatal("空队列不应调用 Provider")
			return Result{}, nil
		}),
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)), workerID: "worker-1", leaseDuration: time.Minute,
	}
	worked, err := operation.RunOnce(context.Background())
	if err != nil || worked {
		t.Fatalf("RunOnce() = %v, %v", worked, err)
	}
}

func TestNewWorkerRejectsInvalidConfiguration(t *testing.T) {
	if _, err := NewWorker(nil, nil, nil, "", WorkerConfig{}); !errors.Is(err, ErrInvalidWorker) {
		t.Fatalf("NewWorker() error = %v", err)
	}
}
