package outbox

import (
	"context"
	"errors"
	"testing"
)

func TestValidatePendingEvent(t *testing.T) {
	valid := PendingEvent{
		ID: "event-1", Topic: "deposit.confirmed", AggregateType: "deposit", AggregateID: "deposit-1",
		Payload: []byte(`{"deposit_intent_id":"deposit-1"}`),
	}
	if err := validatePendingEvent(valid); err != nil {
		t.Fatalf("validatePendingEvent() error = %v", err)
	}
	invalid := valid
	invalid.Payload = []byte("invalid")
	if err := validatePendingEvent(invalid); !errors.Is(err, ErrInvalidPendingEvent) {
		t.Fatalf("validatePendingEvent() error = %v", err)
	}
}

func TestEnqueueInTransactionRejectsNilTransaction(t *testing.T) {
	if err := EnqueueInTransaction(context.Background(), nil, PendingEvent{}); !errors.Is(err, ErrTransactionRequired) {
		t.Fatalf("EnqueueInTransaction() error = %v", err)
	}
}
