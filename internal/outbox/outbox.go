package outbox

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
)

var (
	ErrStoreRequired        = errors.New("Outbox Store 不能为空")
	ErrWorkerIDRequired     = errors.New("Worker ID 不能为空")
	ErrInvalidBatchSize     = errors.New("批量大小必须大于 0")
	ErrInvalidLeaseDuration = errors.New("租约时长必须大于 0")
	ErrInvalidMaxAttempts   = errors.New("最大尝试次数必须大于 0")
	ErrInvalidBackoff       = errors.New("退避时间配置无效")
	ErrHandlerNotFound      = errors.New("未注册 Outbox Handler")
	ErrLeaseLost            = errors.New("Outbox 任务租约已失效")
)

// Event 表示一次已领取的 Outbox 事件。
type Event struct {
	ID        string
	Topic     string
	Payload   []byte
	Attempts  int
	CreatedAt time.Time
}

// Store 定义 Outbox 任务的租约和状态变更能力。
type Store interface {
	Claim(ctx context.Context, workerID string, topics []string, limit int, leaseDuration time.Duration) ([]Event, error)
	MarkSucceeded(ctx context.Context, eventID, workerID string) error
	MarkRetry(ctx context.Context, eventID, workerID string, retryAfter time.Duration, reason string) error
	MarkDead(ctx context.Context, eventID, workerID, reason string) error
}

// Handler 处理指定 topic 的事件。处理过程必须支持幂等重试。
type Handler interface {
	Handle(ctx context.Context, event Event) error
}

// HandlerFunc 将函数适配为 Handler。
type HandlerFunc func(ctx context.Context, event Event) error

// Handle 实现 Handler。
func (function HandlerFunc) Handle(ctx context.Context, event Event) error {
	return function(ctx, event)
}

// Config 定义单次处理参数。
type Config struct {
	WorkerID      string
	BatchSize     int
	LeaseDuration time.Duration
	MaxAttempts   int
	BaseBackoff   time.Duration
	MaxBackoff    time.Duration
}

// Processor 领取并分发 Outbox 事件。
type Processor struct {
	store    Store
	config   Config
	handlers map[string]Handler
	topics   []string
}

// NewProcessor 创建 Outbox Processor。
func NewProcessor(store Store, config Config, handlers map[string]Handler) (*Processor, error) {
	if store == nil {
		return nil, ErrStoreRequired
	}
	if config.WorkerID == "" {
		return nil, ErrWorkerIDRequired
	}
	if config.BatchSize <= 0 {
		return nil, ErrInvalidBatchSize
	}
	if config.LeaseDuration <= 0 {
		return nil, ErrInvalidLeaseDuration
	}
	if config.MaxAttempts <= 0 {
		return nil, ErrInvalidMaxAttempts
	}
	if config.BaseBackoff <= 0 || config.MaxBackoff < config.BaseBackoff {
		return nil, ErrInvalidBackoff
	}

	handlerCopy := make(map[string]Handler, len(handlers))
	topics := make([]string, 0, len(handlers))
	for topic, handler := range handlers {
		if topic != "" && handler != nil {
			handlerCopy[topic] = handler
			topics = append(topics, topic)
		}
	}
	sort.Strings(topics)

	return &Processor{store: store, config: config, handlers: handlerCopy, topics: topics}, nil
}

// RunOnce 领取并处理一批事件，返回实际领取数量。
func (processor *Processor) RunOnce(ctx context.Context) (int, error) {
	events, err := processor.store.Claim(
		ctx,
		processor.config.WorkerID,
		processor.topics,
		processor.config.BatchSize,
		processor.config.LeaseDuration,
	)
	if err != nil {
		return 0, fmt.Errorf("领取 Outbox 事件: %w", err)
	}

	var processingErrors []error
	for _, event := range events {
		if err := processor.process(ctx, event); err != nil {
			processingErrors = append(processingErrors, fmt.Errorf("处理 Outbox 事件 %s: %w", event.ID, err))
		}
	}

	return len(events), errors.Join(processingErrors...)
}

func (processor *Processor) process(ctx context.Context, event Event) error {
	handler, ok := processor.handlers[event.Topic]
	if !ok {
		reason := fmt.Sprintf("%s: %s", ErrHandlerNotFound, event.Topic)
		return processor.store.MarkDead(ctx, event.ID, processor.config.WorkerID, reason)
	}

	err := handler.Handle(ctx, event)
	if err == nil {
		return processor.store.MarkSucceeded(ctx, event.ID, processor.config.WorkerID)
	}

	reason := err.Error()
	if event.Attempts >= processor.config.MaxAttempts {
		return processor.store.MarkDead(ctx, event.ID, processor.config.WorkerID, reason)
	}

	return processor.store.MarkRetry(
		ctx,
		event.ID,
		processor.config.WorkerID,
		processor.retryBackoff(event.Attempts),
		reason,
	)
}

func (processor *Processor) retryBackoff(attempts int) time.Duration {
	backoff := processor.config.BaseBackoff
	for currentAttempt := 1; currentAttempt < attempts; currentAttempt++ {
		if backoff >= processor.config.MaxBackoff/2 {
			return processor.config.MaxBackoff
		}
		backoff *= 2
	}
	if backoff > processor.config.MaxBackoff {
		return processor.config.MaxBackoff
	}
	return backoff
}
