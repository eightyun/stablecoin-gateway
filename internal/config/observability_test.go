package config

import (
	"testing"
	"time"
)

func TestLoadObservability(t *testing.T) {
	clearObservabilityEnvironment(t)
	config, err := LoadObservability()
	if err != nil {
		t.Fatalf("LoadObservability() error = %v", err)
	}
	if config.Addr != "127.0.0.1:9090" || config.ReadHeaderTimeout != 5*time.Second ||
		config.ReadinessTimeout != 2*time.Second || config.ShutdownTimeout != 10*time.Second {
		t.Fatalf("LoadObservability() = %+v", config)
	}
	t.Setenv("GATEWAY_OBSERVABILITY_ADDR", "127.0.0.1:9190")
	t.Setenv("GATEWAY_OBSERVABILITY_READINESS_TIMEOUT", "3s")
	config, err = LoadObservability()
	if err != nil || config.Addr != "127.0.0.1:9190" || config.ReadinessTimeout != 3*time.Second {
		t.Fatalf("自定义 LoadObservability() = %+v, %v", config, err)
	}
}

func TestLoadObservabilityRejectsInvalidDuration(t *testing.T) {
	clearObservabilityEnvironment(t)
	t.Setenv("GATEWAY_OBSERVABILITY_SHUTDOWN_TIMEOUT", "0s")
	if _, err := LoadObservability(); err == nil {
		t.Fatal("LoadObservability() 未拒绝无效关闭超时")
	}
}

func clearObservabilityEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"GATEWAY_OBSERVABILITY_ADDR",
		"GATEWAY_OBSERVABILITY_READ_HEADER_TIMEOUT",
		"GATEWAY_OBSERVABILITY_READINESS_TIMEOUT",
		"GATEWAY_OBSERVABILITY_SHUTDOWN_TIMEOUT",
	} {
		t.Setenv(key, "")
	}
}
