package indexer

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

type stepperFunc func(context.Context) (StepResult, error)

func (function stepperFunc) Step(ctx context.Context) (StepResult, error) {
	return function(ctx)
}

func TestWorkerContinuesWhileCatchingUp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	worker := newTestWorker(t, stepperFunc(func(context.Context) (StepResult, error) {
		calls++
		if calls < 3 {
			return StepResult{Processed: true, Height: uint64(calls)}, nil
		}
		cancel()
		return StepResult{}, nil
	}))
	if err := worker.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if calls != 3 {
		t.Fatalf("Step() 调用次数 = %d", calls)
	}
}

func TestWorkerRetriesTransientError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	transient := errors.New("节点暂时不可用")
	worker := newTestWorker(t, stepperFunc(func(context.Context) (StepResult, error) {
		calls++
		if calls == 1 {
			return StepResult{}, transient
		}
		cancel()
		return StepResult{}, nil
	}))
	if err := worker.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if calls != 2 {
		t.Fatalf("Step() 调用次数 = %d", calls)
	}
}

func TestWorkerStopsOnPermanentError(t *testing.T) {
	worker := newTestWorker(t, stepperFunc(func(context.Context) (StepResult, error) {
		return StepResult{}, ErrEventConflict
	}))
	err := worker.Run(context.Background())
	if !errors.Is(err, ErrEventConflict) {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestWorkerAppliesStepTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	stepper := stepperFunc(func(stepCtx context.Context) (StepResult, error) {
		calls++
		if calls == 1 {
			if _, exists := stepCtx.Deadline(); !exists {
				t.Fatal("Step() 上下文缺少截止时间")
			}
			<-stepCtx.Done()
			return StepResult{}, stepCtx.Err()
		}
		cancel()
		return StepResult{}, nil
	})
	worker, err := NewWorker(stepper, slog.New(slog.NewTextHandler(io.Discard, nil)), WorkerConfig{
		StepTimeout:  5 * time.Millisecond,
		IdleInterval: time.Millisecond,
		RetryMin:     time.Millisecond,
		RetryMax:     time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewWorker() error = %v", err)
	}
	if err := worker.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if calls != 2 {
		t.Fatalf("Step() 调用次数 = %d", calls)
	}
}

func TestNewWorkerRejectsInvalidConfiguration(t *testing.T) {
	_, err := NewWorker(nil, nil, WorkerConfig{})
	if !errors.Is(err, ErrInvalidWorker) {
		t.Fatalf("NewWorker() error = %v", err)
	}
}

func TestRetryHelpers(t *testing.T) {
	if got := nextRetry(4*time.Second, 5*time.Second); got != 5*time.Second {
		t.Fatalf("nextRetry() = %v", got)
	}
	for range 100 {
		got := jitter(10 * time.Second)
		if got < 8*time.Second || got > 12*time.Second {
			t.Fatalf("jitter() = %v", got)
		}
	}
}

func newTestWorker(t *testing.T, stepper Stepper) *Worker {
	t.Helper()
	worker, err := NewWorker(stepper, slog.New(slog.NewTextHandler(io.Discard, nil)), WorkerConfig{
		StepTimeout:  time.Second,
		IdleInterval: time.Millisecond,
		RetryMin:     time.Millisecond,
		RetryMax:     2 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewWorker() error = %v", err)
	}
	return worker
}
