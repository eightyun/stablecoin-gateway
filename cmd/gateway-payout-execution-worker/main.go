package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/eightyun/stablecoin-gateway/internal/chain/tron/nodehttp"
	"github.com/eightyun/stablecoin-gateway/internal/config"
	"github.com/eightyun/stablecoin-gateway/internal/database"
	"github.com/eightyun/stablecoin-gateway/internal/payout"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		slog.Error("出款执行进程退出", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg, err := config.LoadPayoutExecutionWorker()
	if err != nil {
		return err
	}
	databaseConfig := database.DefaultConfig(cfg.DatabaseURL, "gateway-payout-execution-worker")
	databaseConfig.MinConnections = 1
	databaseConfig.MaxConnections = 5
	pool, err := database.Open(ctx, databaseConfig)
	if err != nil {
		return err
	}
	defer pool.Close()
	store, err := payout.NewStore(pool)
	if err != nil {
		return err
	}
	broadcaster, err := nodehttp.NewWriter(nodehttp.WriterConfig{
		BaseURL: cfg.FullNodeURL, APIKey: cfg.NodeAPIKey,
		MaxResponseBytes: cfg.NodeMaxResponseBytes,
	}, nil)
	if err != nil {
		return err
	}
	reader, err := nodehttp.New(nodehttp.Config{
		BaseURL: cfg.SolidityNodeURL, Network: cfg.Network,
		APIKey: cfg.NodeAPIKey, MaxResponseBytes: cfg.NodeMaxResponseBytes,
	}, nil)
	if err != nil {
		return err
	}
	worker, err := payout.NewExecutionWorker(store, broadcaster, reader, slog.Default(), cfg.WorkerID, payout.ExecutionWorkerConfig{
		OperationTimeout: cfg.OperationTimeout, LeaseDuration: cfg.LeaseDuration,
		ConfirmationInterval: cfg.ConfirmationInterval, IdleInterval: cfg.IdleInterval,
		RetryMin: cfg.RetryMin, RetryMax: cfg.RetryMax,
	})
	if err != nil {
		return err
	}
	slog.Info("出款执行进程已启动", "worker_id", cfg.WorkerID, "network", cfg.Network)
	return worker.Run(ctx)
}
