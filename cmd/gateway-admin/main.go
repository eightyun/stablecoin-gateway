package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/config"
	"github.com/eightyun/stablecoin-gateway/internal/database"
	"github.com/eightyun/stablecoin-gateway/internal/identity"
	"github.com/eightyun/stablecoin-gateway/internal/merchantauth"
	"github.com/eightyun/stablecoin-gateway/internal/payout"
	"github.com/eightyun/stablecoin-gateway/internal/secretbox"
	"github.com/eightyun/stablecoin-gateway/internal/webhook"
)

var errUsage = errors.New("用法: gateway-admin create-api-key --merchant-id UUID --name NAME [--expires-at RFC3339] | create-webhook-endpoint --merchant-id UUID --name NAME --url HTTPS_URL | approve-payout --payout-id UUID --reviewer ID --reason TEXT | reject-payout --payout-id UUID --reviewer ID --reason TEXT")

type createAPIKeyOptions struct {
	merchantID string
	name       string
	expiresAt  *time.Time
}

type createWebhookEndpointOptions struct {
	merchantID string
	name       string
	url        string
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, output io.Writer) error {
	if len(args) == 0 {
		return errUsage
	}
	switch args[0] {
	case "create-api-key":
		return runCreateAPIKey(ctx, args, output)
	case "create-webhook-endpoint":
		return runCreateWebhookEndpoint(ctx, args, output)
	case "approve-payout":
		return runReviewPayout(ctx, args, output, payout.DecisionApprove)
	case "reject-payout":
		return runReviewPayout(ctx, args, output, payout.DecisionReject)
	default:
		return errUsage
	}
}

func runCreateAPIKey(ctx context.Context, args []string, output io.Writer) error {
	options, err := parseCreateAPIKeyOptions(args)
	if err != nil {
		return err
	}
	cfg, err := config.LoadAPI()
	if err != nil {
		return err
	}
	poolConfig := database.DefaultConfig(cfg.DatabaseURL, "gateway-admin")
	poolConfig.MinConnections = 1
	poolConfig.MaxConnections = 2
	pool, err := database.Open(ctx, poolConfig)
	if err != nil {
		return err
	}
	defer pool.Close()
	keyring, err := merchantauth.NewKeyring(cfg.APIKeyEncryptionKeys, cfg.APIKeyActiveVersion)
	if err != nil {
		return err
	}
	store, err := merchantauth.NewStore(pool)
	if err != nil {
		return err
	}
	credentials, err := store.ProvisionKey(ctx, keyring, options.merchantID, options.name, options.expiresAt)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(output).Encode(credentials); err != nil {
		return fmt.Errorf("输出 API Key: %w", err)
	}
	return nil
}

func runCreateWebhookEndpoint(ctx context.Context, args []string, output io.Writer) error {
	options, err := parseCreateWebhookEndpointOptions(args)
	if err != nil {
		return err
	}
	cfg, err := config.LoadWebhookSecrets()
	if err != nil {
		return err
	}
	poolConfig := database.DefaultConfig(cfg.DatabaseURL, "gateway-admin")
	poolConfig.MinConnections = 1
	poolConfig.MaxConnections = 2
	pool, err := database.Open(ctx, poolConfig)
	if err != nil {
		return err
	}
	defer pool.Close()
	keyring, err := secretbox.NewKeyring(cfg.EncryptionKeys, cfg.ActiveKeyVersion)
	if err != nil {
		return err
	}
	store, err := webhook.NewStore(pool)
	if err != nil {
		return err
	}
	credentials, err := store.ProvisionEndpoint(ctx, keyring, options.merchantID, options.name, options.url)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(output).Encode(credentials); err != nil {
		return fmt.Errorf("输出 Webhook 端点凭证: %w", err)
	}
	return nil
}

func parseCreateAPIKeyOptions(args []string) (createAPIKeyOptions, error) {
	if len(args) == 0 || args[0] != "create-api-key" {
		return createAPIKeyOptions{}, errUsage
	}
	flags := flag.NewFlagSet("create-api-key", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	merchantID := flags.String("merchant-id", "", "merchant UUID")
	name := flags.String("name", "", "key name")
	expiresAtText := flags.String("expires-at", "", "RFC3339 expiration")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
		return createAPIKeyOptions{}, errUsage
	}
	if !identity.ValidUUID(strings.TrimSpace(*merchantID)) || strings.TrimSpace(*name) == "" || len(*name) > 128 {
		return createAPIKeyOptions{}, errUsage
	}
	options := createAPIKeyOptions{merchantID: strings.TrimSpace(*merchantID), name: strings.TrimSpace(*name)}
	if strings.TrimSpace(*expiresAtText) != "" {
		expiresAt, err := time.Parse(time.RFC3339, strings.TrimSpace(*expiresAtText))
		if err != nil || !expiresAt.After(time.Now()) {
			return createAPIKeyOptions{}, errUsage
		}
		expiresAt = expiresAt.UTC()
		options.expiresAt = &expiresAt
	}
	return options, nil
}

func parseCreateWebhookEndpointOptions(args []string) (createWebhookEndpointOptions, error) {
	if len(args) == 0 || args[0] != "create-webhook-endpoint" {
		return createWebhookEndpointOptions{}, errUsage
	}
	flags := flag.NewFlagSet("create-webhook-endpoint", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	merchantID := flags.String("merchant-id", "", "merchant UUID")
	name := flags.String("name", "", "endpoint name")
	endpointURL := flags.String("url", "", "HTTPS endpoint URL")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
		return createWebhookEndpointOptions{}, errUsage
	}
	options := createWebhookEndpointOptions{
		merchantID: strings.TrimSpace(*merchantID),
		name:       strings.TrimSpace(*name),
		url:        strings.TrimSpace(*endpointURL),
	}
	if !identity.ValidUUID(options.merchantID) || options.name == "" || len(options.name) > 128 || options.url == "" {
		return createWebhookEndpointOptions{}, errUsage
	}
	return options, nil
}
