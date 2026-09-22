package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"syscall"

	"github.com/eightyun/stablecoin-gateway/migrations"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Getenv("GATEWAY_DATABASE_URL"), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, databaseURL string, output io.Writer) (err error) {
	if len(args) != 1 || (args[0] != "up" && args[0] != "version") {
		return errors.New("用法: gateway-migrate up|version")
	}
	if databaseURL == "" {
		return errors.New("缺少 GATEWAY_DATABASE_URL")
	}

	parsedURL, err := url.Parse(databaseURL)
	if err != nil || (parsedURL.Scheme != "postgres" && parsedURL.Scheme != "postgresql") || parsedURL.Host == "" {
		return errors.New("GATEWAY_DATABASE_URL 必须是有效的 PostgreSQL URL")
	}
	parsedURL.Scheme = "pgx5"

	source, err := iofs.New(migrations.Files, ".")
	if err != nil {
		return fmt.Errorf("加载嵌入的迁移文件: %w", err)
	}
	migrator, err := migrate.NewWithSourceInstance("iofs", source, parsedURL.String())
	if err != nil {
		_ = source.Close()
		return fmt.Errorf("连接迁移数据库: %w", err)
	}
	defer func() {
		sourceErr, databaseErr := migrator.Close()
		err = errors.Join(err, sourceErr, databaseErr)
	}()

	stopWatching := make(chan struct{})
	defer close(stopWatching)
	go func() {
		select {
		case <-ctx.Done():
			select {
			case migrator.GracefulStop <- true:
			default:
			}
		case <-stopWatching:
		}
	}()

	if args[0] == "up" {
		err = migrator.Up()
		if errors.Is(err, migrate.ErrNoChange) {
			fmt.Fprintln(output, "数据库已是最新版本")
			return nil
		}
		if err != nil {
			return fmt.Errorf("执行数据库迁移: %w", err)
		}
	}

	version, dirty, err := migrator.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		fmt.Fprintln(output, "尚未应用迁移")
		return nil
	}
	if err != nil {
		return fmt.Errorf("查询数据库迁移版本: %w", err)
	}
	if dirty {
		return fmt.Errorf("数据库迁移版本 %d 处于 dirty 状态，需要人工核查", version)
	}
	fmt.Fprintf(output, "数据库迁移版本: %d\n", version)
	return nil
}
