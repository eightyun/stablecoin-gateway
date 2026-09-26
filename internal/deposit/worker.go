package deposit

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/background"
)

var ErrInvalidWorker = errors.New("充值匹配 Worker 配置无效")

// Matcher 提供一次事件匹配和一批意图过期操作。
type Matcher interface {
	MatchNext(ctx context.Context) (MatchResult, error)
	ExpireDue(ctx context.Context, limit int) (int64, error)
}

// WorkerConfig 控制匹配频率、单次超时和意图过期批次。
type WorkerConfig struct {
	OperationTimeout time.Duration
	IdleInterval     time.Duration
	RetryMin         time.Duration
	RetryMax         time.Duration
	ExpireInterval   time.Duration
	ExpireBatchSize  int
}

// Worker 持续匹配充值事件并关闭到期意图。
type Worker struct {
	runner *background.Runner
}

// NewWorker 创建充值匹配 Worker。
func NewWorker(matcher Matcher, logger *slog.Logger, config WorkerConfig) (*Worker, error) {
	if matcher == nil || logger == nil || config.OperationTimeout <= 0 || config.IdleInterval <= 0 ||
		config.RetryMin <= 0 || config.RetryMax < config.RetryMin || config.ExpireInterval <= 0 ||
		config.ExpireBatchSize <= 0 || config.ExpireBatchSize > 1000 {
		return nil, ErrInvalidWorker
	}
	operation := &matchingOperation{
		matcher: matcher, logger: logger, expireInterval: config.ExpireInterval,
		expireBatchSize: config.ExpireBatchSize,
	}
	runner, err := background.NewRunner(operation, logger, background.Config{
		Name:             "deposit-matcher",
		OperationTimeout: config.OperationTimeout,
		IdleInterval:     config.IdleInterval,
		RetryMin:         config.RetryMin,
		RetryMax:         config.RetryMax,
	}, func(err error) background.Decision {
		if errors.Is(err, ErrInvalidLimit) {
			return background.Stop
		}
		return background.Retry
	})
	if err != nil {
		return nil, ErrInvalidWorker
	}
	return &Worker{runner: runner}, nil
}

// Run 持续执行充值匹配，直到上下文取消或出现不可恢复错误。
func (worker *Worker) Run(ctx context.Context) error {
	return worker.runner.Run(ctx)
}

type matchingOperation struct {
	matcher         Matcher
	logger          *slog.Logger
	expireInterval  time.Duration
	expireBatchSize int
	nextExpiryCheck time.Time
}

func (operation *matchingOperation) RunOnce(ctx context.Context) (bool, error) {
	result, err := operation.matcher.MatchNext(ctx)
	if err != nil {
		return false, err
	}
	if result.Processed {
		operation.logger.Info("已处理充值链事件",
			"transaction_id", result.TransactionID,
			"log_index", result.LogIndex,
			"match_status", result.MatchStatus,
			"intent_id", result.IntentID,
			"intent_status", result.IntentStatus,
			"reason", result.Reason,
		)
	}

	now := time.Now()
	if now.Before(operation.nextExpiryCheck) {
		return result.Processed, nil
	}
	expired, err := operation.matcher.ExpireDue(ctx, operation.expireBatchSize)
	if err != nil {
		return result.Processed, err
	}
	if expired < int64(operation.expireBatchSize) {
		operation.nextExpiryCheck = now.Add(operation.expireInterval)
	}
	if expired > 0 {
		operation.logger.Info("已关闭到期充值意图", "count", expired)
	}
	return result.Processed || expired > 0, nil
}
