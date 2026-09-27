package observability

import (
	"fmt"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/background"
	"github.com/prometheus/client_golang/prometheus"
)

// WorkerObserver 将后台运行器状态记录为固定标签集合的 Prometheus 指标。
type WorkerObserver struct {
	operations          *prometheus.CounterVec
	durations           *prometheus.HistogramVec
	lastSuccess         *prometheus.GaugeVec
	consecutiveFailures *prometheus.GaugeVec
	running             *prometheus.GaugeVec
}

var _ background.Observer = (*WorkerObserver)(nil)

// NewWorkerObserver 注册后台任务指标。
func NewWorkerObserver(registerer prometheus.Registerer) (*WorkerObserver, error) {
	if registerer == nil {
		return nil, ErrInvalidConfig
	}
	observer := &WorkerObserver{
		operations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "stablecoin_gateway", Subsystem: "worker", Name: "operations_total",
			Help: "Total background worker operations by result.",
		}, []string{"worker", "result"}),
		durations: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "stablecoin_gateway", Subsystem: "worker", Name: "operation_duration_seconds",
			Help: "Background worker operation duration by result.",
		}, []string{"worker", "result"}),
		lastSuccess: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "stablecoin_gateway", Subsystem: "worker", Name: "last_success_timestamp_seconds",
			Help: "Unix timestamp of the last successful worker operation.",
		}, []string{"worker"}),
		consecutiveFailures: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "stablecoin_gateway", Subsystem: "worker", Name: "consecutive_failures",
			Help: "Current number of consecutive worker operation failures.",
		}, []string{"worker"}),
		running: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "stablecoin_gateway", Subsystem: "worker", Name: "running",
			Help: "Whether the background worker run loop is active.",
		}, []string{"worker"}),
	}
	collectors := []prometheus.Collector{
		observer.operations, observer.durations, observer.lastSuccess,
		observer.consecutiveFailures, observer.running,
	}
	for _, collector := range collectors {
		if err := registerer.Register(collector); err != nil {
			return nil, fmt.Errorf("注册 Worker 指标: %w", err)
		}
	}
	return observer, nil
}

func (observer *WorkerObserver) Started(name string) {
	observer.running.WithLabelValues(name).Set(1)
}

func (observer *WorkerObserver) Succeeded(name string, worked bool, duration time.Duration) {
	result := "idle"
	if worked {
		result = "worked"
	}
	observer.observe(name, result, duration)
	observer.lastSuccess.WithLabelValues(name).SetToCurrentTime()
	observer.consecutiveFailures.WithLabelValues(name).Set(0)
}

func (observer *WorkerObserver) Failed(name string, decision background.Decision, duration time.Duration) {
	result := "retry_error"
	switch decision {
	case background.Idle:
		result = "idle_error"
	case background.Stop:
		result = "stop_error"
	}
	observer.observe(name, result, duration)
	observer.consecutiveFailures.WithLabelValues(name).Inc()
}

func (observer *WorkerObserver) Stopped(name string) {
	observer.running.WithLabelValues(name).Set(0)
}

func (observer *WorkerObserver) observe(name, result string, duration time.Duration) {
	observer.operations.WithLabelValues(name, result).Inc()
	observer.durations.WithLabelValues(name, result).Observe(duration.Seconds())
}
