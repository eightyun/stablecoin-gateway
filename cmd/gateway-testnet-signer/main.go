package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/chain/tron/nodehttp"
	"github.com/eightyun/stablecoin-gateway/internal/config"
	"github.com/eightyun/stablecoin-gateway/internal/signer"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		slog.Error("测试网 signer 退出", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg, err := config.LoadTestnetSigner()
	if err != nil {
		return err
	}
	key, err := signer.LoadFileKey(cfg.PrivateKeyFile)
	if err != nil {
		return err
	}
	defer key.Close()
	if !key.MatchesAddress(cfg.OwnerAddress) {
		return signer.ErrInvalidKeyFile
	}
	store, err := signer.NewFileStore(cfg.StoreDirectory)
	if err != nil {
		return err
	}
	builder, err := nodehttp.NewBuilder(nodehttp.BuilderConfig{
		BaseURL: cfg.FullNodeURL, Network: cfg.Network, APIKey: cfg.NodeAPIKey,
		OwnerAddress: cfg.OwnerAddress, FeeLimit: cfg.FeeLimit,
		MaxTransactionLifetime: cfg.MaxTransactionLifetime,
		MaxResponseBytes:       cfg.NodeMaxResponseBytes,
	}, nil)
	if err != nil {
		return err
	}
	service, err := signer.NewService(signer.ServiceConfig{
		Network: cfg.Network, OwnerAddress: cfg.OwnerAddress,
		AllowedContracts: cfg.AllowedContracts, MaxAmount: cfg.MaxAmount,
		MaxFeeLimit: cfg.FeeLimit, MaxTransactionLifetime: cfg.MaxTransactionLifetime,
	}, builder, key, store)
	if err != nil {
		return err
	}
	handler, err := signer.NewHTTPHandler(signer.HTTPConfig{
		BearerToken: cfg.BearerToken, MaxRequestBodyBytes: cfg.MaxRequestBodyBytes,
	}, service)
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr: cfg.HTTPAddr, Handler: handler, ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: time.Minute,
	}
	serveErrors := make(chan error, 1)
	go func() {
		slog.Info("测试网 signer 已启动", "address", cfg.HTTPAddr, "network", cfg.Network, "owner", cfg.OwnerAddress)
		serveErrors <- server.ListenAndServeTLS(cfg.TLSCertFile, cfg.TLSKeyFile)
	}()
	select {
	case err := <-serveErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownContext); err != nil {
			return err
		}
		err := <-serveErrors
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
