package screening

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/background"
)

// InboundQueue 定义入金 Screening Worker 所需的持久化租约能力。
type InboundQueue interface {
	Claim(context.Context, string, time.Duration) (InboundClaim, error)
	Complete(context.Context, InboundClaim, Result) error
	Release(context.Context, InboundClaim, string) error
}

// InboundWorker 从已固化入金事件发现并筛查来源地址。
type InboundWorker struct {
	runner *background.Runner
}

// NewInboundWorker 创建入金地址筛查 Worker。
func NewInboundWorker(
	queue InboundQueue,
	provider Provider,
	logger *slog.Logger,
	workerID string,
	config WorkerConfig,
) (*InboundWorker, error) {
	workerID = strings.TrimSpace(workerID)
	if queue == nil || provider == nil || logger == nil || workerID == "" || len(workerID) > 128 ||
		config.OperationTimeout <= 0 || config.LeaseDuration <= config.OperationTimeout ||
		config.IdleInterval <= 0 || config.RetryMin <= 0 || config.RetryMax < config.RetryMin {
		return nil, ErrInvalidWorker
	}
	operation := &inboundWorkerOperation{
		queue: queue, provider: provider, logger: logger,
		workerID: workerID, leaseDuration: config.LeaseDuration,
	}
	runner, err := background.NewRunner(operation, logger, background.Config{
		Name: "deposit-screening", OperationTimeout: config.OperationTimeout,
		IdleInterval: config.IdleInterval, RetryMin: config.RetryMin, RetryMax: config.RetryMax,
		Observer: config.Observer,
	}, func(err error) background.Decision {
		if errors.Is(err, ErrInvalidInboundClaim) || errors.Is(err, ErrInvalidResult) {
			return background.Stop
		}
		return background.Retry
	})
	if err != nil {
		return nil, ErrInvalidWorker
	}
	return &InboundWorker{runner: runner}, nil
}

// Run 持续执行入金地址筛查任务。
func (worker *InboundWorker) Run(ctx context.Context) error {
	return worker.runner.Run(ctx)
}

type inboundWorkerOperation struct {
	queue         InboundQueue
	provider      Provider
	logger        *slog.Logger
	workerID      string
	leaseDuration time.Duration
}

func (operation *inboundWorkerOperation) RunOnce(ctx context.Context) (bool, error) {
	claim, err := operation.queue.Claim(ctx, operation.workerID, operation.leaseDuration)
	if errors.Is(err, ErrNoInboundScreeningJob) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	result, err := operation.provider.Screen(ctx, claim.Request)
	if err != nil {
		releaseErr := operation.queue.Release(ctx, claim, "provider_error")
		if releaseErr != nil {
			return true, errors.Join(fmt.Errorf("入金地址筛查 Provider 失败: %w", err), releaseErr)
		}
		return true, fmt.Errorf("入金地址筛查 Provider 失败: %w", err)
	}
	if err := operation.queue.Complete(ctx, claim, result); err != nil {
		return true, err
	}
	operation.logger.Info("入金地址筛查结果已持久化",
		"screening_id", claim.JobID,
		"decision", result.Decision,
		"provider", result.Provider,
		"attempt", claim.LeaseEpoch,
	)
	return true, nil
}
