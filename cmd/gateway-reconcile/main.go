package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/eightyun/stablecoin-gateway/internal/config"
	"github.com/eightyun/stablecoin-gateway/internal/database"
	"github.com/eightyun/stablecoin-gateway/internal/reconciliation"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		slog.Error("对账任务退出", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg, err := config.LoadReconciliation()
	if err != nil {
		return err
	}
	databaseConfig := database.DefaultConfig(cfg.DatabaseURL, "gateway-reconcile")
	databaseConfig.MinConnections = 1
	databaseConfig.MaxConnections = 3
	pool, err := database.Open(ctx, databaseConfig)
	if err != nil {
		return err
	}
	defer pool.Close()
	store, err := reconciliation.NewStore(pool)
	if err != nil {
		return err
	}
	result, err := store.RunLedgerIntegrity(ctx)
	if err != nil {
		return err
	}
	log := slog.Info
	if result.FindingCount > 0 {
		log = slog.Warn
	}
	log("对账任务完成",
		"run_id", result.RunID,
		"kind", result.Kind,
		"checked_items", result.CheckedItems,
		"finding_count", result.FindingCount,
	)
	return nil
}
