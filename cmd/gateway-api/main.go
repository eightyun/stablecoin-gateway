package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/eightyun/stablecoin-gateway/internal/config"
	"github.com/eightyun/stablecoin-gateway/internal/database"
	"github.com/eightyun/stablecoin-gateway/internal/deposit"
	"github.com/eightyun/stablecoin-gateway/internal/merchantauth"
	"github.com/eightyun/stablecoin-gateway/internal/payout"
	httptransport "github.com/eightyun/stablecoin-gateway/internal/transport/http"
)

func main() {
	if err := run(); err != nil {
		slog.Error("服务退出", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadAPI()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	databaseConfig := database.DefaultConfig(cfg.DatabaseURL, "gateway-api")
	pool, err := database.Open(ctx, databaseConfig)
	if err != nil {
		return err
	}
	defer pool.Close()
	keyring, err := merchantauth.NewKeyring(cfg.APIKeyEncryptionKeys, cfg.APIKeyActiveVersion)
	if err != nil {
		return err
	}
	authStore, err := merchantauth.NewStore(pool)
	if err != nil {
		return err
	}
	authenticator, err := merchantauth.NewAuthenticator(authStore, keyring, cfg.AuthClockSkew)
	if err != nil {
		return err
	}
	depositStore, err := deposit.NewStore(pool)
	if err != nil {
		return err
	}
	payoutStore, err := payout.NewStore(pool)
	if err != nil {
		return err
	}
	handler, err := httptransport.NewHandler(authenticator, depositStore, payoutStore, cfg.MaxRequestBodyBytes)
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("HTTP 服务已启动", "addr", cfg.HTTPAddr)
		errCh <- server.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return err
	}

	err = <-errCh
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
