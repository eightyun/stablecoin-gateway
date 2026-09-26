package webhook

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/outbox"
	"github.com/eightyun/stablecoin-gateway/internal/secretbox"
)

type deliveryStoreStub struct {
	endpoints []Endpoint
	attempts  []Attempt
}

func (store *deliveryStoreStub) PendingEndpoints(context.Context, string, string) ([]Endpoint, error) {
	return store.endpoints, nil
}

func (store *deliveryStoreStub) RecordAttempt(_ context.Context, attempt Attempt) error {
	store.attempts = append(store.attempts, attempt)
	return nil
}

type senderStub struct {
	results  []DeliveryResult
	requests []DeliveryRequest
}

func (sender *senderStub) Send(_ context.Context, request DeliveryRequest) DeliveryResult {
	sender.requests = append(sender.requests, request)
	result := sender.results[0]
	sender.results = sender.results[1:]
	return result
}

func TestHandlerDeliversAllEndpointsAndRecordsAttempts(t *testing.T) {
	keyring, err := secretbox.NewKeyring(map[string][]byte{"v1": bytes.Repeat([]byte{1}, 32)}, "v1")
	if err != nil {
		t.Fatalf("NewKeyring() error = %v", err)
	}
	endpoints := make([]Endpoint, 2)
	for index := range endpoints {
		endpoints[index].ID = "endpoint-" + string(rune('a'+index))
		endpoints[index].URL = "https://example.com/hook"
		endpoints[index].Encrypted, err = keyring.Encrypt(endpointAAD(endpoints[index].ID), []byte("whsec_12345678901234567890"))
		if err != nil {
			t.Fatalf("Encrypt() error = %v", err)
		}
	}
	statusOK, statusFailed := 204, 500
	store := &deliveryStoreStub{endpoints: endpoints}
	sender := &senderStub{results: []DeliveryResult{
		{RequestTimestamp: 100, ResponseStatus: &statusOK, Duration: time.Millisecond},
		{RequestTimestamp: 101, ResponseStatus: &statusFailed, Duration: 2 * time.Millisecond, Err: errors.New("HTTP 500")},
	}}
	handler, err := NewHandler(store, keyring, sender)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	event := outbox.Event{
		ID: "event-1", Topic: "deposit.confirmed", Attempts: 2, CreatedAt: time.Now(),
		Payload: []byte(`{"merchant_id":"123e4567-e89b-12d3-a456-426614174000","amount":"100"}`),
	}
	if err := handler.Handle(context.Background(), event); err == nil {
		t.Fatal("Handle() 未返回失败端点错误")
	}
	if len(sender.requests) != 2 || len(store.attempts) != 2 || store.attempts[1].Attempt != 2 || store.attempts[1].Error == "" {
		t.Fatalf("requests=%d attempts=%+v", len(sender.requests), store.attempts)
	}
}

func TestHandlerRejectsEventWithoutMerchant(t *testing.T) {
	keyring, _ := secretbox.NewKeyring(map[string][]byte{"v1": bytes.Repeat([]byte{1}, 32)}, "v1")
	handler, err := NewHandler(&deliveryStoreStub{}, keyring, &senderStub{})
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	if err := handler.Handle(context.Background(), outbox.Event{Payload: []byte(`{}`)}); err == nil {
		t.Fatal("Handle() 未拒绝缺少商户的事件")
	}
}

func TestTruncateUTF8PreservesValidText(t *testing.T) {
	value := truncateUTF8("错误错误", 7)
	if value != "错误" {
		t.Fatalf("truncateUTF8() = %q", value)
	}
}
