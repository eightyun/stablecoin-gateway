package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/eightyun/stablecoin-gateway/internal/database"
	"github.com/eightyun/stablecoin-gateway/internal/wallet"
)

type registerCustodyWalletOptions struct {
	assetID string
	address string
	role    string
	actor   string
	reason  string
}

func runRegisterCustodyWallet(ctx context.Context, args []string, output io.Writer) error {
	options, err := parseRegisterCustodyWalletOptions(args)
	if err != nil {
		return err
	}
	databaseURL := strings.TrimSpace(os.Getenv("GATEWAY_DATABASE_URL"))
	if databaseURL == "" {
		return errors.New("GATEWAY_DATABASE_URL 不能为空")
	}
	poolConfig := database.DefaultConfig(databaseURL, "gateway-admin")
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
	result, err := store.Register(ctx, wallet.RegisterRequest{
		AssetID: options.assetID, Address: options.address, Role: options.role,
		Actor: options.actor, Reason: options.reason,
	})
	if err != nil {
		return err
	}
	if err := json.NewEncoder(output).Encode(result); err != nil {
		return fmt.Errorf("输出托管钱包登记结果: %w", err)
	}
	return nil
}

func parseRegisterCustodyWalletOptions(args []string) (registerCustodyWalletOptions, error) {
	if len(args) == 0 || args[0] != "register-custody-wallet" {
		return registerCustodyWalletOptions{}, errUsage
	}
	flags := flag.NewFlagSet("register-custody-wallet", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	assetID := flags.String("asset-id", "", "asset ID")
	address := flags.String("address", "", "TRON address")
	role := flags.String("role", "", "wallet role")
	actor := flags.String("actor", "", "operator identity")
	reason := flags.String("reason", "", "registration reason")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
		return registerCustodyWalletOptions{}, errUsage
	}
	options := registerCustodyWalletOptions{
		assetID: strings.TrimSpace(*assetID), address: strings.TrimSpace(*address),
		role: strings.TrimSpace(*role), actor: strings.TrimSpace(*actor), reason: strings.TrimSpace(*reason),
	}
	if options.assetID == "" || len(options.assetID) > 128 || options.address == "" ||
		(options.role != wallet.RoleHot && options.role != wallet.RoleCold && options.role != wallet.RoleFee) ||
		options.actor == "" || len(options.actor) > 128 ||
		options.reason == "" || len(options.reason) > 512 {
		return registerCustodyWalletOptions{}, errUsage
	}
	return options, nil
}
