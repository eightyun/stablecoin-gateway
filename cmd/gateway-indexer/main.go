package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/eightyun/stablecoin-gateway/internal/chain/tron/indexer"
	"github.com/eightyun/stablecoin-gateway/internal/chain/tron/nodehttp"
	"github.com/eightyun/stablecoin-gateway/internal/config"
	"github.com/eightyun/stablecoin-gateway/internal/database"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		slog.Error("链扫描进程退出", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg, err := config.LoadIndexer()
	if err != nil {
		return err
	}
	reader, err := nodehttp.New(nodehttp.Config{
		BaseURL:          cfg.NodeURL,
		Network:          cfg.Network,
		APIKey:           cfg.NodeAPIKey,
		MaxResponseBytes: cfg.MaxResponseBytes,
	}, nil)
	if err != nil {
		return err
	}
	databaseConfig := database.DefaultConfig(cfg.DatabaseURL, "gateway-indexer")
	databaseConfig.MinConnections = 1
	databaseConfig.MaxConnections = 5
	pool, err := database.Open(ctx, databaseConfig)
	if err != nil {
		return err
	}
	defer pool.Close()

	store, err := indexer.NewCursorStore(pool)
	if err != nil {
		return err
	}
	scanner, err := indexer.NewScanner(store, reader, cfg.Network, cfg.Contract, cfg.WorkerID, cfg.LeaseDuration)
	if err != nil {
		return err
	}
	worker, err := indexer.NewWorker(scanner, slog.Default(), indexer.WorkerConfig{
		StepTimeout:  cfg.StepTimeout,
		IdleInterval: cfg.IdleInterval,
		RetryMin:     cfg.RetryMin,
		RetryMax:     cfg.RetryMax,
	})
	if err != nil {
		return err
	}
	if err := store.Ensure(ctx, cfg.Network, cfg.StartHeight, cfg.AnchorHash); err != nil {
		return err
	}
	slog.Info("TRON 扫描进程已启动", "network", cfg.Network, "contract", cfg.Contract, "worker", cfg.WorkerID)
	return worker.Run(ctx)
}
