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

type executionQueueStub struct {
	broadcastClaim    BroadcastClaim
	broadcastErr      error
	confirmationClaim ConfirmationClaim
	confirmationErr   error
	beginResult       string
	beginFailureCode  string
	releasedCode      string
	succeeded         bool
	failedReason      string
}

func (stub *executionQueueStub) ClaimBroadcast(context.Context, string, time.Duration) (BroadcastClaim, error) {
	return stub.broadcastClaim, stub.broadcastErr
}
func (stub *executionQueueStub) BeginConfirmation(_ context.Context, _ BroadcastClaim, result, code string) error {
	stub.beginResult, stub.beginFailureCode = result, code
	return nil
}
func (stub *executionQueueStub) ClaimConfirmation(context.Context, string, time.Duration) (ConfirmationClaim, error) {
	return stub.confirmationClaim, stub.confirmationErr
}
func (stub *executionQueueStub) ReleaseConfirmation(_ context.Context, _ ConfirmationClaim, _ time.Duration, code string) error {
	stub.releasedCode = code
	return nil
}
func (stub *executionQueueStub) CompleteConfirmationSuccess(context.Context, ConfirmationClaim) error {
	stub.succeeded = true
	return nil
}
func (stub *executionQueueStub) CompleteConfirmationFailure(_ context.Context, _ ConfirmationClaim, reason string) error {
	stub.failedReason = reason
	return nil
}

type broadcasterFunc func(context.Context, tron.SignedTransaction) error

func (function broadcasterFunc) Broadcast(ctx context.Context, transaction tron.SignedTransaction) error {
	return function(ctx, transaction)
}

type transactionReaderStub struct {
	state tron.TransactionState
	head  tron.Header
	err   error
}

func (stub transactionReaderStub) Transaction(context.Context, string) (tron.TransactionState, error) {
	return stub.state, stub.err
}
func (stub transactionReaderStub) SolidifiedHead(context.Context) (tron.Header, error) {
	return stub.head, stub.err
}

func TestExecutionOperationMovesUnknownBroadcastToConfirmation(t *testing.T) {
	broadcastErr := errors.New("response lost")
	queue := &executionQueueStub{broadcastClaim: BroadcastClaim{PayoutID: "payout-1"}, confirmationErr: ErrNoConfirmationJob}
	operation := newExecutionOperationForTest(queue, broadcasterFunc(func(context.Context, tron.SignedTransaction) error {
		return broadcastErr
	}), transactionReaderStub{})
	worked, err := operation.RunOnce(context.Background())
	if err != nil || !worked || queue.beginResult != "unknown" || queue.beginFailureCode != "broadcast_error" {
		t.Fatalf("RunOnce() = %v, %v; queue=%+v", worked, err, queue)
	}
}

func TestExecutionOperationAlternatesBroadcastAndConfirmation(t *testing.T) {
	queue := &executionQueueStub{
		broadcastClaim: BroadcastClaim{PayoutID: "payout-broadcast"},
		confirmationClaim: ConfirmationClaim{
			PayoutID: "payout-confirm", Transaction: tron.SignedTransaction{ID: "tx"},
		},
	}
	broadcasts := 0
	operation := newExecutionOperationForTest(queue, broadcasterFunc(func(context.Context, tron.SignedTransaction) error {
		broadcasts++
		return nil
	}), transactionReaderStub{state: tron.TransactionState{Status: tron.TransactionSucceeded, Solidified: true}})
	if worked, err := operation.RunOnce(context.Background()); err != nil || !worked {
		t.Fatalf("第一次 RunOnce() = %v, %v", worked, err)
	}
	if worked, err := operation.RunOnce(context.Background()); err != nil || !worked {
		t.Fatalf("第二次 RunOnce() = %v, %v", worked, err)
	}
	if broadcasts != 1 || !queue.succeeded {
		t.Fatalf("broadcasts=%d, succeeded=%v", broadcasts, queue.succeeded)
	}
}

func TestExecutionOperationFinalizesSolidifiedSuccess(t *testing.T) {
	queue := &executionQueueStub{
		broadcastErr:      ErrNoBroadcastJob,
		confirmationClaim: ConfirmationClaim{PayoutID: "payout-1", Transaction: tron.SignedTransaction{ID: "tx"}},
	}
	operation := newExecutionOperationForTest(queue, broadcasterFunc(func(context.Context, tron.SignedTransaction) error {
		t.Fatal("固化成功不应重新广播")
		return nil
	}), transactionReaderStub{state: tron.TransactionState{Status: tron.TransactionSucceeded, Solidified: true}})
	worked, err := operation.RunOnce(context.Background())
	if err != nil || !worked || !queue.succeeded {
		t.Fatalf("RunOnce() = %v, %v; queue=%+v", worked, err, queue)
	}
}

func TestExecutionOperationFailsOnlyAfterSolidifiedHeadPassesExpiration(t *testing.T) {
	expiresAt := time.Unix(100, 0).UTC()
	queue := &executionQueueStub{
		broadcastErr: ErrNoBroadcastJob,
		confirmationClaim: ConfirmationClaim{PayoutID: "payout-1", ExpiresAt: expiresAt,
			Transaction: tron.SignedTransaction{ID: "tx"}},
	}
	operation := newExecutionOperationForTest(queue, broadcasterFunc(func(context.Context, tron.SignedTransaction) error {
		t.Fatal("交易安全过期后不应重新广播")
		return nil
	}), transactionReaderStub{
		state: tron.TransactionState{Status: tron.TransactionNotFound},
		head:  tron.Header{Timestamp: expiresAt.Add(time.Second)},
	})
	worked, err := operation.RunOnce(context.Background())
	if err != nil || !worked || queue.failedReason != "expired_not_found" {
		t.Fatalf("RunOnce() = %v, %v; queue=%+v", worked, err, queue)
	}
}

func newExecutionOperationForTest(
	queue ExecutionQueue,
	broadcaster tron.Broadcaster,
	reader tron.FinalizedTransactionReader,
) *executionOperation {
	return &executionOperation{
		queue: queue, broadcaster: broadcaster, reader: reader,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)), workerID: "worker-1",
		leaseDuration: time.Minute, confirmationInterval: time.Second,
	}
}
