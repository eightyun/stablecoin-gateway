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

var ErrInvalidWorker = errors.New("地址筛查 Worker 配置无效")

// Queue 定义 Screening Worker 所需的持久化租约能力。
type Queue interface {
	Claim(context.Context, string, time.Duration) (Claim, error)
	Complete(context.Context, Claim, Result) error
	Release(context.Context, Claim, string) error
}

// WorkerConfig 控制地址筛查任务的超时、租约和退避。
type WorkerConfig struct {
	OperationTimeout time.Duration
	LeaseDuration    time.Duration
	IdleInterval     time.Duration
	RetryMin         time.Duration
	RetryMax         time.Duration
	Observer         background.Observer
}

// Worker 从数据库领取任务并调用不持有资金权限的筛查 Provider。
type Worker struct {
	runner *background.Runner
}

// NewWorker 创建地址筛查 Worker。
func NewWorker(
	queue Queue,
	provider Provider,
	logger *slog.Logger,
	workerID string,
	config WorkerConfig,
) (*Worker, error) {
	workerID = strings.TrimSpace(workerID)
	if queue == nil || provider == nil || logger == nil || workerID == "" || len(workerID) > 128 ||
		config.OperationTimeout <= 0 || config.LeaseDuration <= config.OperationTimeout ||
		config.IdleInterval <= 0 || config.RetryMin <= 0 || config.RetryMax < config.RetryMin {
		return nil, ErrInvalidWorker
	}
	operation := &workerOperation{
		queue: queue, provider: provider, logger: logger,
		workerID: workerID, leaseDuration: config.LeaseDuration,
	}
	runner, err := background.NewRunner(operation, logger, background.Config{
		Name: "payout-screening", OperationTimeout: config.OperationTimeout,
		IdleInterval: config.IdleInterval, RetryMin: config.RetryMin, RetryMax: config.RetryMax,
		Observer: config.Observer,
	}, func(err error) background.Decision {
		if errors.Is(err, ErrInvalidScreeningClaim) || errors.Is(err, ErrInvalidResult) {
			return background.Stop
		}
		return background.Retry
	})
	if err != nil {
		return nil, ErrInvalidWorker
	}
	return &Worker{runner: runner}, nil
}

// Run 持续执行地址筛查任务。
func (worker *Worker) Run(ctx context.Context) error {
	return worker.runner.Run(ctx)
}

type workerOperation struct {
	queue         Queue
	provider      Provider
	logger        *slog.Logger
	workerID      string
	leaseDuration time.Duration
}

func (operation *workerOperation) RunOnce(ctx context.Context) (bool, error) {
	claim, err := operation.queue.Claim(ctx, operation.workerID, operation.leaseDuration)
	if errors.Is(err, ErrNoScreeningJob) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	result, err := operation.provider.Screen(ctx, claim.Request)
	if err != nil {
		releaseErr := operation.queue.Release(ctx, claim, "provider_error")
		if releaseErr != nil {
			return true, errors.Join(fmt.Errorf("地址筛查 Provider 失败: %w", err), releaseErr)
		}
		return true, fmt.Errorf("地址筛查 Provider 失败: %w", err)
	}
	if err := operation.queue.Complete(ctx, claim, result); err != nil {
		return true, err
	}
	operation.logger.Info("出款地址筛查结果已持久化",
		"payout_id", claim.PayoutID,
		"decision", result.Decision,
		"provider", result.Provider,
		"attempt", claim.LeaseEpoch,
	)
	return true, nil
}
