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
	"github.com/eightyun/stablecoin-gateway/internal/chain/tron/preflight"
	"github.com/eightyun/stablecoin-gateway/internal/config"
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
	cfg, err := config.LoadTRONPreflight()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	fullNode, err := nodehttp.New(nodehttp.Config{
		BaseURL: cfg.FullNodeURL, Network: cfg.Network,
		APIKey: cfg.NodeAPIKey, MaxResponseBytes: cfg.NodeMaxResponseBytes,
	}, nil)
	if err != nil {
		return err
	}
	solidityNode, err := nodehttp.New(nodehttp.Config{
		BaseURL: cfg.SolidityNodeURL, Network: cfg.Network,
		APIKey: cfg.NodeAPIKey, MaxResponseBytes: cfg.NodeMaxResponseBytes,
	}, nil)
	if err != nil {
		return err
	}
	result, err := preflight.Check(ctx, fullNode, solidityNode, preflight.Config{
		Network: cfg.Network, ContractAddress: cfg.ContractAddress,
		ExpectedSymbol: cfg.ExpectedSymbol, ExpectedDecimals: cfg.ExpectedDecimals,
		MaxFinalizedLag: cfg.MaxFinalizedLag, MaxHeadAge: cfg.MaxHeadAge,
		MaxFutureSkew: cfg.MaxFutureSkew,
	}, time.Now().UTC())
	if err != nil {
		return err
	}
	output := struct {
		Status           string    `json:"status"`
		Network          string    `json:"network"`
		Contract         string    `json:"contract"`
		Symbol           string    `json:"symbol"`
		Decimals         uint8     `json:"decimals"`
		HeadHeight       uint64    `json:"head_height"`
		HeadTime         time.Time `json:"head_time"`
		SolidifiedHeight uint64    `json:"solidified_height"`
		SolidifiedTime   time.Time `json:"solidified_time"`
		FinalizedLag     uint64    `json:"finalized_lag"`
	}{
		Status: "ok", Network: result.Network, Contract: result.ContractAddress,
		Symbol: result.Symbol, Decimals: result.Decimals,
		HeadHeight: result.Head.Height, HeadTime: result.Head.Timestamp,
		SolidifiedHeight: result.SolidifiedHead.Height, SolidifiedTime: result.SolidifiedHead.Timestamp,
		FinalizedLag: result.FinalizedLag,
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(output)
}
