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
	"github.com/eightyun/stablecoin-gateway/internal/sweep"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		slog.Error("归集规划进程退出", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg, err := config.LoadSweepPlanner()
	if err != nil {
		return err
	}
	observabilityConfig, err := config.LoadObservability()
	if err != nil {
		return err
	}
	databaseConfig := database.DefaultConfig(cfg.DatabaseURL, "gateway-sweep-planner")
	databaseConfig.MinConnections = 1
	databaseConfig.MaxConnections = 4
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
	store, err := sweep.NewStore(pool)
	if err != nil {
		return err
	}
	worker, err := sweep.NewWorker(store, sweep.Policy{
		AssetID: cfg.AssetID, MinimumAmount: cfg.MinimumAmount, MaxSnapshotAge: cfg.MaxSnapshotAge,
	}, slog.Default(), sweep.WorkerConfig{
		OperationTimeout: cfg.OperationTimeout, IdleInterval: cfg.IdleInterval,
		RetryMin: cfg.RetryMin, RetryMax: cfg.RetryMax, Observer: monitoring.WorkerObserver(),
	})
	if err != nil {
		return err
	}
	slog.Info("归集规划进程已启动", "asset_id", cfg.AssetID,
		"minimum_amount", cfg.MinimumAmount, "observability_addr", observabilityConfig.Addr)
	return monitoring.Run(ctx, worker.Run)
}
