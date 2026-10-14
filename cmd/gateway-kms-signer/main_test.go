package main

import (
	"context"
	"errors"
	"testing"
)

type readinessStub struct {
	calls int
	err   error
}

func (checker *readinessStub) Ping(context.Context) error { checker.calls++; return checker.err }

func TestSignerReadinessChecksDatabaseAndKMS(t *testing.T) {
	database, key := &readinessStub{}, &readinessStub{}
	checker := signerReadiness{database: database, key: key}
	if err := checker.Ping(context.Background()); err != nil || database.calls != 1 || key.calls != 1 {
		t.Fatalf("Ping = %v database=%d key=%d", err, database.calls, key.calls)
	}
	database.err = errors.New("database unavailable")
	if err := checker.Ping(context.Background()); !errors.Is(err, database.err) || key.calls != 1 {
		t.Fatalf("数据库故障 Ping = %v key=%d", err, key.calls)
	}
	database.err = nil
	key.err = errors.New("kms unavailable")
	if err := checker.Ping(context.Background()); !errors.Is(err, key.err) {
		t.Fatalf("KMS 故障 Ping = %v", err)
	}
}
