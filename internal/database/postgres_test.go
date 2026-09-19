package database

import (
	"errors"
	"testing"
	"time"
)

func TestDefaultConfig(t *testing.T) {
	config := DefaultConfig("postgres://localhost/gateway", "gateway-api")
	if config.URL != "postgres://localhost/gateway" {
		t.Fatalf("URL = %q", config.URL)
	}
	if config.ApplicationName != "gateway-api" {
		t.Fatalf("ApplicationName = %q", config.ApplicationName)
	}
	if err := validateConfig(config); err != nil {
		t.Fatalf("validateConfig() error = %v", err)
	}
}

func TestValidateConfig(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr error
	}{
		{
			name: "缺少连接地址",
			mutate: func(config *Config) {
				config.URL = " "
			},
			wantErr: ErrURLRequired,
		},
		{
			name: "最小连接数大于最大连接数",
			mutate: func(config *Config) {
				config.MinConnections = 21
			},
			wantErr: ErrInvalidPoolSize,
		},
		{
			name: "连接超时无效",
			mutate: func(config *Config) {
				config.ConnectTimeout = 0
			},
			wantErr: ErrInvalidPoolTimes,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := DefaultConfig("postgres://localhost/gateway", "gateway-api")
			test.mutate(&config)
			if err := validateConfig(config); !errors.Is(err, test.wantErr) {
				t.Fatalf("validateConfig() error = %v, 期望 %v", err, test.wantErr)
			}
		})
	}
}

func TestValidateConfigRejectsInvalidLifetime(t *testing.T) {
	config := DefaultConfig("postgres://localhost/gateway", "gateway-api")
	config.MaxConnectionLifetime = -time.Second
	if err := validateConfig(config); !errors.Is(err, ErrInvalidPoolTimes) {
		t.Fatalf("validateConfig() error = %v, 期望 %v", err, ErrInvalidPoolTimes)
	}
}
