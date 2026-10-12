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

// SweepQueue 定义归集 Screening Worker 所需的持久化租约能力。
type SweepQueue interface {
	Claim(context.Context, string, time.Duration) (SweepClaim, error)
	Complete(context.Context, SweepClaim, Result) error
	Release(context.Context, SweepClaim, string) error
}

// SweepWorker 执行归集计划前的当前地址风险筛查。
type SweepWorker struct {
	runner *background.Runner
}

// NewSweepWorker 创建归集地址筛查 Worker。
func NewSweepWorker(
	queue SweepQueue,
	provider Provider,
	logger *slog.Logger,
	workerID string,
	config WorkerConfig,
) (*SweepWorker, error) {
	workerID = strings.TrimSpace(workerID)
	if queue == nil || provider == nil || logger == nil || workerID == "" || len(workerID) > 128 ||
		config.OperationTimeout <= 0 || config.LeaseDuration <= config.OperationTimeout ||
		config.IdleInterval <= 0 || config.RetryMin <= 0 || config.RetryMax < config.RetryMin {
		return nil, ErrInvalidWorker
	}
	operation := &sweepWorkerOperation{
		queue: queue, provider: provider, logger: logger,
		workerID: workerID, leaseDuration: config.LeaseDuration,
	}
	runner, err := background.NewRunner(operation, logger, background.Config{
		Name: "sweep-screening", OperationTimeout: config.OperationTimeout,
		IdleInterval: config.IdleInterval, RetryMin: config.RetryMin, RetryMax: config.RetryMax,
		Observer: config.Observer,
	}, func(err error) background.Decision {
		if errors.Is(err, ErrInvalidSweepClaim) || errors.Is(err, ErrInvalidResult) {
			return background.Stop
		}
		return background.Retry
	})
	if err != nil {
		return nil, ErrInvalidWorker
	}
	return &SweepWorker{runner: runner}, nil
}

// Run 持续执行归集地址筛查任务。
func (worker *SweepWorker) Run(ctx context.Context) error {
	return worker.runner.Run(ctx)
}

type sweepWorkerOperation struct {
	queue         SweepQueue
	provider      Provider
	logger        *slog.Logger
	workerID      string
	leaseDuration time.Duration
}

func (operation *sweepWorkerOperation) RunOnce(ctx context.Context) (bool, error) {
	claim, err := operation.queue.Claim(ctx, operation.workerID, operation.leaseDuration)
	if errors.Is(err, ErrNoSweepScreeningJob) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	result, err := operation.provider.Screen(ctx, claim.Request)
	if err != nil {
		releaseErr := operation.queue.Release(ctx, claim, "provider_error")
		if releaseErr != nil {
			return true, errors.Join(fmt.Errorf("归集地址筛查 Provider 失败: %w", err), releaseErr)
		}
		return true, fmt.Errorf("归集地址筛查 Provider 失败: %w", err)
	}
	if err := operation.queue.Complete(ctx, claim, result); err != nil {
		return true, err
	}
	operation.logger.Info("归集地址筛查结果已持久化",
		"screening_id", claim.JobID,
		"decision", result.Decision,
		"provider", result.Provider,
		"attempt", claim.LeaseEpoch,
	)
	return true, nil
}
