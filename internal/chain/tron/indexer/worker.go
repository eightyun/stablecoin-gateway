package indexer

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/background"
)

var ErrInvalidWorker = errors.New("扫描 Worker 配置无效")

// Stepper 执行一次持久化区块扫描。
type Stepper interface {
	Step(ctx context.Context) (StepResult, error)
}

// WorkerConfig 控制扫描频率、单步超时和错误退避。
type WorkerConfig struct {
	StepTimeout  time.Duration
	IdleInterval time.Duration
	RetryMin     time.Duration
	RetryMax     time.Duration
}

// Worker 持续驱动扫描器追赶已固化链头。
type Worker struct {
	runner *background.Runner
}

// NewWorker 创建扫描 Worker。
func NewWorker(stepper Stepper, logger *slog.Logger, config WorkerConfig) (*Worker, error) {
	if stepper == nil || logger == nil || config.StepTimeout <= 0 || config.IdleInterval <= 0 ||
		config.RetryMin <= 0 || config.RetryMax < config.RetryMin {
		return nil, ErrInvalidWorker
	}
	operation := background.OperationFunc(func(ctx context.Context) (bool, error) {
		result, err := stepper.Step(ctx)
		if err == nil && result.Processed && result.EventCount > 0 {
			logger.Info("已保存 TRON 链事件", "height", result.Height, "events", result.EventCount)
		}
		return result.Processed, err
	})
	runner, err := background.NewRunner(operation, logger, background.Config{
		Name:             "tron-indexer",
		OperationTimeout: config.StepTimeout,
		IdleInterval:     config.IdleInterval,
		RetryMin:         config.RetryMin,
		RetryMax:         config.RetryMax,
	}, classifyWorkerError)
	if err != nil {
		return nil, ErrInvalidWorker
	}
	return &Worker{runner: runner}, nil
}

// Run 持续扫描，直到上下文取消或遇到需要人工处理的一致性错误。
func (worker *Worker) Run(ctx context.Context) error {
	return worker.runner.Run(ctx)
}

func classifyWorkerError(err error) background.Decision {
	if errors.Is(err, ErrInvalidCursor) ||
		errors.Is(err, ErrAnchorConflict) ||
		errors.Is(err, ErrContractConflict) ||
		errors.Is(err, ErrContractUnbound) ||
		errors.Is(err, ErrInvalidBlock) ||
		errors.Is(err, ErrInvalidEvent) ||
		errors.Is(err, ErrEventConflict) {
		return background.Stop
	}
	if errors.Is(err, ErrLeaseUnavailable) {
		return background.Idle
	}
	return background.Retry
}
