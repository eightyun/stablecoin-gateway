package deposit

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

type matcherStub struct {
	match  func(context.Context) (MatchResult, error)
	expire func(context.Context, int) (int64, error)
}

func (stub matcherStub) MatchNext(ctx context.Context) (MatchResult, error) {
	return stub.match(ctx)
}

func (stub matcherStub) ExpireDue(ctx context.Context, limit int) (int64, error) {
	return stub.expire(ctx, limit)
}

func TestWorkerMatchesEventsAndExpiresIntents(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	matchCalls := 0
	expireCalls := 0
	worker := newTestDepositWorker(t, matcherStub{
		match: func(context.Context) (MatchResult, error) {
			matchCalls++
			if matchCalls == 1 {
				return MatchResult{Processed: true, TransactionID: "tx", MatchStatus: "matched"}, nil
			}
			cancel()
			return MatchResult{}, nil
		},
		expire: func(_ context.Context, limit int) (int64, error) {
			expireCalls++
			if limit != 50 {
				t.Fatalf("ExpireDue() limit = %d", limit)
			}
			return 1, nil
		},
	})
	if err := worker.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if matchCalls != 2 || expireCalls != 1 {
		t.Fatalf("调用次数 match=%d expire=%d", matchCalls, expireCalls)
	}
}

func TestWorkerRetriesTransientMatchingError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	worker := newTestDepositWorker(t, matcherStub{
		match: func(context.Context) (MatchResult, error) {
			calls++
			if calls == 1 {
				return MatchResult{}, errors.New("数据库暂时不可用")
			}
			cancel()
			return MatchResult{}, nil
		},
		expire: func(context.Context, int) (int64, error) { return 0, nil },
	})
	if err := worker.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if calls != 2 {
		t.Fatalf("MatchNext() 调用次数 = %d", calls)
	}
}

func TestWorkerStopsOnInvalidExpiryLimit(t *testing.T) {
	worker := newTestDepositWorker(t, matcherStub{
		match:  func(context.Context) (MatchResult, error) { return MatchResult{}, nil },
		expire: func(context.Context, int) (int64, error) { return 0, ErrInvalidLimit },
	})
	if err := worker.Run(context.Background()); !errors.Is(err, ErrInvalidLimit) {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestMatchingOperationImmediatelyDrainsExpiryBacklog(t *testing.T) {
	expireCalls := 0
	operation := &matchingOperation{
		matcher: matcherStub{
			match: func(context.Context) (MatchResult, error) { return MatchResult{}, nil },
			expire: func(context.Context, int) (int64, error) {
				expireCalls++
				if expireCalls == 1 {
					return 50, nil
				}
				return 0, nil
			},
		},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)), expireInterval: time.Hour, expireBatchSize: 50,
	}
	if worked, err := operation.RunOnce(context.Background()); err != nil || !worked {
		t.Fatalf("首次 RunOnce() = %t, %v", worked, err)
	}
	if _, err := operation.RunOnce(context.Background()); err != nil {
		t.Fatalf("再次 RunOnce() error = %v", err)
	}
	if expireCalls != 2 {
		t.Fatalf("ExpireDue() 调用次数 = %d", expireCalls)
	}
}

func TestNewWorkerRejectsInvalidConfiguration(t *testing.T) {
	if _, err := NewWorker(nil, nil, WorkerConfig{}); !errors.Is(err, ErrInvalidWorker) {
		t.Fatalf("NewWorker() error = %v", err)
	}
}

func newTestDepositWorker(t *testing.T, matcher Matcher) *Worker {
	t.Helper()
	worker, err := NewWorker(matcher, slog.New(slog.NewTextHandler(io.Discard, nil)), WorkerConfig{
		OperationTimeout: time.Second,
		IdleInterval:     time.Millisecond,
		RetryMin:         time.Millisecond,
		RetryMax:         2 * time.Millisecond,
		ExpireInterval:   time.Hour,
		ExpireBatchSize:  50,
	})
	if err != nil {
		t.Fatalf("NewWorker() error = %v", err)
	}
	return worker
}
