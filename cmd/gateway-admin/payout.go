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
	"github.com/eightyun/stablecoin-gateway/internal/identity"
	"github.com/eightyun/stablecoin-gateway/internal/payout"
)

type reviewPayoutOptions struct {
	payoutID string
	reviewer string
	reason   string
}

func runReviewPayout(
	ctx context.Context,
	args []string,
	output io.Writer,
	decision payout.ReviewDecision,
) error {
	options, err := parseReviewPayoutOptions(args, decision)
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
	store, err := payout.NewStore(pool)
	if err != nil {
		return err
	}
	result, err := store.Review(ctx, payout.ReviewRequest{
		PayoutID: options.payoutID, Decision: decision,
		Reviewer: options.reviewer, Reason: options.reason,
	})
	if err != nil {
		return err
	}
	if err := json.NewEncoder(output).Encode(result); err != nil {
		return fmt.Errorf("输出出款审批结果: %w", err)
	}
	return nil
}

func parseReviewPayoutOptions(args []string, decision payout.ReviewDecision) (reviewPayoutOptions, error) {
	expectedCommand := "approve-payout"
	if decision == payout.DecisionReject {
		expectedCommand = "reject-payout"
	}
	if len(args) == 0 || args[0] != expectedCommand {
		return reviewPayoutOptions{}, errUsage
	}
	flags := flag.NewFlagSet(expectedCommand, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	payoutID := flags.String("payout-id", "", "payout UUID")
	reviewer := flags.String("reviewer", "", "operator identity")
	reason := flags.String("reason", "", "review reason")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
		return reviewPayoutOptions{}, errUsage
	}
	options := reviewPayoutOptions{
		payoutID: strings.TrimSpace(*payoutID),
		reviewer: strings.TrimSpace(*reviewer),
		reason:   strings.TrimSpace(*reason),
	}
	if !identity.ValidUUID(options.payoutID) || options.reviewer == "" || len(options.reviewer) > 128 ||
		options.reason == "" || len(options.reason) > 512 {
		return reviewPayoutOptions{}, errUsage
	}
	return options, nil
}
