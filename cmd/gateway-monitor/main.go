package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/eightyun/stablecoin-gateway/internal/background"
	"github.com/eightyun/stablecoin-gateway/internal/config"
	"github.com/eightyun/stablecoin-gateway/internal/database"
	"github.com/eightyun/stablecoin-gateway/internal/monitoring"
	"github.com/eightyun/stablecoin-gateway/internal/observability"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		slog.Error("业务监控进程退出", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg, err := config.LoadMonitor()
	if err != nil {
		return err
	}
	observabilityConfig, err := config.LoadObservability()
	if err != nil {
		return err
	}
	databaseConfig := database.DefaultConfig(cfg.DatabaseURL, "gateway-monitor")
	databaseConfig.MinConnections = 1
	databaseConfig.MaxConnections = 3
	pool, err := database.Open(ctx, databaseConfig)
	if err != nil {
		return err
	}
	defer pool.Close()
	service, err := observability.New(observability.Config{
		Addr: observabilityConfig.Addr, ReadHeaderTimeout: observabilityConfig.ReadHeaderTimeout,
		ReadinessTimeout: observabilityConfig.ReadinessTimeout, ShutdownTimeout: observabilityConfig.ShutdownTimeout,
	}, pool)
	if err != nil {
		return err
	}
	store, err := monitoring.NewStore(pool)
	if err != nil {
		return err
	}
	metrics := monitoring.NewMetrics()
	if err := service.Register(metrics); err != nil {
		return err
	}
	sampler, err := monitoring.NewSampler(store, metrics)
	if err != nil {
		return err
	}
	runner, err := background.NewRunner(sampler, slog.Default(), background.Config{
		Name: "business-monitor", OperationTimeout: cfg.OperationTimeout,
		IdleInterval: cfg.RefreshInterval, RetryMin: cfg.RetryMin, RetryMax: cfg.RetryMax,
		Observer: service.WorkerObserver(),
	}, nil)
	if err != nil {
		return err
	}
	slog.Info("业务监控进程已启动", "observability_addr", observabilityConfig.Addr,
		"refresh_interval", cfg.RefreshInterval)
	return service.Run(ctx, runner.Run)
}
