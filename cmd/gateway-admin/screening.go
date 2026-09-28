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
	"github.com/eightyun/stablecoin-gateway/internal/screening"
)

func runListPayoutScreenings(ctx context.Context, args []string, output io.Writer) error {
	limit, err := parseListPayoutScreeningsOptions(args)
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
	store, err := screening.NewStore(pool)
	if err != nil {
		return err
	}
	results, err := store.ListActionRequired(ctx, limit)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(output).Encode(results); err != nil {
		return fmt.Errorf("输出待处置地址筛查结果: %w", err)
	}
	return nil
}

func parseListPayoutScreeningsOptions(args []string) (int, error) {
	if len(args) == 0 || args[0] != "list-payout-screenings" {
		return 0, errUsage
	}
	flags := flag.NewFlagSet("list-payout-screenings", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	limit := flags.Int("limit", 100, "maximum number of screening results")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 || *limit <= 0 || *limit > 1000 {
		return 0, errUsage
	}
	return *limit, nil
}
