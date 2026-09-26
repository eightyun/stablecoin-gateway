package outbox

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestProcessorRunOnceMarksSucceeded(t *testing.T) {
	store := &fakeStore{events: []Event{{ID: "event-1", Topic: "deposit.confirmed", Attempts: 1}}}
	processor := newTestProcessor(t, store, map[string]Handler{
		"deposit.confirmed": HandlerFunc(func(context.Context, Event) error { return nil }),
	})

	count, err := processor.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if count != 1 {
		t.Fatalf("RunOnce() count = %d, 期望 1", count)
	}
	if store.succeededID != "event-1" {
		t.Fatalf("成功事件 = %q, 期望 event-1", store.succeededID)
	}
	if len(store.topics) != 1 || store.topics[0] != "deposit.confirmed" {
		t.Fatalf("领取 topics = %v", store.topics)
	}
}

func TestProcessorRunOnceSchedulesRetry(t *testing.T) {
	handleErr := errors.New("临时错误")
	store := &fakeStore{events: []Event{{ID: "event-1", Topic: "webhook", Attempts: 2}}}
	processor := newTestProcessor(t, store, map[string]Handler{
		"webhook": HandlerFunc(func(context.Context, Event) error { return handleErr }),
	})

	count, err := processor.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if count != 1 {
		t.Fatalf("RunOnce() count = %d, 期望 1", count)
	}
	if store.retryID != "event-1" || store.retryAfter != 2*time.Second {
		t.Fatalf("重试事件 = %q, 退避 = %s", store.retryID, store.retryAfter)
	}
	if store.reason != handleErr.Error() {
		t.Fatalf("失败原因 = %q, 期望 %q", store.reason, handleErr)
	}
}

func TestProcessorRunOnceMarksDeadAtAttemptLimit(t *testing.T) {
	store := &fakeStore{events: []Event{{ID: "event-1", Topic: "webhook", Attempts: 3}}}
	processor := newTestProcessor(t, store, map[string]Handler{
		"webhook": HandlerFunc(func(context.Context, Event) error { return errors.New("永久错误") }),
	})

	if _, err := processor.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if store.deadID != "event-1" {
		t.Fatalf("死信事件 = %q, 期望 event-1", store.deadID)
	}
}

func TestProcessorRunOnceMarksUnknownTopicDead(t *testing.T) {
	store := &fakeStore{events: []Event{{ID: "event-1", Topic: "unknown", Attempts: 1}}}
	processor := newTestProcessor(t, store, nil)

	if _, err := processor.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if store.deadID != "event-1" {
		t.Fatalf("死信事件 = %q, 期望 event-1", store.deadID)
	}
}

func TestNewProcessorValidatesConfig(t *testing.T) {
	validConfig := Config{
		WorkerID:      "worker-1",
		BatchSize:     10,
		LeaseDuration: time.Minute,
		MaxAttempts:   3,
		BaseBackoff:   time.Second,
		MaxBackoff:    time.Minute,
	}

	tests := []struct {
		name    string
		store   Store
		mutate  func(*Config)
		wantErr error
	}{
		{name: "Store 为空", wantErr: ErrStoreRequired},
		{name: "Worker ID 为空", store: &fakeStore{}, mutate: func(config *Config) { config.WorkerID = "" }, wantErr: ErrWorkerIDRequired},
		{name: "批量大小无效", store: &fakeStore{}, mutate: func(config *Config) { config.BatchSize = 0 }, wantErr: ErrInvalidBatchSize},
		{name: "租约时长无效", store: &fakeStore{}, mutate: func(config *Config) { config.LeaseDuration = 0 }, wantErr: ErrInvalidLeaseDuration},
		{name: "最大尝试次数无效", store: &fakeStore{}, mutate: func(config *Config) { config.MaxAttempts = 0 }, wantErr: ErrInvalidMaxAttempts},
		{name: "退避配置无效", store: &fakeStore{}, mutate: func(config *Config) { config.MaxBackoff = 0 }, wantErr: ErrInvalidBackoff},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := validConfig
			if test.mutate != nil {
				test.mutate(&config)
			}
			_, err := NewProcessor(test.store, config, nil)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("NewProcessor() error = %v, 期望 %v", err, test.wantErr)
			}
		})
	}
}

func newTestProcessor(t *testing.T, store Store, handlers map[string]Handler) *Processor {
	t.Helper()
	processor, err := NewProcessor(store, Config{
		WorkerID:      "worker-1",
		BatchSize:     10,
		LeaseDuration: time.Minute,
		MaxAttempts:   3,
		BaseBackoff:   time.Second,
		MaxBackoff:    time.Minute,
	}, handlers)
	if err != nil {
		t.Fatalf("NewProcessor() error = %v", err)
	}
	return processor
}

type fakeStore struct {
	events      []Event
	topics      []string
	succeededID string
	retryID     string
	retryAfter  time.Duration
	deadID      string
	reason      string
}

func (store *fakeStore) Claim(_ context.Context, _ string, topics []string, _ int, _ time.Duration) ([]Event, error) {
	store.topics = append([]string(nil), topics...)
	return store.events, nil
}

func (store *fakeStore) MarkSucceeded(_ context.Context, eventID, _ string) error {
	store.succeededID = eventID
	return nil
}

func (store *fakeStore) MarkRetry(_ context.Context, eventID, _ string, retryAfter time.Duration, reason string) error {
	store.retryID = eventID
	store.retryAfter = retryAfter
	store.reason = reason
	return nil
}

func (store *fakeStore) MarkDead(_ context.Context, eventID, _, reason string) error {
	store.deadID = eventID
	store.reason = reason
	return nil
}
