package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/chain/tron/nodehttp"
	"github.com/eightyun/stablecoin-gateway/internal/config"
	"github.com/eightyun/stablecoin-gateway/internal/database"
	"github.com/eightyun/stablecoin-gateway/internal/wallet"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg, err := config.LoadWalletSnapshot()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	poolConfig := database.DefaultConfig(cfg.DatabaseURL, "gateway-wallet-snapshot")
	poolConfig.MinConnections = 1
	poolConfig.MaxConnections = 2
	pool, err := database.Open(ctx, poolConfig)
	if err != nil {
		return err
	}
	defer pool.Close()
	store, err := wallet.NewStore(pool)
	if err != nil {
		return err
	}
	asset, wallets, err := store.ActiveWallets(ctx, cfg.AssetID)
	if err != nil {
		return err
	}
	reader, err := nodehttp.New(nodehttp.Config{
		BaseURL: cfg.SolidityNodeURL, Network: asset.Network,
		APIKey: cfg.NodeAPIKey, MaxResponseBytes: cfg.NodeMaxResponseBytes,
	}, nil)
	if err != nil {
		return err
	}
	snapshot, err := wallet.CollectSnapshot(ctx, reader, asset, wallets)
	if err != nil {
		return err
	}
	if err := store.SaveSnapshot(ctx, snapshot); err != nil {
		return err
	}
	result := struct {
		RunID        string    `json:"run_id"`
		AssetID      string    `json:"asset_id"`
		BlockHeight  uint64    `json:"block_height"`
		BlockHash    string    `json:"block_hash"`
		BlockTime    time.Time `json:"block_time"`
		WalletCount  int       `json:"wallet_count"`
		TotalBalance string    `json:"total_balance"`
	}{
		RunID: snapshot.ID, AssetID: snapshot.Asset.ID,
		BlockHeight: snapshot.Block.Height, BlockHash: snapshot.Block.Hash,
		BlockTime: snapshot.Block.Timestamp, WalletCount: len(snapshot.Balances),
		TotalBalance: snapshot.TotalBalance,
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		return fmt.Errorf("输出钱包余额快照: %w", err)
	}
	return nil
}
