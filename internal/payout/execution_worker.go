package payout

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/background"
	"github.com/eightyun/stablecoin-gateway/internal/chain/tron"
)

var ErrInvalidExecutionWorker = errors.New("出款执行 Worker 配置无效")

// ExecutionQueue 定义广播和固化确认需要的持久化状态机。
type ExecutionQueue interface {
	ClaimBroadcast(context.Context, string, time.Duration) (BroadcastClaim, error)
	BeginConfirmation(context.Context, BroadcastClaim, string, string) error
	ClaimConfirmation(context.Context, string, time.Duration) (ConfirmationClaim, error)
	ReleaseConfirmation(context.Context, ConfirmationClaim, time.Duration, string) error
	CompleteConfirmationSuccess(context.Context, ConfirmationClaim) error
	CompleteConfirmationFailure(context.Context, ConfirmationClaim, string) error
}

// ExecutionWorkerConfig 控制广播、确认轮询和故障退避。
type ExecutionWorkerConfig struct {
	OperationTimeout     time.Duration
	LeaseDuration        time.Duration
	ConfirmationInterval time.Duration
	IdleInterval         time.Duration
	RetryMin             time.Duration
	RetryMax             time.Duration
}

// ExecutionWorker 广播原始签名交易并等待 SolidityNode 固化终态。
type ExecutionWorker struct {
	runner *background.Runner
}

// NewExecutionWorker 创建出款执行 Worker。
func NewExecutionWorker(
	queue ExecutionQueue,
	broadcaster tron.Broadcaster,
	reader tron.FinalizedTransactionReader,
	logger *slog.Logger,
	workerID string,
	config ExecutionWorkerConfig,
) (*ExecutionWorker, error) {
	workerID = strings.TrimSpace(workerID)
	if queue == nil || broadcaster == nil || reader == nil || logger == nil || workerID == "" || len(workerID) > 128 ||
		config.OperationTimeout <= 0 || config.LeaseDuration <= config.OperationTimeout ||
		config.ConfirmationInterval <= 0 || config.IdleInterval <= 0 ||
		config.RetryMin <= 0 || config.RetryMax < config.RetryMin {
		return nil, ErrInvalidExecutionWorker
	}
	operation := &executionOperation{
		queue: queue, broadcaster: broadcaster, reader: reader, logger: logger,
		workerID: workerID, leaseDuration: config.LeaseDuration,
		confirmationInterval: config.ConfirmationInterval,
	}
	runner, err := background.NewRunner(operation, logger, background.Config{
		Name: "payout-execution", OperationTimeout: config.OperationTimeout,
		IdleInterval: config.IdleInterval, RetryMin: config.RetryMin, RetryMax: config.RetryMax,
	}, func(err error) background.Decision {
		if errors.Is(err, tron.ErrInvalidSignedTransaction) || errors.Is(err, ErrInvalidExecutionClaim) {
			return background.Stop
		}
		return background.Retry
	})
	if err != nil {
		return nil, ErrInvalidExecutionWorker
	}
	return &ExecutionWorker{runner: runner}, nil
}

// Run 持续运行广播和固化确认。
func (worker *ExecutionWorker) Run(ctx context.Context) error {
	return worker.runner.Run(ctx)
}

type executionOperation struct {
	queue                ExecutionQueue
	broadcaster          tron.Broadcaster
	reader               tron.FinalizedTransactionReader
	logger               *slog.Logger
	workerID             string
	leaseDuration        time.Duration
	confirmationInterval time.Duration
	preferConfirmation   bool
}

