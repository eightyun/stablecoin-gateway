package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/acceptance"
	"github.com/eightyun/stablecoin-gateway/internal/chain/tron/nodehttp"
	"github.com/eightyun/stablecoin-gateway/internal/chain/tron/signhttp"
	"github.com/eightyun/stablecoin-gateway/internal/config"
	"github.com/eightyun/stablecoin-gateway/internal/database"
	"github.com/eightyun/stablecoin-gateway/internal/reconciliation"
	"github.com/eightyun/stablecoin-gateway/internal/sweep"
	"github.com/eightyun/stablecoin-gateway/internal/wallet"
)

type result struct {
	Network             string    `json:"network"`
	AssetID             string    `json:"asset_id"`
	PlanID              string    `json:"plan_id"`
	TransactionID       string    `json:"transaction_id"`
	Amount              string    `json:"amount"`
	InitialSnapshotID   string    `json:"initial_snapshot_id"`
	FinalSnapshotID     string    `json:"final_snapshot_id"`
	ReconciliationRunID string    `json:"reconciliation_run_id"`
	CompletedAt         time.Time `json:"completed_at"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(parent context.Context) error {
	cfg, err := config.LoadSweepAcceptance()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, cfg.Timeout)
	defer cancel()
	databaseConfig := database.DefaultConfig(cfg.Planner.DatabaseURL, "gateway-sweep-acceptance")
	databaseConfig.MinConnections = 1
	databaseConfig.MaxConnections = 6
	pool, err := database.Open(ctx, databaseConfig)
	if err != nil {
		return err
	}
	defer pool.Close()

	walletStore, err := wallet.NewStore(pool)
	if err != nil {
		return err
	}
	sweepStore, err := sweep.NewStore(pool)
	if err != nil {
		return err
	}
	reconciliationStore, err := reconciliation.NewStore(pool)
	if err != nil {
		return err
	}
	asset, wallets, err := walletStore.ActiveWallets(ctx, cfg.Planner.AssetID)
	if err != nil {
		return err
	}
	if err := acceptance.ValidateSweepWallets(
		asset, wallets, cfg.Signing.Network, cfg.SourceAddress, cfg.DestinationAddress,
	); err != nil {
		return err
	}
	reader, err := nodehttp.New(nodehttp.Config{
		BaseURL: cfg.Signing.SolidityNodeURL, Network: cfg.Signing.Network,
		APIKey: cfg.Signing.NodeAPIKey, MaxResponseBytes: cfg.Signing.NodeMaxResponseBytes,
	}, nil)
	if err != nil {
		return err
	}
	broadcaster, err := nodehttp.NewWriter(nodehttp.WriterConfig{
		BaseURL: cfg.Execution.FullNodeURL, APIKey: cfg.Execution.NodeAPIKey,
		MaxResponseBytes: cfg.Execution.BroadcastMaxResponseBytes,
	}, nil)
	if err != nil {
		return err
	}
	signer, err := signhttp.NewSweep(signhttp.Config{
		BaseURL: cfg.Signing.SignerURL, BearerToken: cfg.Signing.SignerBearerToken,
		MaxFeeLimit:            cfg.Signing.SignerMaxFeeLimit,
		MaxTransactionLifetime: cfg.Signing.SignerMaxLifetime,
		MaxResponseBytes:       cfg.Signing.SignerMaxResponseBytes, CAFile: cfg.Signing.SignerCAFile,
		ClientCertificateFile: cfg.Signing.SignerClientCertFile,
		ClientKeyFile:         cfg.Signing.SignerClientKeyFile,
	}, nil)
	if err != nil {
		return err
	}

	initialSnapshot, err := captureSnapshot(ctx, walletStore, reader, asset, wallets)
	if err != nil {
		return err
	}
	plan, err := sweepStore.PlanNext(ctx, sweep.Policy{
		AssetID: cfg.Planner.AssetID, MinimumAmount: cfg.Planner.MinimumAmount,
		MaximumAmount: cfg.MaximumAmount, SourceAddress: cfg.SourceAddress,
		DestinationAddress: cfg.DestinationAddress, MaxSnapshotAge: cfg.Planner.MaxSnapshotAge,
	})
	if err != nil {
		return err
	}
	signingWorker, err := sweep.NewSigningWorker(
		sweepStore, reader, signer, slog.Default(), cfg.Signing.WorkerID,
		sweep.SigningWorkerConfig{
			Network: cfg.Signing.Network, OperationTimeout: cfg.Signing.OperationTimeout,
			LeaseDuration: cfg.Signing.LeaseDuration, IdleInterval: cfg.Signing.IdleInterval,
			RetryMin: cfg.Signing.RetryMin, RetryMax: cfg.Signing.RetryMax,
		},
	)
	if err != nil {
		return err
	}
	executionWorker, err := sweep.NewExecutionWorker(
		sweepStore, broadcaster, reader, slog.Default(), cfg.Execution.WorkerID,
		sweep.ExecutionWorkerConfig{
			Network: cfg.Execution.Network, OperationTimeout: cfg.Execution.OperationTimeout,
			LeaseDuration:        cfg.Execution.LeaseDuration,
			ConfirmationInterval: cfg.Execution.ConfirmationInterval,
			IdleInterval:         cfg.Execution.IdleInterval,
			RetryMin:             cfg.Execution.RetryMin, RetryMax: cfg.Execution.RetryMax,
		},
	)
	if err != nil {
		return err
	}
	executionState, err := runWorkers(ctx, signingWorker, executionWorker, sweepStore, plan.ID, cfg.PollInterval)
	if err != nil {
		return err
	}
	finalSnapshot, err := captureSnapshot(ctx, walletStore, reader, asset, wallets)
	if err != nil {
		return err
	}
	reconciliationResult, err := reconciliationStore.RunWalletAssets(ctx)
	if err != nil {
		return err
	}
	if reconciliationResult.FindingCount != 0 {
		return fmt.Errorf("归集后钱包资产对账发现 %d 项差异", reconciliationResult.FindingCount)
	}
	output := result{
		Network: asset.Network, AssetID: asset.ID, PlanID: plan.ID,
		TransactionID: executionState.TransactionID, Amount: plan.Amount,
		InitialSnapshotID: initialSnapshot.ID, FinalSnapshotID: finalSnapshot.ID,
		ReconciliationRunID: reconciliationResult.RunID, CompletedAt: time.Now().UTC(),
	}
	if err := json.NewEncoder(os.Stdout).Encode(output); err != nil {
		return fmt.Errorf("输出归集验收结果: %w", err)
	}
	return nil
}

func captureSnapshot(
	ctx context.Context,
	store *wallet.Store,
	reader *nodehttp.Client,
	asset wallet.Asset,
	wallets []wallet.Wallet,
) (wallet.Snapshot, error) {
	snapshot, err := store.CaptureSnapshot(ctx, reader, asset, wallets)
	if err != nil {
		return wallet.Snapshot{}, err
	}
	if err := store.SaveSnapshot(ctx, snapshot); err != nil {
		return wallet.Snapshot{}, err
	}
	return snapshot, nil
}

func runWorkers(
	ctx context.Context,
	signingWorker *sweep.SigningWorker,
	executionWorker *sweep.ExecutionWorker,
	store *sweep.Store,
	planID string,
	pollInterval time.Duration,
) (sweep.ExecutionState, error) {
	workerContext, stopWorkers := context.WithCancel(ctx)
	workerErrors := make(chan error, 2)
	go func() { workerErrors <- signingWorker.Run(workerContext) }()
	go func() { workerErrors <- executionWorker.Run(workerContext) }()
	type waitResult struct {
		state sweep.ExecutionState
		err   error
	}
	completed := make(chan waitResult, 1)
	go func() {
		state, err := acceptance.WaitForSweep(workerContext, store, planID, pollInterval)
		completed <- waitResult{state: state, err: err}
	}()
	var state sweep.ExecutionState
	var runErr error
	stoppedWorkers := 0
	select {
	case outcome := <-completed:
		state, runErr = outcome.state, outcome.err
	case workerErr := <-workerErrors:
		stoppedWorkers = 1
		if workerErr == nil && ctx.Err() != nil {
			runErr = ctx.Err()
		} else if workerErr == nil {
			runErr = errors.New("归集验收 Worker 意外停止")
		} else {
			runErr = workerErr
		}
	case <-ctx.Done():
		runErr = ctx.Err()
	}
	stopWorkers()
	shutdownTimer := time.NewTimer(5 * time.Second)
	defer shutdownTimer.Stop()
	for stoppedWorkers < 2 {
		select {
		case <-workerErrors:
			stoppedWorkers++
		case <-shutdownTimer.C:
			return sweep.ExecutionState{}, errors.New("归集验收 Worker 未能及时停止")
		}
	}
	return state, runErr
}
