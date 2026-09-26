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
	"github.com/eightyun/stablecoin-gateway/internal/outbox"
	"github.com/eightyun/stablecoin-gateway/internal/secretbox"
	"github.com/eightyun/stablecoin-gateway/internal/webhook"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		slog.Error("Webhook Worker 退出", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg, err := config.LoadWebhookWorker()
	if err != nil {
		return err
	}
	databaseConfig := database.DefaultConfig(cfg.DatabaseURL, "gateway-webhook-worker")
	databaseConfig.MinConnections = 1
	databaseConfig.MaxConnections = 5
	pool, err := database.Open(ctx, databaseConfig)
	if err != nil {
		return err
	}
	defer pool.Close()
	keyring, err := secretbox.NewKeyring(cfg.EncryptionKeys, cfg.ActiveKeyVersion)
	if err != nil {
		return err
	}
	webhookStore, err := webhook.NewStore(pool)
	if err != nil {
		return err
	}
	sender, err := webhook.NewHTTPSender(webhook.HTTPSenderConfig{
		Timeout: cfg.RequestTimeout, MaxResponseBodyBytes: cfg.MaxResponseBodyBytes,
		AllowPrivateNetworks: cfg.AllowPrivateNetworks,
	})
	if err != nil {
		return err
	}
	handler, err := webhook.NewHandler(webhookStore, keyring, sender)
	if err != nil {
		return err
	}
	outboxStore, err := outbox.NewPostgreSQLStore(pool)
	if err != nil {
		return err
	}
	processor, err := outbox.NewProcessor(outboxStore, outbox.Config{
		WorkerID: cfg.WorkerID, BatchSize: cfg.BatchSize, LeaseDuration: cfg.LeaseDuration,
		MaxAttempts: cfg.MaxAttempts, BaseBackoff: cfg.BaseBackoff, MaxBackoff: cfg.MaxBackoff,
	}, map[string]outbox.Handler{"deposit.confirmed": handler})
	if err != nil {
		return err
	}
	operation := background.OperationFunc(func(operationCtx context.Context) (bool, error) {
		count, runErr := processor.RunOnce(operationCtx)
		return count > 0, runErr
	})
	runner, err := background.NewRunner(operation, slog.Default(), background.Config{
		Name: "webhook-delivery", OperationTimeout: cfg.OperationTimeout,
		IdleInterval: cfg.IdleInterval, RetryMin: cfg.RetryMin, RetryMax: cfg.RetryMax,
	}, nil)
	if err != nil {
		return err
	}
	slog.Info("Webhook Worker 已启动", "worker_id", cfg.WorkerID)
	return runner.Run(ctx)
}
