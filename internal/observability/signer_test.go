package observability

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/signer"
)

func TestSignerObserverMetricsAndAuditUseSeparateIdentifiers(t *testing.T) {
	service := newTestService(t, &readinessStub{})
	var logs bytes.Buffer
	observer, err := service.NewSignerObserver(slog.New(slog.NewJSONHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	observer.Completed(signer.HTTPResult{
		Operation: "transfer", Result: "success", RequestID: "123e4567-e89b-12d3-a456-426614174000",
		TransactionID: strings.Repeat("a", 64), Duration: 50 * time.Millisecond,
	})
	observer.Completed(signer.HTTPResult{Operation: "https://untrusted.example", Result: "secret error", Duration: time.Second})
	response := httptest.NewRecorder()
	service.server.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	metrics := response.Body.String()
	if !strings.Contains(metrics, `stablecoin_gateway_signer_requests_total{operation="transfer",result="success"} 1`) ||
		!strings.Contains(metrics, `stablecoin_gateway_signer_requests_total{operation="unknown",result="failed"} 1`) ||
		!strings.Contains(metrics, `stablecoin_gateway_signer_request_duration_seconds_count{operation="transfer"} 1`) {
		t.Fatalf("指标 = %s", metrics)
	}
	if strings.Contains(metrics, "123e4567") || strings.Contains(metrics, "untrusted.example") || strings.Contains(metrics, "secret error") {
		t.Fatal("指标泄漏标识或使用无界标签")
	}
	if !strings.Contains(logs.String(), "123e4567") || !strings.Contains(logs.String(), strings.Repeat("a", 64)) ||
		strings.Contains(logs.String(), "untrusted.example") || strings.Contains(logs.String(), "secret error") {
		t.Fatalf("审计日志 = %s", logs.String())
	}
}
