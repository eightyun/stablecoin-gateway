package sweep

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/background"
)

// Planner 定义归集规划持久化能力。
type Planner interface {
	PlanNext(context.Context, Policy) (Plan, error)
}

// WorkerConfig 控制归集规划后台循环。
type WorkerConfig struct {
	OperationTimeout time.Duration
	IdleInterval     time.Duration
	RetryMin         time.Duration
	RetryMax         time.Duration
	Observer         background.Observer
}

// Worker 持续生成符合策略的归集计划。
type Worker struct {
	runner *background.Runner
}

// NewWorker 创建归集规划 Worker。
func NewWorker(planner Planner, policy Policy, logger *slog.Logger, config WorkerConfig) (*Worker, error) {
	if planner == nil || logger == nil || validatePolicy(policy) != nil || config.OperationTimeout <= 0 ||
		config.IdleInterval <= 0 || config.RetryMin <= 0 || config.RetryMax < config.RetryMin {
		return nil, ErrInvalidPolicy
	}
	operation := background.OperationFunc(func(ctx context.Context) (bool, error) {
		plan, err := planner.PlanNext(ctx, policy)
		if errors.Is(err, ErrNoCandidate) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		logger.Info("归集计划已生成", "plan_id", plan.ID, "asset_id", plan.AssetID,
			"source_wallet_id", plan.SourceWalletID, "amount", plan.Amount,
			"snapshot_run_id", plan.SnapshotRunID)
		return true, nil
	})
	runner, err := background.NewRunner(operation, logger, background.Config{
		Name: "sweep-planner", OperationTimeout: config.OperationTimeout,
		IdleInterval: config.IdleInterval, RetryMin: config.RetryMin, RetryMax: config.RetryMax,
		Observer: config.Observer,
	}, func(err error) background.Decision {
		if errors.Is(err, ErrInvalidPolicy) || errors.Is(err, ErrAssetUnavailable) || errors.Is(err, ErrHotWalletCount) {
			return background.Stop
		}
		return background.Retry
	})
	if err != nil {
		return nil, ErrInvalidPolicy
	}
	return &Worker{runner: runner}, nil
}

// Run 持续运行归集规划任务。
func (worker *Worker) Run(ctx context.Context) error {
	return worker.runner.Run(ctx)
}
