package config

import (
	"os"
	"strings"
	"time"
)

const (
	defaultObservabilityAddr             = "127.0.0.1:9090"
	defaultObservabilityReadinessTimeout = 2 * time.Second
)

// ObservabilityConfig 保存 Worker 指标和健康探针服务配置。
type ObservabilityConfig struct {
	Addr              string
	ReadHeaderTimeout time.Duration
	ReadinessTimeout  time.Duration
	ShutdownTimeout   time.Duration
}

// LoadObservability 加载进程级指标和健康探针配置。
func LoadObservability() (ObservabilityConfig, error) {
	readHeaderTimeout, err := durationFromEnv(
		"GATEWAY_OBSERVABILITY_READ_HEADER_TIMEOUT", defaultReadHeaderTimeout,
	)
	if err != nil {
		return ObservabilityConfig{}, err
	}
	readinessTimeout, err := durationFromEnv(
		"GATEWAY_OBSERVABILITY_READINESS_TIMEOUT", defaultObservabilityReadinessTimeout,
	)
	if err != nil {
		return ObservabilityConfig{}, err
	}
	shutdownTimeout, err := durationFromEnv(
		"GATEWAY_OBSERVABILITY_SHUTDOWN_TIMEOUT", defaultShutdownTimeout,
	)
	if err != nil {
		return ObservabilityConfig{}, err
	}
	addr := strings.TrimSpace(os.Getenv("GATEWAY_OBSERVABILITY_ADDR"))
	if addr == "" {
		addr = defaultObservabilityAddr
	}
	return ObservabilityConfig{
		Addr: addr, ReadHeaderTimeout: readHeaderTimeout,
		ReadinessTimeout: readinessTimeout, ShutdownTimeout: shutdownTimeout,
	}, nil
}
