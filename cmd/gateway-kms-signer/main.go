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

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/eightyun/stablecoin-gateway/internal/chain/tron/nodehttp"
	"github.com/eightyun/stablecoin-gateway/internal/config"
	"github.com/eightyun/stablecoin-gateway/internal/database"
	"github.com/eightyun/stablecoin-gateway/internal/observability"
	"github.com/eightyun/stablecoin-gateway/internal/signer"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		slog.Error("KMS signer 退出", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg, err := config.LoadKMSSigner()
	if err != nil {
		return err
	}
	monitoringConfig, err := config.LoadObservability()
	if err != nil {
		return err
	}
	databaseConfig := database.DefaultConfig(cfg.DatabaseURL, "gateway-kms-signer")
	databaseConfig.MinConnections = 1
	databaseConfig.MaxConnections = 10
	pool, err := database.Open(ctx, databaseConfig)
	if err != nil {
		return err
	}
	defer pool.Close()
	store, err := signer.NewPostgresStore(pool)
	if err != nil {
		return err
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.AWSRegion))
	if err != nil {
		return err
	}
	key, err := signer.NewAWSKMSKey(ctx, kms.NewFromConfig(awsCfg), cfg.AWSKMSKeyID)
	if err != nil {
		return err
	}
	monitoring, err := observability.New(observability.Config{
		Addr: monitoringConfig.Addr, ReadHeaderTimeout: monitoringConfig.ReadHeaderTimeout,
		ReadinessTimeout: monitoringConfig.ReadinessTimeout, ShutdownTimeout: monitoringConfig.ShutdownTimeout,
	}, signerReadiness{database: pool, key: key})
	if err != nil {
		return err
	}
	observer, err := monitoring.NewSignerObserver(slog.Default())
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
	service, err := signer.NewProductionService(signer.ServiceConfig{
		Network: cfg.Network, OwnerAddress: cfg.OwnerAddress,
		AllowedContracts: cfg.AllowedContracts, MaxAmount: cfg.MaxAmount,
		MaxFeeLimit: cfg.FeeLimit, MaxTransactionLifetime: cfg.MaxTransactionLifetime,
	}, builder, key, store)
	if err != nil {
		return err
	}
	handler, err := signer.NewHTTPHandler(signer.HTTPConfig{
		BearerToken: cfg.BearerToken, MaxRequestBodyBytes: cfg.MaxRequestBodyBytes,
		RequestTimeout: cfg.RequestTimeout, Observer: observer,
	}, service)
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr: cfg.HTTPAddr, Handler: handler, ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout: 10 * time.Second, WriteTimeout: cfg.RequestTimeout + 5*time.Second, IdleTimeout: time.Minute,
	}
	slog.Info("KMS signer 开始运行", "address", cfg.HTTPAddr, "network", cfg.Network,
		"observability_addr", monitoringConfig.Addr)
	return monitoring.Run(ctx, func(workloadCtx context.Context) error {
		return serveTLS(workloadCtx, server, cfg.TLSCertFile, cfg.TLSKeyFile, cfg.ShutdownTimeout)
	})
}

type signerReadiness struct {
	database observability.ReadinessChecker
	key      observability.ReadinessChecker
}

func (checker signerReadiness) Ping(ctx context.Context) error {
	if err := checker.database.Ping(ctx); err != nil {
		return err
	}
	return checker.key.Ping(ctx)
}

func serveTLS(ctx context.Context, server *http.Server, certFile, keyFile string, shutdownTimeout time.Duration) error {
	serveErrors := make(chan error, 1)
	go func() {
		serveErrors <- server.ListenAndServeTLS(certFile, keyFile)
	}()
	select {
	case err := <-serveErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownContext); err != nil {
			return errors.Join(err, server.Close())
		}
		err := <-serveErrors
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
