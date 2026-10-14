package signer

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/chain/tron"
)

type httpSignerStub struct {
	request      tron.TransferSignRequest
	sweepRequest tron.SweepSignRequest
	err          error
}

type httpObserverStub struct {
	results chan HTTPResult
}

func (observer *httpObserverStub) Completed(result HTTPResult) { observer.results <- result }

func TestHTTPHandlerObservesBoundedResultsAndAuthenticatedIDs(t *testing.T) {
	for _, test := range []struct {
		name          string
		token         string
		err           error
		result        string
		requestID     string
		transactionID string
	}{
		{name: "成功", token: "Bearer secret", result: "success", requestID: testRequestID, transactionID: strings.Repeat("a", 64)},
		{name: "未鉴权", token: "Bearer wrong", result: "unauthorized"},
		{name: "策略拒绝", token: "Bearer secret", err: ErrPolicyDenied, result: "policy_denied", requestID: testRequestID},
		{name: "幂等冲突", token: "Bearer secret", err: ErrIdempotencyConflict, result: "conflict", requestID: testRequestID},
		{name: "上游错误", token: "Bearer secret", err: errors.New("secret upstream error"), result: "failed", requestID: testRequestID},
	} {
		t.Run(test.name, func(t *testing.T) {
			observer := &httpObserverStub{results: make(chan HTTPResult, 1)}
			handler, err := NewHTTPHandler(HTTPConfig{BearerToken: "secret", Observer: observer}, &httpSignerStub{err: test.err})
			if err != nil {
				t.Fatal(err)
			}
			body := `{"request_id":"` + testRequestID + `","network":"tron-nile","contract_address":"` +
				testContract + `","destination_address":"` + testDestination + `","amount":"1000000"}`
			request := httptest.NewRequest(http.MethodPost, "/v1/tron/transfers:sign", strings.NewReader(body))
			request.Header.Set("Authorization", test.token)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Idempotency-Key", testRequestID)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			event := <-observer.results
			if event.Operation != "transfer" || event.Result != test.result || event.RequestID != test.requestID ||
				event.TransactionID != test.transactionID || event.Duration < 0 {
				t.Fatalf("审计事件 = %+v", event)
			}
			if strings.Contains(response.Body.String(), "secret upstream error") {
				t.Fatal("响应泄漏上游错误")
			}
		})
	}
}

type cancelingHTTPSigner struct{}

func (cancelingHTTPSigner) SignTransfer(ctx context.Context, _ tron.TransferSignRequest) (tron.SignedTransaction, error) {
	<-ctx.Done()
	return tron.SignedTransaction{}, ctx.Err()
}

func TestHTTPHandlerTimeoutCancelsSigningAndRecordsOutcome(t *testing.T) {
	observer := &httpObserverStub{results: make(chan HTTPResult, 1)}
	handler, err := NewHTTPHandler(HTTPConfig{BearerToken: "secret", RequestTimeout: 10 * time.Millisecond, Observer: observer}, cancelingHTTPSigner{})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/tron/transfers:sign", strings.NewReader(`{"request_id":"`+testRequestID+`"}`))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", testRequestID)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "signing_timeout") {
		t.Fatalf("超时响应 = %d %s", response.Code, response.Body.String())
	}
	select {
	case event := <-observer.results:
		if event.Result != "timeout" || event.RequestID != testRequestID {
			t.Fatalf("超时审计 = %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("签名操作未响应取消")
	}
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
