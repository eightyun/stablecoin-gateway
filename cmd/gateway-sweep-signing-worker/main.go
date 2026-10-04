package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/eightyun/stablecoin-gateway/internal/chain/tron/nodehttp"
	"github.com/eightyun/stablecoin-gateway/internal/chain/tron/signhttp"
	"github.com/eightyun/stablecoin-gateway/internal/config"
	"github.com/eightyun/stablecoin-gateway/internal/database"
	"github.com/eightyun/stablecoin-gateway/internal/observability"
	"github.com/eightyun/stablecoin-gateway/internal/sweep"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		slog.Error("归集签名进程退出", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg, err := config.LoadSweepSigningWorker()
	if err != nil {
		return err
	}
	monitoringConfig, err := config.LoadObservability()
	if err != nil {
		return err
	}
	databaseConfig := database.DefaultConfig(cfg.DatabaseURL, "gateway-sweep-signing-worker")
	databaseConfig.MinConnections = 1
	databaseConfig.MaxConnections = 5
	pool, err := database.Open(ctx, databaseConfig)
	if err != nil {
		return err
	}
	defer pool.Close()
	monitoring, err := observability.New(observability.Config{
		Addr: monitoringConfig.Addr, ReadHeaderTimeout: monitoringConfig.ReadHeaderTimeout,
		ReadinessTimeout: monitoringConfig.ReadinessTimeout, ShutdownTimeout: monitoringConfig.ShutdownTimeout,
	}, pool)
	if err != nil {
		return err
	}
	store, err := sweep.NewStore(pool)
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
	signer, err := signhttp.NewSweep(signhttp.Config{
		BaseURL: cfg.SignerURL, BearerToken: cfg.SignerBearerToken,
		MaxFeeLimit: cfg.SignerMaxFeeLimit, MaxTransactionLifetime: cfg.SignerMaxLifetime,
		MaxResponseBytes: cfg.SignerMaxResponseBytes, CAFile: cfg.SignerCAFile,
		ClientCertificateFile: cfg.SignerClientCertFile, ClientKeyFile: cfg.SignerClientKeyFile,
	}, nil)
	if err != nil {
		return err
	}
	worker, err := sweep.NewSigningWorker(store, reader, signer, slog.Default(), cfg.WorkerID, sweep.SigningWorkerConfig{
		Network:          cfg.Network,
		OperationTimeout: cfg.OperationTimeout, LeaseDuration: cfg.LeaseDuration,
		IdleInterval: cfg.IdleInterval, RetryMin: cfg.RetryMin, RetryMax: cfg.RetryMax,
		Observer: monitoring.WorkerObserver(),
	})
	if err != nil {
		return err
	}
	slog.Info("归集签名进程已启动", "worker_id", cfg.WorkerID, "network", cfg.Network,
		"observability_addr", monitoringConfig.Addr)
	return monitoring.Run(ctx, worker.Run)
}
