package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/eightyun/stablecoin-gateway/internal/database"
	"github.com/eightyun/stablecoin-gateway/internal/recovery"
	"github.com/eightyun/stablecoin-gateway/migrations"
)

var errAuditFailed = errors.New("恢复数据库未通过只读资金一致性审计")

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Getenv("GATEWAY_DATABASE_URL"), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, databaseURL string, output io.Writer) error {
	databaseURL = strings.TrimSpace(databaseURL)
	if databaseURL == "" {
		return errors.New("GATEWAY_DATABASE_URL 不能为空")
	}
	expectedVersion, err := migrations.LatestVersion()
	if err != nil {
		return err
	}
	databaseConfig := database.DefaultConfig(databaseURL, "gateway-recovery-audit")
	databaseConfig.MinConnections = 1
	databaseConfig.MaxConnections = 2
	pool, err := database.Open(ctx, databaseConfig)
	if err != nil {
		return err
	}
	defer pool.Close()
	store, err := recovery.NewStore(pool)
	if err != nil {
		return err
	}
	report, err := store.Audit(ctx, expectedVersion)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		return fmt.Errorf("输出恢复审计报告: %w", err)
	}
	if !report.Passed {
		return errAuditFailed
	}
	return nil
}
