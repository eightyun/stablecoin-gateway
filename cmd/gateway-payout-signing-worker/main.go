package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/eightyun/stablecoin-gateway/internal/chain/tron/signhttp"
	"github.com/eightyun/stablecoin-gateway/internal/config"
	"github.com/eightyun/stablecoin-gateway/internal/database"
	"github.com/eightyun/stablecoin-gateway/internal/payout"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		slog.Error("出款签名进程退出", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg, err := config.LoadPayoutSigningWorker()
	if err != nil {
		return err
	}
	databaseConfig := database.DefaultConfig(cfg.DatabaseURL, "gateway-payout-signing-worker")
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
	signer, err := signhttp.New(signhttp.Config{
		BaseURL: cfg.SignerURL, BearerToken: cfg.SignerBearerToken,
		MaxResponseBytes: cfg.SignerMaxResponseBytes,
	}, nil)
	if err != nil {
		return err
	}
	worker, err := payout.NewSigningWorker(store, signer, slog.Default(), cfg.WorkerID, payout.SigningWorkerConfig{
		OperationTimeout: cfg.OperationTimeout, LeaseDuration: cfg.LeaseDuration,
		IdleInterval: cfg.IdleInterval, RetryMin: cfg.RetryMin, RetryMax: cfg.RetryMax,
	})
	if err != nil {
		return err
	}
	slog.Info("出款签名进程已启动", "worker_id", cfg.WorkerID)
	return worker.Run(ctx)
}
