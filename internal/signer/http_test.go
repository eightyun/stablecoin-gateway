package signer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/eightyun/stablecoin-gateway/internal/chain/tron"
)

type httpSignerStub struct {
	request      tron.TransferSignRequest
	sweepRequest tron.SweepSignRequest
	err          error
}

func (signer *httpSignerStub) SignSweep(_ context.Context, request tron.SweepSignRequest) (tron.SignedTransaction, error) {
	signer.sweepRequest = request
	return tron.SignedTransaction{ID: strings.Repeat("b", 64), Payload: []byte(`{"signed":true}`)}, signer.err
}

func (signer *httpSignerStub) SignTransfer(_ context.Context, request tron.TransferSignRequest) (tron.SignedTransaction, error) {
	signer.request = request
	return tron.SignedTransaction{ID: strings.Repeat("a", 64), Payload: []byte(`{"signed":true}`)}, signer.err
}

func TestHTTPHandlerForwardsSweepSourceAddress(t *testing.T) {
	service := &httpSignerStub{}
	handler, err := NewHTTPHandler(HTTPConfig{BearerToken: "secret"}, service)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"request_id":"` + testRequestID + `","network":"tron-nile","source_address":"` +
		testDestination + `","contract_address":"` + testContract + `","destination_address":"` +
		testDestination + `","amount":"1000000"}`
	request := httptest.NewRequest(http.MethodPost, "/v1/tron/sweeps:sign", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", testRequestID)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.sweepRequest.SourceAddress != testDestination {
		t.Fatalf("response = %d %s, request = %+v", response.Code, response.Body.String(), service.sweepRequest)
	}
}

func TestHTTPHandlerAuthenticatesAndForwardsRequest(t *testing.T) {
	service := &httpSignerStub{}
	handler, err := NewHTTPHandler(HTTPConfig{BearerToken: "secret"}, service)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"request_id":"` + testRequestID + `","network":"tron-nile","contract_address":"` +
		testContract + `","destination_address":"` + testDestination + `","amount":"1000000"}`
	request := httptest.NewRequest(http.MethodPost, "/v1/tron/transfers:sign", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", testRequestID)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.request.RequestID != testRequestID ||
		!strings.Contains(response.Body.String(), `"signed_transaction":{"signed":true}`) {
		t.Fatalf("response = %d %s, request = %+v", response.Code, response.Body.String(), service.request)
	}
}

func TestHTTPHandlerRejectsUnauthorizedAndConflictingRequest(t *testing.T) {
	service := &httpSignerStub{err: ErrIdempotencyConflict}
	handler, err := NewHTTPHandler(HTTPConfig{BearerToken: "secret"}, service)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"request_id":"` + testRequestID + `","network":"tron-nile","contract_address":"` +
		testContract + `","destination_address":"` + testDestination + `","amount":"1000000"}`
	unauthorized := httptest.NewRequest(http.MethodPost, "/v1/tron/transfers:sign", strings.NewReader(body))
	unauthorized.Header.Set("Content-Type", "application/json")
	unauthorizedResponse := httptest.NewRecorder()
	handler.ServeHTTP(unauthorizedResponse, unauthorized)
	if unauthorizedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorizedResponse.Code)
	}
	conflict := httptest.NewRequest(http.MethodPost, "/v1/tron/transfers:sign", strings.NewReader(body))
	conflict.Header.Set("Authorization", "Bearer secret")
	conflict.Header.Set("Content-Type", "application/json")
	conflict.Header.Set("Idempotency-Key", testRequestID)
	conflictResponse := httptest.NewRecorder()
	handler.ServeHTTP(conflictResponse, conflict)
	if conflictResponse.Code != http.StatusConflict {
		t.Fatalf("conflict status = %d, body = %s", conflictResponse.Code, conflictResponse.Body.String())
	}
}
