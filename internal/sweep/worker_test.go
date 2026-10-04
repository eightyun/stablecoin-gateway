package sweep

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

func TestWorkerPlansCandidate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	planner := &plannerStub{plan: Plan{ID: "plan-1", AssetID: "asset-1", Amount: "100"}, cancel: cancel}
	worker, err := NewWorker(planner, Policy{
		AssetID: "asset-1", MinimumAmount: "100", MaxSnapshotAge: time.Minute,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), WorkerConfig{
		OperationTimeout: time.Second, IdleInterval: time.Millisecond,
		RetryMin: time.Millisecond, RetryMax: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewWorker() error = %v", err)
	}
	if err := worker.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if planner.calls != 1 {
		t.Fatalf("PlanNext() calls = %d", planner.calls)
	}
}

func TestNewWorkerRejectsInvalidPolicy(t *testing.T) {
	_, err := NewWorker(&plannerStub{}, Policy{}, slog.Default(), WorkerConfig{
		OperationTimeout: time.Second, IdleInterval: time.Second,
		RetryMin: time.Second, RetryMax: time.Second,
	})
	if err == nil {
		t.Fatal("NewWorker() 未拒绝无效策略")
	}
}

type plannerStub struct {
	plan   Plan
	err    error
	cancel context.CancelFunc
	calls  int
}

func (stub *plannerStub) PlanNext(context.Context, Policy) (Plan, error) {
	stub.calls++
	if stub.cancel != nil {
		stub.cancel()
	}
	return stub.plan, stub.err
}
