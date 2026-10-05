package sweep

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

func (stub *executionQueueStub) ClaimBroadcast(
	context.Context, string, string, time.Duration,
) (BroadcastClaim, error) {
	return stub.broadcastClaim, stub.broadcastErr
}

func (stub *executionQueueStub) BeginConfirmation(
	_ context.Context, _ BroadcastClaim, result, code string,
) error {
	stub.beginResult, stub.beginFailureCode = result, code
	return nil
}

func (stub *executionQueueStub) ClaimConfirmation(
	context.Context, string, string, time.Duration,
) (ConfirmationClaim, error) {
	return stub.confirmationClaim, stub.confirmationErr
}

func (stub *executionQueueStub) ReleaseConfirmation(
	_ context.Context, _ ConfirmationClaim, _ time.Duration, code string,
) error {
	stub.releasedCode = code
	return nil
}

func (stub *executionQueueStub) CompleteConfirmationSuccess(context.Context, ConfirmationClaim) error {
	stub.succeeded = true
	return nil
}

func (stub *executionQueueStub) CompleteConfirmationFailure(
	_ context.Context, _ ConfirmationClaim, reason string,
) error {
	stub.failedReason = reason
	return nil
}

type sweepBroadcasterFunc func(context.Context, tron.SignedTransaction) error

func (function sweepBroadcasterFunc) Broadcast(ctx context.Context, transaction tron.SignedTransaction) error {
	return function(ctx, transaction)
}

type sweepTransactionReaderStub struct {
	state tron.TransactionState
	head  tron.Header
	err   error
}

func (stub sweepTransactionReaderStub) Transaction(context.Context, string) (tron.TransactionState, error) {
	return stub.state, stub.err
}

func (stub sweepTransactionReaderStub) SolidifiedHead(context.Context) (tron.Header, error) {
	return stub.head, stub.err
}

func TestSweepExecutionMovesUnknownBroadcastToConfirmation(t *testing.T) {
	queue := &executionQueueStub{
		broadcastClaim: BroadcastClaim{PlanID: "plan-1"}, confirmationErr: ErrNoConfirmationJob,
	}
	operation := newSweepExecutionOperationForTest(queue, sweepBroadcasterFunc(
		func(context.Context, tron.SignedTransaction) error { return errors.New("response lost") },
	), sweepTransactionReaderStub{})
	worked, err := operation.RunOnce(context.Background())
	if err != nil || !worked || queue.beginResult != "unknown" || queue.beginFailureCode != "broadcast_error" {
		t.Fatalf("RunOnce() = %v, %v; queue=%+v", worked, err, queue)
	}
}

func TestSweepExecutionFinalizesOnlySolidifiedSuccess(t *testing.T) {
	queue := &executionQueueStub{
		broadcastErr: ErrNoBroadcastJob,
		confirmationClaim: ConfirmationClaim{
			PlanID: "plan-1", Transaction: tron.SignedTransaction{ID: "tx"},
		},
	}
	operation := newSweepExecutionOperationForTest(queue, sweepBroadcasterFunc(
		func(context.Context, tron.SignedTransaction) error {
			t.Fatal("固化成功不应重新广播")
			return nil
		},
	), sweepTransactionReaderStub{state: tron.TransactionState{
		Status: tron.TransactionSucceeded, Solidified: true,
	}})
	worked, err := operation.RunOnce(context.Background())
	if err != nil || !worked || !queue.succeeded {
		t.Fatalf("RunOnce() = %v, %v; queue=%+v", worked, err, queue)
	}
}

func TestSweepExecutionRecordsSolidifiedFailure(t *testing.T) {
	queue := &executionQueueStub{
		broadcastErr: ErrNoBroadcastJob,
		confirmationClaim: ConfirmationClaim{
			PlanID: "plan-1", Transaction: tron.SignedTransaction{ID: "tx"},
		},
	}
	operation := newSweepExecutionOperationForTest(queue, sweepBroadcasterFunc(
		func(context.Context, tron.SignedTransaction) error { return nil },
	), sweepTransactionReaderStub{state: tron.TransactionState{
		Status: tron.TransactionFailed, Solidified: true,
	}})
	worked, err := operation.RunOnce(context.Background())
	if err != nil || !worked || queue.failedReason != "execution_failed" {
		t.Fatalf("RunOnce() = %v, %v; queue=%+v", worked, err, queue)
	}
}

func TestSweepExecutionRebroadcastsSameTransactionBeforeExpiration(t *testing.T) {
	expiresAt := time.Unix(200, 0).UTC()
	transaction := tron.SignedTransaction{ID: "same-tx", Payload: []byte("same-payload")}
	queue := &executionQueueStub{
		broadcastErr:      ErrNoBroadcastJob,
		confirmationClaim: ConfirmationClaim{PlanID: "plan-1", ExpiresAt: expiresAt, Transaction: transaction},
	}
	var broadcasted tron.SignedTransaction
	operation := newSweepExecutionOperationForTest(queue, sweepBroadcasterFunc(
		func(_ context.Context, value tron.SignedTransaction) error {
			broadcasted = value
			return nil
		},
	), sweepTransactionReaderStub{
		state: tron.TransactionState{Status: tron.TransactionNotFound},
		head:  tron.Header{Timestamp: expiresAt.Add(-time.Second)},
	})
	worked, err := operation.RunOnce(context.Background())
	if err != nil || !worked || broadcasted.ID != transaction.ID || string(broadcasted.Payload) != string(transaction.Payload) {
		t.Fatalf("RunOnce() = %v, %v; broadcasted=%+v", worked, err, broadcasted)
	}
}

func TestSweepExecutionFailsAfterSolidifiedHeadPassesExpiration(t *testing.T) {
	expiresAt := time.Unix(100, 0).UTC()
	queue := &executionQueueStub{
		broadcastErr: ErrNoBroadcastJob,
		confirmationClaim: ConfirmationClaim{PlanID: "plan-1", ExpiresAt: expiresAt,
			Transaction: tron.SignedTransaction{ID: "tx"}},
	}
	operation := newSweepExecutionOperationForTest(queue, sweepBroadcasterFunc(
		func(context.Context, tron.SignedTransaction) error {
			t.Fatal("交易安全过期后不应重新广播")
			return nil
		},
	), sweepTransactionReaderStub{
		state: tron.TransactionState{Status: tron.TransactionNotFound},
		head:  tron.Header{Timestamp: expiresAt.Add(time.Second)},
	})
	worked, err := operation.RunOnce(context.Background())
	if err != nil || !worked || queue.failedReason != "expired_not_found" {
		t.Fatalf("RunOnce() = %v, %v; queue=%+v", worked, err, queue)
	}
}

func newSweepExecutionOperationForTest(
	queue ExecutionQueue,
	broadcaster tron.Broadcaster,
	reader tron.FinalizedTransactionReader,
) *executionOperation {
	return &executionOperation{
		queue: queue, broadcaster: broadcaster, reader: reader,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)), workerID: "worker-1",
		network: "tron-nile", leaseDuration: time.Minute, confirmationInterval: time.Second,
	}
}
