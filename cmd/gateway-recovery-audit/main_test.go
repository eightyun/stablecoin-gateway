package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunRequiresDatabaseURL(t *testing.T) {
	err := run(context.Background(), "  ", &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "GATEWAY_DATABASE_URL") {
		t.Fatalf("run() error = %v", err)
	}
}
