package screening

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

type sweepQueueStub struct {
	claim          SweepClaim
	claimErr       error
	completedClaim SweepClaim
	completed      Result
	releasedClaim  SweepClaim
	releaseCode    string
}

func (stub *sweepQueueStub) Claim(context.Context, string, time.Duration) (SweepClaim, error) {
	return stub.claim, stub.claimErr
}

func (stub *sweepQueueStub) Complete(_ context.Context, claim SweepClaim, result Result) error {
	stub.completedClaim = claim
	stub.completed = result
	return nil
}

func (stub *sweepQueueStub) Release(_ context.Context, claim SweepClaim, code string) error {
	stub.releasedClaim = claim
	stub.releaseCode = code
	return nil
}

func TestSweepWorkerOperationPersistsResult(t *testing.T) {
	claim := SweepClaim{JobID: screeningTestPayoutID, WorkerID: "worker-1", LeaseEpoch: 1, Request: validScreeningRequest()}
	want := Result{Provider: "provider", Decision: DecisionAllow}
	queue := &sweepQueueStub{claim: claim}
	operation := sweepWorkerOperation{
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

func TestSweepWorkerOperationReleasesProviderFailure(t *testing.T) {
	claim := SweepClaim{JobID: screeningTestPayoutID, WorkerID: "worker-1", LeaseEpoch: 1}
	want := errors.New("provider unavailable")
	queue := &sweepQueueStub{claim: claim}
	operation := sweepWorkerOperation{
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

func TestSweepWorkerOperationIdlesWithoutJobs(t *testing.T) {
	operation := sweepWorkerOperation{
		queue: &sweepQueueStub{claimErr: ErrNoSweepScreeningJob},
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