func (operation *executionOperation) RunOnce(ctx context.Context) (bool, error) {
	if operation.preferConfirmation {
		confirmation, err := operation.queue.ClaimConfirmation(ctx, operation.workerID, operation.leaseDuration)
		if err == nil {
			operation.preferConfirmation = false
			return true, operation.confirm(ctx, confirmation)
		}
		if !errors.Is(err, ErrNoConfirmationJob) {
			return false, err
		}
	}
	broadcast, err := operation.queue.ClaimBroadcast(ctx, operation.workerID, operation.leaseDuration)
	if err == nil {
		operation.preferConfirmation = true
		return true, operation.broadcast(ctx, broadcast)
	}
	if !errors.Is(err, ErrNoBroadcastJob) {
		return false, err
	}
	confirmation, err := operation.queue.ClaimConfirmation(ctx, operation.workerID, operation.leaseDuration)
	if errors.Is(err, ErrNoConfirmationJob) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	operation.preferConfirmation = false
	return true, operation.confirm(ctx, confirmation)
}

func (operation *executionOperation) broadcast(ctx context.Context, claim BroadcastClaim) error {
	err := operation.broadcaster.Broadcast(ctx, claim.Transaction)
	if errors.Is(err, tron.ErrInvalidSignedTransaction) {
		return err
	}
	result, failureCode := "accepted", ""
	if err != nil {
		result, failureCode = "unknown", "broadcast_error"
	}
	if stateErr := operation.queue.BeginConfirmation(ctx, claim, result, failureCode); stateErr != nil {
		return errors.Join(err, stateErr)
	}
	if err != nil {
		operation.logger.Warn("出款广播结果未知，转入原交易确认", "payout_id", claim.PayoutID,
			"transaction_id", claim.Transaction.ID, "error", err)
	} else {
		operation.logger.Info("出款已提交节点，等待固化确认", "payout_id", claim.PayoutID,
			"transaction_id", claim.Transaction.ID)
	}
	return nil
}

func (operation *executionOperation) confirm(ctx context.Context, claim ConfirmationClaim) error {
	state, err := operation.reader.Transaction(ctx, claim.Transaction.ID)
	if err != nil {
		releaseErr := operation.queue.ReleaseConfirmation(ctx, claim, operation.confirmationInterval, "node_error")
		return errors.Join(fmt.Errorf("查询出款固化状态: %w", err), releaseErr)
	}
	if state.Solidified && state.Status == tron.TransactionSucceeded {
		if err := operation.queue.CompleteConfirmationSuccess(ctx, claim); err != nil {
			return err
		}
		operation.logger.Info("出款已固化成功", "payout_id", claim.PayoutID,
			"transaction_id", claim.Transaction.ID, "block_height", state.Block.Height)
		return nil
	}
	if state.Solidified && state.Status == tron.TransactionFailed {
		if err := operation.queue.CompleteConfirmationFailure(ctx, claim, "execution_failed"); err != nil {
			return err
		}
		operation.logger.Warn("出款链上执行失败，资金已解冻", "payout_id", claim.PayoutID,
			"transaction_id", claim.Transaction.ID, "block_height", state.Block.Height)
		return nil
	}
	if state.Status == tron.TransactionNotFound {
		head, headErr := operation.reader.SolidifiedHead(ctx)
		if headErr != nil {
			releaseErr := operation.queue.ReleaseConfirmation(ctx, claim, operation.confirmationInterval, "node_error")
			return errors.Join(fmt.Errorf("查询 TRON 固化链头: %w", headErr), releaseErr)
		}
		if !head.Timestamp.Before(claim.ExpiresAt) {
			if err := operation.queue.CompleteConfirmationFailure(ctx, claim, "expired_not_found"); err != nil {
				return err
			}
			operation.logger.Warn("出款交易过期且未进入固化链，资金已解冻",
				"payout_id", claim.PayoutID, "transaction_id", claim.Transaction.ID)
			return nil
		}
		rebroadcastErr := operation.broadcaster.Broadcast(ctx, claim.Transaction)
		failureCode := ""
		if rebroadcastErr != nil {
			failureCode = "rebroadcast_error"
		}
		if releaseErr := operation.queue.ReleaseConfirmation(
			ctx, claim, operation.confirmationInterval, failureCode,
		); releaseErr != nil {
			return errors.Join(rebroadcastErr, releaseErr)
		}
		return nil
	}
	return operation.queue.ReleaseConfirmation(ctx, claim, operation.confirmationInterval, "")
}
