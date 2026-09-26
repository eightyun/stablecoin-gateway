package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/eightyun/stablecoin-gateway/internal/deposit"
	"github.com/eightyun/stablecoin-gateway/internal/merchantauth"
)

type authenticatorStub struct {
	principal merchantauth.Principal
	err       error
	body      []byte
}

func (stub *authenticatorStub) Authenticate(_ context.Context, _ *http.Request, body []byte) (merchantauth.Principal, error) {
	stub.body = append([]byte(nil), body...)
	return stub.principal, stub.err
}

type depositServiceStub struct {
	createRequest deposit.PoolIntentRequest
	createResult  deposit.PoolIntentResult
	createErr     error
	intent        deposit.IntentDetails
	balances      []deposit.Balance
}

func (stub *depositServiceStub) CreateIntentFromPool(_ context.Context, request deposit.PoolIntentRequest) (deposit.PoolIntentResult, error) {
	stub.createRequest = request
	return stub.createResult, stub.createErr
}

func (stub *depositServiceStub) GetIntent(context.Context, string, string) (deposit.IntentDetails, error) {
	return stub.intent, nil
}

func (stub *depositServiceStub) ListBalances(context.Context, string) ([]deposit.Balance, error) {
	return stub.balances, nil
}

func TestHealth(t *testing.T) {
	handler := newTestHandler(t, &authenticatorStub{}, &depositServiceStub{})
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != "{\"status\":\"ok\"}\n" {
		t.Fatalf("health response = %d %q", response.Code, response.Body.String())
	}
}

func TestCreateDepositUsesAuthenticatedMerchantAndIdempotencyKey(t *testing.T) {
	authenticator := &authenticatorStub{principal: merchantauth.Principal{MerchantID: "merchant-1"}}
	service := &depositServiceStub{createResult: deposit.PoolIntentResult{
		Intent: deposit.IntentDetails{ID: "deposit-1"}, Created: true,
	}}
	handler := newTestHandler(t, authenticator, service)
	body := []byte(`{"merchant_reference":"order-1","asset_id":"usdt-tron","amount":"1000000","expires_in_seconds":900}`)
	request := httptest.NewRequest(http.MethodPost, "/v1/deposits", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(idempotencyHeader, "idem-1")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || !bytes.Equal(authenticator.body, body) {
		t.Fatalf("create response = %d %q", response.Code, response.Body.String())
	}
	if service.createRequest.MerchantID != "merchant-1" || service.createRequest.IdempotencyKey != "idem-1" ||
		service.createRequest.MerchantReference != "order-1" || service.createRequest.ExpectedAmount != "1000000" {
		t.Fatalf("CreateIntentFromPool() request = %+v", service.createRequest)
	}
}

func TestMerchantRoutesRejectUnauthorizedAndInvalidRequests(t *testing.T) {
	t.Run("未授权", func(t *testing.T) {
		handler := newTestHandler(t, &authenticatorStub{err: merchantauth.ErrUnauthorized}, &depositServiceStub{})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/balances", nil))
		if response.Code != http.StatusUnauthorized || response.Header().Get("WWW-Authenticate") != "HMAC" {
			t.Fatalf("response = %d %+v", response.Code, response.Header())
		}
	})
	t.Run("未知字段", func(t *testing.T) {
		handler := newTestHandler(t, &authenticatorStub{principal: merchantauth.Principal{MerchantID: "merchant"}}, &depositServiceStub{})
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/v1/deposits", bytes.NewBufferString(`{"unknown":true}`))
		request.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("response = %d %q", response.Code, response.Body.String())
		}
	})
	t.Run("幂等冲突", func(t *testing.T) {
		service := &depositServiceStub{createErr: deposit.ErrIntentConflict}
		handler := newTestHandler(t, &authenticatorStub{principal: merchantauth.Principal{MerchantID: "merchant"}}, service)
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/v1/deposits", bytes.NewBufferString(
			`{"merchant_reference":"order","asset_id":"asset","amount":"1","expires_in_seconds":900}`,
		))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set(idempotencyHeader, "idem")
		handler.ServeHTTP(response, request)
		var payload map[string]any
		_ = json.Unmarshal(response.Body.Bytes(), &payload)
		if response.Code != http.StatusConflict || payload["error"] == nil {
			t.Fatalf("response = %d %q", response.Code, response.Body.String())
		}
	})
}

func TestNewHandlerRejectsInvalidConfiguration(t *testing.T) {
	if _, err := NewHandler(nil, nil, 0); !errors.Is(err, ErrInvalidHandlerConfig) {
		t.Fatalf("NewHandler() error = %v", err)
	}
}

func newTestHandler(t *testing.T, authenticator merchantauth.RequestAuthenticator, service DepositService) http.Handler {
	t.Helper()
	handler, err := NewHandler(authenticator, service, 1024)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	return handler
}
