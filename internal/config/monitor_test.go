package config

import (
	"testing"
	"time"
)

func TestLoadMonitorDefaults(t *testing.T) {
	t.Setenv("GATEWAY_DATABASE_URL", "postgres://gateway@example.com/gateway")
	t.Setenv("GATEWAY_MONITOR_OPERATION_TIMEOUT", "")
	t.Setenv("GATEWAY_MONITOR_REFRESH_INTERVAL", "")
	t.Setenv("GATEWAY_MONITOR_RETRY_MIN", "")
	t.Setenv("GATEWAY_MONITOR_RETRY_MAX", "")

	loaded, err := LoadMonitor()
	if err != nil {
		t.Fatalf("LoadMonitor() error = %v", err)
	}
	if loaded.OperationTimeout != 10*time.Second || loaded.RefreshInterval != 15*time.Second ||
		loaded.RetryMin != time.Second || loaded.RetryMax != 30*time.Second {
		t.Fatalf("LoadMonitor() = %+v", loaded)
	}
}

func TestLoadMonitorRejectsInvalidRetryRange(t *testing.T) {
	t.Setenv("GATEWAY_DATABASE_URL", "postgres://gateway@example.com/gateway")
	t.Setenv("GATEWAY_MONITOR_RETRY_MIN", "2s")
	t.Setenv("GATEWAY_MONITOR_RETRY_MAX", "1s")
	if _, err := LoadMonitor(); err == nil {
		t.Fatalf("LoadMonitor() error = %v", err)
	}
}
