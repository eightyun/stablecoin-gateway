package screening

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

type inboundQueueStub struct {
	claim          InboundClaim
	claimErr       error
	completedClaim InboundClaim
	completed      Result
	releasedClaim  InboundClaim
	releaseCode    string
}

func (stub *inboundQueueStub) Claim(context.Context, string, time.Duration) (InboundClaim, error) {
	return stub.claim, stub.claimErr
}

func (stub *inboundQueueStub) Complete(_ context.Context, claim InboundClaim, result Result) error {
	stub.completedClaim = claim
	stub.completed = result
	return nil
}

func (stub *inboundQueueStub) Release(_ context.Context, claim InboundClaim, code string) error {
	stub.releasedClaim = claim
	stub.releaseCode = code
	return nil
}

func TestInboundWorkerOperationPersistsResult(t *testing.T) {
	claim := InboundClaim{JobID: screeningTestPayoutID, WorkerID: "worker-1", LeaseEpoch: 1, Request: validScreeningRequest()}
	want := Result{Provider: "provider", Decision: DecisionAllow}
	queue := &inboundQueueStub{claim: claim}
	operation := inboundWorkerOperation{
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
	if err != nil || !worked || queue.completedClaim.JobID != claim.JobID || queue.completed.Decision != DecisionAllow {
		t.Fatalf("RunOnce() = %v, %v queue=%+v", worked, err, queue)
	}
}

func TestInboundWorkerOperationReleasesProviderFailure(t *testing.T) {
	claim := InboundClaim{JobID: screeningTestPayoutID, WorkerID: "worker-1", LeaseEpoch: 1}
	want := errors.New("provider unavailable")
	queue := &inboundQueueStub{claim: claim}
	operation := inboundWorkerOperation{
		queue: queue,
		provider: providerFunc(func(context.Context, Request) (Result, error) {
			return Result{}, want
		}),
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)), workerID: "worker-1", leaseDuration: time.Minute,
	}
	worked, err := operation.RunOnce(context.Background())
	if !worked || !errors.Is(err, want) || queue.releasedClaim.JobID != claim.JobID || queue.releaseCode != "provider_error" {
		t.Fatalf("RunOnce() = %v, %v queue=%+v", worked, err, queue)
	}
}

func TestInboundWorkerOperationIdlesWithoutJobs(t *testing.T) {
	operation := inboundWorkerOperation{
		queue: &inboundQueueStub{claimErr: ErrNoInboundScreeningJob},
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
