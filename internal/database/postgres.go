// Package database 提供 PostgreSQL 连接池初始化能力。
package database

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrURLRequired      = errors.New("PostgreSQL 连接地址不能为空")
	ErrInvalidPoolSize  = errors.New("PostgreSQL 连接池大小无效")
	ErrInvalidPoolTimes = errors.New("PostgreSQL 连接池时间配置无效")
)

// Config 定义 PostgreSQL 连接池配置。
type Config struct {
	URL                   string
	ApplicationName       string
	MinConnections        int32
	MaxConnections        int32
	MaxConnectionLifetime time.Duration
	MaxConnectionIdleTime time.Duration
	HealthCheckPeriod     time.Duration
	ConnectTimeout        time.Duration
}

// DefaultConfig 返回适合单实例起步的连接池配置。
func DefaultConfig(url, applicationName string) Config {
	return Config{
		URL:                   url,
		ApplicationName:       applicationName,
		MinConnections:        2,
		MaxConnections:        20,
		MaxConnectionLifetime: 30 * time.Minute,
		MaxConnectionIdleTime: 5 * time.Minute,
		HealthCheckPeriod:     30 * time.Second,
		ConnectTimeout:        5 * time.Second,
	}
}

// Open 创建连接池并验证数据库可达性。
func Open(ctx context.Context, config Config) (*pgxpool.Pool, error) {
	if err := validateConfig(config); err != nil {
		return nil, err
	}

	poolConfig, err := pgxpool.ParseConfig(config.URL)
	if err != nil {
		return nil, fmt.Errorf("解析 PostgreSQL 配置: %w", err)
	}
	poolConfig.MinConns = config.MinConnections
	poolConfig.MaxConns = config.MaxConnections
	poolConfig.MaxConnLifetime = config.MaxConnectionLifetime
	poolConfig.MaxConnIdleTime = config.MaxConnectionIdleTime
	poolConfig.HealthCheckPeriod = config.HealthCheckPeriod
	poolConfig.ConnConfig.ConnectTimeout = config.ConnectTimeout
	if applicationName := strings.TrimSpace(config.ApplicationName); applicationName != "" {
		poolConfig.ConnConfig.RuntimeParams["application_name"] = applicationName
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("创建 PostgreSQL 连接池: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("连接 PostgreSQL: %w", err)
	}
	return pool, nil
}

func validateConfig(config Config) error {
	if strings.TrimSpace(config.URL) == "" {
		return ErrURLRequired
	}
	if config.MinConnections < 0 || config.MaxConnections <= 0 || config.MinConnections > config.MaxConnections {
		return ErrInvalidPoolSize
	}
	if config.MaxConnectionLifetime <= 0 ||
		config.MaxConnectionIdleTime <= 0 ||
		config.HealthCheckPeriod <= 0 ||
		config.ConnectTimeout <= 0 {
		return ErrInvalidPoolTimes
	}
	return nil
}
