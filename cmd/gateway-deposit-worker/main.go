package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/eightyun/stablecoin-gateway/internal/config"
	"github.com/eightyun/stablecoin-gateway/internal/database"
	"github.com/eightyun/stablecoin-gateway/internal/deposit"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		slog.Error("充值匹配进程退出", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg, err := config.LoadDepositWorker()
	if err != nil {
		return err
	}
	databaseConfig := database.DefaultConfig(cfg.DatabaseURL, "gateway-deposit-worker")
	databaseConfig.MinConnections = 1
	databaseConfig.MaxConnections = 5
	pool, err := database.Open(ctx, databaseConfig)
	if err != nil {
		return err
	}
	defer pool.Close()

	store, err := deposit.NewStore(pool)
	if err != nil {
		return err
	}
	worker, err := deposit.NewWorker(store, slog.Default(), deposit.WorkerConfig{
		OperationTimeout: cfg.OperationTimeout,
		IdleInterval:     cfg.IdleInterval,
		RetryMin:         cfg.RetryMin,
		RetryMax:         cfg.RetryMax,
		ExpireInterval:   cfg.ExpireInterval,
		ExpireBatchSize:  cfg.ExpireBatchSize,
	})
	if err != nil {
		return err
	}
	slog.Info("充值匹配进程已启动")
	return worker.Run(ctx)
}
