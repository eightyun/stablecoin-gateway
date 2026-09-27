package config

import (
	"fmt"
	"time"
)

const (
	defaultMonitorOperationTimeout = 10 * time.Second
	defaultMonitorRefreshInterval  = 15 * time.Second
	defaultMonitorRetryMin         = time.Second
	defaultMonitorRetryMax         = 30 * time.Second
)

// MonitorConfig 保存业务风险指标采样进程配置。
type MonitorConfig struct {
	DatabaseURL      string
	OperationTimeout time.Duration
	RefreshInterval  time.Duration
	RetryMin         time.Duration
	RetryMax         time.Duration
}

// LoadMonitor 加载业务风险指标采样进程配置。
func LoadMonitor() (MonitorConfig, error) {
	databaseURL, err := requiredEnv("GATEWAY_DATABASE_URL")
	if err != nil {
		return MonitorConfig{}, err
	}
	operationTimeout, err := durationFromEnv("GATEWAY_MONITOR_OPERATION_TIMEOUT", defaultMonitorOperationTimeout)
	if err != nil {
		return MonitorConfig{}, err
	}
	refreshInterval, err := durationFromEnv("GATEWAY_MONITOR_REFRESH_INTERVAL", defaultMonitorRefreshInterval)
	if err != nil {
		return MonitorConfig{}, err
	}
	retryMin, err := durationFromEnv("GATEWAY_MONITOR_RETRY_MIN", defaultMonitorRetryMin)
	if err != nil {
		return MonitorConfig{}, err
	}
	retryMax, err := durationFromEnv("GATEWAY_MONITOR_RETRY_MAX", defaultMonitorRetryMax)
	if err != nil {
		return MonitorConfig{}, err
	}
	if retryMax < retryMin {
		return MonitorConfig{}, fmt.Errorf("业务监控重试区间无效")
	}
	return MonitorConfig{
		DatabaseURL: databaseURL, OperationTimeout: operationTimeout,
		RefreshInterval: refreshInterval, RetryMin: retryMin, RetryMax: retryMax,
	}, nil
}
