package main

import (
	"errors"
	"testing"
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
