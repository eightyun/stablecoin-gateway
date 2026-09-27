package observability

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func TestServiceHealthReadinessAndMetrics(t *testing.T) {
	checker := &readinessStub{}
	service := newTestService(t, checker)
	service.observer.Started("test-worker")
	service.observer.Succeeded("test-worker", true, 20*time.Millisecond)

	health := httptest.NewRecorder()
	service.server.Handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != http.StatusOK || health.Body.String() != "ok\n" {
		t.Fatalf("healthz status=%d body=%q", health.Code, health.Body.String())
	}
	ready := httptest.NewRecorder()
	service.server.Handler.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if ready.Code != http.StatusOK || checker.calls != 1 {
		t.Fatalf("readyz status=%d calls=%d", ready.Code, checker.calls)
	}
	metrics := httptest.NewRecorder()
	service.server.Handler.ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := metrics.Body.String()
	if metrics.Code != http.StatusOK ||
		!strings.Contains(body, `stablecoin_gateway_worker_operations_total{result="worked",worker="test-worker"} 1`) ||
		!strings.Contains(body, `stablecoin_gateway_worker_running{worker="test-worker"} 1`) {
		t.Fatalf("metrics status=%d body=%s", metrics.Code, body)
	}
}

func TestServiceReadinessFailureDoesNotExposeError(t *testing.T) {
	service := newTestService(t, &readinessStub{err: errors.New("secret database detail")})
	response := httptest.NewRecorder()
	service.server.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusServiceUnavailable || response.Body.String() != "not ready\n" {
		t.Fatalf("readyz status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestServiceRunReturnsWorkloadError(t *testing.T) {
	service := newTestService(t, &readinessStub{})
	want := errors.New("worker stopped")
	if err := service.Run(context.Background(), func(context.Context) error { return want }); !errors.Is(err, want) {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestServiceRunStopsWorkloadAfterCancellation(t *testing.T) {
	service := newTestService(t, &readinessStub{})
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	go func() {
		<-started
		cancel()
	}()
	if err := service.Run(ctx, func(workloadCtx context.Context) error {
		close(started)
		<-workloadCtx.Done()
		return workloadCtx.Err()
	}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestNewRejectsInvalidConfiguration(t *testing.T) {
	if _, err := New(Config{}, nil); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("New() error = %v", err)
	}
	service := newTestService(t, &readinessStub{})
	if err := service.Run(context.Background(), nil); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestServiceRegistersCustomCollector(t *testing.T) {
	service := newTestService(t, &readinessStub{})
	custom := prometheus.NewGauge(prometheus.GaugeOpts{Name: "gateway_test_custom_metric"})
	custom.Set(7)
	if err := service.Register(custom); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if err := service.Register(custom); err == nil {
		t.Fatal("Register() 未拒绝重复 Collector")
	}
	response := httptest.NewRecorder()
	service.server.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(response.Body.String(), "gateway_test_custom_metric 7") {
		t.Fatalf("metrics body = %s", response.Body.String())
	}
}

func newTestService(t *testing.T, checker ReadinessChecker) *Service {
	t.Helper()
	service, err := New(Config{
		Addr: "127.0.0.1:0", ReadHeaderTimeout: time.Second,
		ReadinessTimeout: time.Second, ShutdownTimeout: time.Second,
	}, checker)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return service
}

type readinessStub struct {
	calls int
	err   error
}

func (stub *readinessStub) Ping(context.Context) error {
	stub.calls++
	return stub.err
}
