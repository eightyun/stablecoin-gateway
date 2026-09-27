package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/eightyun/stablecoin-gateway/internal/database"
	"github.com/eightyun/stablecoin-gateway/internal/identity"
	"github.com/eightyun/stablecoin-gateway/internal/reconciliation"
)

type resolveReconciliationCaseOptions struct {
	caseID string
	actor  string
	reason string
}

func runListReconciliationCases(ctx context.Context, args []string, output io.Writer) error {
	limit, err := parseListReconciliationCasesOptions(args)
	if err != nil {
		return err
	}
	store, closeStore, err := openReconciliationStore(ctx)
	if err != nil {
		return err
	}
	defer closeStore()
	cases, err := store.ListOpenCases(ctx, limit)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(output).Encode(cases); err != nil {
		return fmt.Errorf("输出对账工单: %w", err)
	}
	return nil
}

func runResolveReconciliationCase(ctx context.Context, args []string, output io.Writer) error {
	options, err := parseResolveReconciliationCaseOptions(args)
	if err != nil {
		return err
	}
	store, closeStore, err := openReconciliationStore(ctx)
	if err != nil {
		return err
	}
	defer closeStore()
	if err := store.ResolveCase(ctx, reconciliation.ResolveRequest{
		CaseID: options.caseID, Actor: options.actor, Reason: options.reason,
	}); err != nil {
		return err
	}
	if err := json.NewEncoder(output).Encode(map[string]string{
		"case_id": options.caseID,
		"status":  "resolved",
	}); err != nil {
		return fmt.Errorf("输出对账工单关闭结果: %w", err)
	}
	return nil
}

func openReconciliationStore(ctx context.Context) (*reconciliation.Store, func(), error) {
	databaseURL := strings.TrimSpace(os.Getenv("GATEWAY_DATABASE_URL"))
	if databaseURL == "" {
		return nil, nil, errors.New("GATEWAY_DATABASE_URL 不能为空")
	}
	poolConfig := database.DefaultConfig(databaseURL, "gateway-admin")
	poolConfig.MinConnections = 1
	poolConfig.MaxConnections = 2
	pool, err := database.Open(ctx, poolConfig)
	if err != nil {
		return nil, nil, err
	}
	store, err := reconciliation.NewStore(pool)
	if err != nil {
		pool.Close()
		return nil, nil, err
	}
	return store, pool.Close, nil
}

func parseListReconciliationCasesOptions(args []string) (int, error) {
	if len(args) == 0 || args[0] != "list-reconciliation-cases" {
		return 0, errUsage
	}
	flags := flag.NewFlagSet("list-reconciliation-cases", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	limitText := flags.String("limit", "100", "maximum cases")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
		return 0, errUsage
	}
	limit, err := strconv.Atoi(strings.TrimSpace(*limitText))
	if err != nil || limit < 1 || limit > 1000 {
		return 0, errUsage
	}
	return limit, nil
}

func parseResolveReconciliationCaseOptions(args []string) (resolveReconciliationCaseOptions, error) {
	if len(args) == 0 || args[0] != "resolve-reconciliation-case" {
		return resolveReconciliationCaseOptions{}, errUsage
	}
	flags := flag.NewFlagSet("resolve-reconciliation-case", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	caseID := flags.String("case-id", "", "reconciliation case UUID")
	actor := flags.String("actor", "", "operator identity")
	reason := flags.String("reason", "", "resolution reason")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
		return resolveReconciliationCaseOptions{}, errUsage
	}
	options := resolveReconciliationCaseOptions{
		caseID: strings.TrimSpace(*caseID),
		actor:  strings.TrimSpace(*actor),
		reason: strings.TrimSpace(*reason),
	}
	if !identity.ValidUUID(options.caseID) || options.actor == "" || len(options.actor) > 128 ||
		options.reason == "" || len(options.reason) > 512 {
		return resolveReconciliationCaseOptions{}, errUsage
	}
	return options, nil
}
