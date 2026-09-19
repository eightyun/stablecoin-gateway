package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	defaultHTTPAddr          = "127.0.0.1:8080"
	defaultReadHeaderTimeout = 5 * time.Second
	defaultShutdownTimeout   = 10 * time.Second
)

// Config 保存服务运行所需的非敏感配置。
type Config struct {
	HTTPAddr          string
	ReadHeaderTimeout time.Duration
	ShutdownTimeout   time.Duration
}

// Load 从环境变量读取配置，并对关键值进行校验。
func Load() (Config, error) {
	readHeaderTimeout, err := durationFromEnv("GATEWAY_READ_HEADER_TIMEOUT", defaultReadHeaderTimeout)
	if err != nil {
		return Config{}, err
	}

	shutdownTimeout, err := durationFromEnv("GATEWAY_SHUTDOWN_TIMEOUT", defaultShutdownTimeout)
	if err != nil {
		return Config{}, err
	}

	httpAddr := strings.TrimSpace(os.Getenv("GATEWAY_HTTP_ADDR"))
	if httpAddr == "" {
		httpAddr = defaultHTTPAddr
	}

	return Config{
		HTTPAddr:          httpAddr,
		ReadHeaderTimeout: readHeaderTimeout,
		ShutdownTimeout:   shutdownTimeout,
	}, nil
}

func durationFromEnv(name string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}

	value, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("解析配置 %s: %w", name, err)
	}
	if value <= 0 {
		return 0, fmt.Errorf("配置 %s 必须大于 0", name)
	}

	return value, nil
}
