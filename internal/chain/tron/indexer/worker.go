package indexer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"
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
	stepper Stepper
	logger  *slog.Logger
	config  WorkerConfig
}

// NewWorker 创建扫描 Worker。
func NewWorker(stepper Stepper, logger *slog.Logger, config WorkerConfig) (*Worker, error) {
	if stepper == nil || logger == nil || config.StepTimeout <= 0 || config.IdleInterval <= 0 ||
		config.RetryMin <= 0 || config.RetryMax < config.RetryMin {
		return nil, ErrInvalidWorker
	}
	return &Worker{stepper: stepper, logger: logger, config: config}, nil
}

// Run 持续扫描，直到上下文取消或遇到需要人工处理的一致性错误。
func (worker *Worker) Run(ctx context.Context) error {
	retryDelay := worker.config.RetryMin
	for {
		if ctx.Err() != nil {
			return nil
		}
		stepCtx, cancel := context.WithTimeout(ctx, worker.config.StepTimeout)
		result, err := worker.stepper.Step(stepCtx)
		cancel()
		if ctx.Err() != nil {
			return nil
		}
		if err == nil {
			retryDelay = worker.config.RetryMin
			if result.Processed {
				if result.EventCount > 0 {
					worker.logger.Info("已保存 TRON 链事件", "height", result.Height, "events", result.EventCount)
				}
				continue
			}
			if !wait(ctx, worker.config.IdleInterval) {
				return nil
			}
			continue
		}
		if isPermanentWorkerError(err) {
			return fmt.Errorf("扫描因一致性错误停止: %w", err)
		}
		if errors.Is(err, ErrLeaseUnavailable) {
			if !wait(ctx, worker.config.IdleInterval) {
				return nil
			}
			continue
		}
		actualDelay := jitter(retryDelay)
		worker.logger.Warn("TRON 扫描暂时失败", "error", err, "retry_after", actualDelay)
		if !wait(ctx, actualDelay) {
			return nil
		}
		retryDelay = nextRetry(retryDelay, worker.config.RetryMax)
	}
}

func isPermanentWorkerError(err error) bool {
	return errors.Is(err, ErrInvalidCursor) ||
		errors.Is(err, ErrAnchorConflict) ||
		errors.Is(err, ErrContractConflict) ||
		errors.Is(err, ErrContractUnbound) ||
		errors.Is(err, ErrInvalidBlock) ||
		errors.Is(err, ErrInvalidEvent) ||
		errors.Is(err, ErrEventConflict)
}

func wait(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func jitter(duration time.Duration) time.Duration {
	spread := duration / 5
	if spread == 0 {
		return duration
	}
	offset := time.Duration(rand.Int64N(int64(spread)*2+1)) - spread
	return duration + offset
}

func nextRetry(current, maximum time.Duration) time.Duration {
	if current >= maximum || current > maximum/2 {
		return maximum
	}
	return current * 2
}
