package background

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

func TestRunnerContinuesAfterWorkAndRetriesErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	runner := newTestRunner(t, OperationFunc(func(context.Context) (bool, error) {
		calls++
		switch calls {
		case 1:
			return true, nil
		case 2:
			return false, errors.New("暂时失败")
		default:
			cancel()
			return false, nil
		}
	}), nil)
	if err := runner.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if calls != 3 {
		t.Fatalf("RunOnce() 调用次数 = %d", calls)
	}
}

func TestRunnerStopsOnClassifiedError(t *testing.T) {
	permanent := errors.New("永久错误")
	runner := newTestRunner(t, OperationFunc(func(context.Context) (bool, error) {
		return false, permanent
	}), func(error) Decision { return Stop })
	if err := runner.Run(context.Background()); !errors.Is(err, permanent) {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestRunnerAppliesOperationTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	runner, err := NewRunner(OperationFunc(func(operationCtx context.Context) (bool, error) {
		calls++
		if calls == 1 {
			if _, exists := operationCtx.Deadline(); !exists {
				t.Fatal("任务上下文缺少截止时间")
			}
			<-operationCtx.Done()
			return false, operationCtx.Err()
		}
		cancel()
		return false, nil
	}), discardLogger(), Config{
		Name: "test", OperationTimeout: 5 * time.Millisecond, IdleInterval: time.Millisecond,
		RetryMin: time.Millisecond, RetryMax: time.Millisecond,
	}, nil)
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	if err := runner.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if calls != 2 {
		t.Fatalf("RunOnce() 调用次数 = %d", calls)
	}
}

func TestNewRunnerRejectsInvalidConfiguration(t *testing.T) {
	if _, err := NewRunner(nil, nil, Config{}, nil); !errors.Is(err, ErrInvalidRunner) {
		t.Fatalf("NewRunner() error = %v", err)
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

func newTestRunner(t *testing.T, operation Operation, classifier Classifier) *Runner {
	t.Helper()
	runner, err := NewRunner(operation, discardLogger(), Config{
		Name: "test", OperationTimeout: time.Second, IdleInterval: time.Millisecond,
		RetryMin: time.Millisecond, RetryMax: 2 * time.Millisecond,
	}, classifier)
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	return runner
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
