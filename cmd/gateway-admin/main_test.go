package main

import (
	"errors"
	"testing"

	"github.com/eightyun/stablecoin-gateway/internal/payout"
)

func TestParseCreateAPIKeyOptions(t *testing.T) {
	options, err := parseCreateAPIKeyOptions([]string{
		"create-api-key", "--merchant-id", "123e4567-e89b-12d3-a456-426614174000", "--name", "production",
	})
	if err != nil || options.merchantID == "" || options.name != "production" {
		t.Fatalf("parseCreateAPIKeyOptions() = %+v, %v", options, err)
	}
}

func TestParseCreateAPIKeyOptionsRejectsInvalidArguments(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"unknown"},
		{"create-api-key", "--merchant-id", "invalid", "--name", "production"},
		{"create-api-key", "--merchant-id", "123e4567-e89b-12d3-a456-426614174000"},
	} {
		if _, err := parseCreateAPIKeyOptions(args); !errors.Is(err, errUsage) {
			t.Fatalf("args=%v error=%v", args, err)
		}
	}
}

func TestParseCreateWebhookEndpointOptions(t *testing.T) {
	options, err := parseCreateWebhookEndpointOptions([]string{
		"create-webhook-endpoint", "--merchant-id", "123e4567-e89b-12d3-a456-426614174000",
		"--name", "orders", "--url", "https://example.com/webhooks/gateway",
	})
	if err != nil || options.name != "orders" || options.url == "" {
		t.Fatalf("parseCreateWebhookEndpointOptions() = %+v, %v", options, err)
	}
}

func TestParseReviewPayoutOptions(t *testing.T) {
	options, err := parseReviewPayoutOptions([]string{
		"approve-payout", "--payout-id", "123e4567-e89b-42d3-a456-426614174000",
		"--reviewer", "ops@example.com", "--reason", "screening passed",
	}, payout.DecisionApprove)
	if err != nil || options.reviewer != "ops@example.com" || options.reason != "screening passed" {
		t.Fatalf("parseReviewPayoutOptions() = %+v, %v", options, err)
	}
	if _, err := parseReviewPayoutOptions([]string{
		"reject-payout", "--payout-id", "invalid", "--reviewer", "ops", "--reason", "denied",
	}, payout.DecisionReject); !errors.Is(err, errUsage) {
		t.Fatalf("invalid parseReviewPayoutOptions() error = %v", err)
	}
}
