package recovery

import (
	"context"
	"errors"
	"testing"
)

func TestNewStoreRequiresDatabase(t *testing.T) {
	if _, err := NewStore(nil); !errors.Is(err, ErrDatabaseRequired) {
		t.Fatalf("NewStore(nil) error = %v", err)
	}
}

func TestAuditRejectsInvalidExpectedVersion(t *testing.T) {
	store := &Store{}
	if _, err := store.Audit(context.Background(), 0); !errors.Is(err, ErrExpectedVersionInvalid) {
		t.Fatalf("Audit(..., 0) error = %v", err)
	}
}
