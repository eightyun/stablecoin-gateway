package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/eightyun/stablecoin-gateway/internal/config"
	"github.com/eightyun/stablecoin-gateway/internal/database"
	"github.com/eightyun/stablecoin-gateway/internal/observability"
	"github.com/eightyun/stablecoin-gateway/internal/screening"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		slog.Error("入金地址筛查进程退出", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg, err := config.LoadDepositScreeningWorker()
	if err != nil {
		return err
	}
	observabilityConfig, err := config.LoadObservability()
	if err != nil {
		return err
	}
	databaseConfig := database.DefaultConfig(cfg.DatabaseURL, "gateway-deposit-screening-worker")
	databaseConfig.MinConnections = 1
	databaseConfig.MaxConnections = 5
	pool, err := database.Open(ctx, databaseConfig)
	if err != nil {
		return err
	}
	defer pool.Close()
	monitoring, err := observability.New(observability.Config{
		Addr: observabilityConfig.Addr, ReadHeaderTimeout: observabilityConfig.ReadHeaderTimeout,
		ReadinessTimeout: observabilityConfig.ReadinessTimeout,
		ShutdownTimeout:  observabilityConfig.ShutdownTimeout,
	}, pool)
	if err != nil {
		return err
	}
	store, err := screening.NewInboundStore(pool)
	if err != nil {
		return err
	}
	provider, err := screening.NewHTTPProvider(screening.HTTPConfig{
		BaseURL: cfg.ProviderURL, BearerToken: cfg.ProviderToken,
		ProviderName: cfg.ProviderName, RequestTimeout: cfg.RequestTimeout,
		MaxResponseBytes: cfg.MaxResponseBytes, MaxResultValidity: cfg.MaxResultValidity,
	}, nil)
	if err != nil {
		return err
	}
	worker, err := screening.NewInboundWorker(store, provider, slog.Default(), cfg.WorkerID, screening.WorkerConfig{
		OperationTimeout: cfg.OperationTimeout, LeaseDuration: cfg.LeaseDuration,
		IdleInterval: cfg.IdleInterval, RetryMin: cfg.RetryMin, RetryMax: cfg.RetryMax,
		Observer: monitoring.WorkerObserver(),
	})
	if err != nil {
		return err
	}
	slog.Info("入金地址筛查进程已启动", "worker_id", cfg.WorkerID,
		"provider", cfg.ProviderName, "observability_addr", observabilityConfig.Addr)
	return monitoring.Run(ctx, worker.Run)
}
