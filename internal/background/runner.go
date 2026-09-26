// Package background 提供与业务无关的后台任务运行语义。
package background

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strings"
	"time"
)

var ErrInvalidRunner = errors.New("后台任务运行器配置无效")

// Decision 表示一次任务错误应如何处理。
type Decision uint8

const (
	Retry Decision = iota
	Idle
	Stop
)

// Operation 执行一次后台任务。worked 表示仍有积压任务，应立即继续。
type Operation interface {
	RunOnce(ctx context.Context) (worked bool, err error)
}

// OperationFunc 将函数适配为 Operation。
type OperationFunc func(context.Context) (bool, error)

// RunOnce 执行函数。
func (operation OperationFunc) RunOnce(ctx context.Context) (bool, error) {
	return operation(ctx)
}

// Classifier 判断任务错误是否应重试、空闲等待或终止进程。
type Classifier func(error) Decision

// Config 控制任务超时、空闲间隔和错误退避。
type Config struct {
	Name             string
	OperationTimeout time.Duration
	IdleInterval     time.Duration
	RetryMin         time.Duration
	RetryMax         time.Duration
}

// Runner 持续运行单步后台任务。
type Runner struct {
	operation  Operation
	logger     *slog.Logger
	config     Config
	classifier Classifier
}

// NewRunner 创建后台任务运行器。classifier 为空时所有任务错误均重试。
func NewRunner(operation Operation, logger *slog.Logger, config Config, classifier Classifier) (*Runner, error) {
	if operation == nil || logger == nil || strings.TrimSpace(config.Name) == "" ||
		config.OperationTimeout <= 0 || config.IdleInterval <= 0 ||
		config.RetryMin <= 0 || config.RetryMax < config.RetryMin {
		return nil, ErrInvalidRunner
	}
	return &Runner{operation: operation, logger: logger, config: config, classifier: classifier}, nil
}

// Run 持续运行任务，直到上下文取消或分类器要求停止。
func (runner *Runner) Run(ctx context.Context) error {
	retryDelay := runner.config.RetryMin
	for {
		if ctx.Err() != nil {
			return nil
		}
		operationCtx, cancel := context.WithTimeout(ctx, runner.config.OperationTimeout)
		worked, err := runner.operation.RunOnce(operationCtx)
		cancel()
		if ctx.Err() != nil {
			return nil
		}
		if err == nil {
			retryDelay = runner.config.RetryMin
			if worked {
				continue
			}
			if !wait(ctx, runner.config.IdleInterval) {
				return nil
			}
			continue
		}

		switch runner.classify(err) {
		case Stop:
			return fmt.Errorf("后台任务 %s 停止: %w", runner.config.Name, err)
		case Idle:
			if !wait(ctx, runner.config.IdleInterval) {
				return nil
			}
		default:
			actualDelay := jitter(retryDelay)
			runner.logger.Warn("后台任务暂时失败", "task", runner.config.Name, "error", err, "retry_after", actualDelay)
			if !wait(ctx, actualDelay) {
				return nil
			}
			retryDelay = nextRetry(retryDelay, runner.config.RetryMax)
		}
	}
}

func (runner *Runner) classify(err error) Decision {
	if runner.classifier == nil {
		return Retry
	}
	return runner.classifier(err)
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
