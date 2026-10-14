package observability

import (
	"log/slog"

	"github.com/eightyun/stablecoin-gateway/internal/signer"
	"github.com/prometheus/client_golang/prometheus"
)

// SignerObserver 记录固定基数请求指标及可关联签名结果的审计日志。
type SignerObserver struct {
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
	logger   *slog.Logger
}

// NewSignerObserver 在当前进程注册 signer 请求指标。
func (service *Service) NewSignerObserver(logger *slog.Logger) (*SignerObserver, error) {
	if logger == nil {
		return nil, ErrInvalidConfig
	}
	observer := &SignerObserver{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "stablecoin_gateway", Subsystem: "signer", Name: "requests_total",
			Help: "Total signer HTTP requests by bounded operation and result.",
		}, []string{"operation", "result"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "stablecoin_gateway", Subsystem: "signer", Name: "request_duration_seconds",
			Help:    "Signer HTTP processing duration including idempotency storage and signing.",
			Buckets: []float64{0.01, 0.05, 0.1, 0.5, 1, 2, 5, 10, 25, 60},
		}, []string{"operation"}),
		logger: logger,
	}
	if err := service.Register(observer.requests, observer.duration); err != nil {
		return nil, err
	}
	for _, operation := range []string{"transfer", "sweep", "unknown"} {
		observer.duration.WithLabelValues(operation)
		for _, result := range []string{"success", "unauthorized", "conflict", "policy_denied", "failed", "invalid_request", "timeout", "canceled"} {
			observer.requests.WithLabelValues(operation, result)
		}
	}
	return observer, nil
}

// Completed 只写入固定类别及请求/交易标识，不记录正文或上游错误。
func (observer *SignerObserver) Completed(event signer.HTTPResult) {
	operation := event.Operation
	switch operation {
	case "transfer", "sweep":
	default:
		operation = "unknown"
	}
	result := event.Result
	switch result {
	case "success", "unauthorized", "conflict", "policy_denied", "failed", "invalid_request", "timeout", "canceled":
	default:
		result = "failed"
	}
	observer.requests.WithLabelValues(operation, result).Inc()
	observer.duration.WithLabelValues(operation).Observe(event.Duration.Seconds())
	observer.logger.Info("signer 请求审计", "operation", operation, "result", result,
		"request_id", event.RequestID, "transaction_id", event.TransactionID,
		"duration_ms", event.Duration.Milliseconds())
}
