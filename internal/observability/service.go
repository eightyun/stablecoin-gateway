// Package observability 提供后台进程的 Prometheus 指标和健康探针。
package observability

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/background"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var ErrInvalidConfig = errors.New("可观测性服务配置无效")

// ReadinessChecker 检查进程依赖是否可用。
type ReadinessChecker interface {
	Ping(context.Context) error
}

// Config 控制指标监听和有界关闭。
type Config struct {
	Addr              string
	ReadHeaderTimeout time.Duration
	ReadinessTimeout  time.Duration
	ShutdownTimeout   time.Duration
}

// Service 同时运行指标端点和一个长期工作负载。
type Service struct {
	config   Config
	server   *http.Server
	registry *prometheus.Registry
	observer *WorkerObserver
}

// New 创建使用独立指标注册表的可观测性服务。
func New(config Config, checker ReadinessChecker) (*Service, error) {
	config.Addr = strings.TrimSpace(config.Addr)
	if config.Addr == "" || config.ReadHeaderTimeout <= 0 || config.ReadinessTimeout <= 0 ||
		config.ShutdownTimeout <= 0 || checker == nil {
		return nil, ErrInvalidConfig
	}
	registry := prometheus.NewRegistry()
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	observer, err := NewWorkerObserver(registry)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /readyz", func(writer http.ResponseWriter, request *http.Request) {
		ctx, cancel := context.WithTimeout(request.Context(), config.ReadinessTimeout)
		defer cancel()
		if err := checker.Ping(ctx); err != nil {
			http.Error(writer, "not ready", http.StatusServiceUnavailable)
			return
		}
		writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("ready\n"))
	})
	mux.Handle("GET /metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
	}))
	return &Service{
		config: config,
		server: &http.Server{
			Addr: config.Addr, Handler: mux, ReadHeaderTimeout: config.ReadHeaderTimeout,
		},
		registry: registry,
		observer: observer,
	}, nil
}

// Register 注册当前进程独有的指标 Collector。
func (service *Service) Register(collectors ...prometheus.Collector) error {
	for _, collector := range collectors {
		if collector == nil {
			return ErrInvalidConfig
		}
	}
	for _, collector := range collectors {
		if err := service.registry.Register(collector); err != nil {
			return fmt.Errorf("注册 Prometheus 指标: %w", err)
		}
	}
	return nil
}

// WorkerObserver 返回供后台运行器复用的进程级指标观察器。
func (service *Service) WorkerObserver() background.Observer {
	return service.observer
}

// Run 运行指标服务和工作负载；任一方退出都会取消另一方并有界关闭。
func (service *Service) Run(ctx context.Context, workload func(context.Context) error) error {
	if workload == nil {
		return ErrInvalidConfig
	}
	if err := ctx.Err(); err != nil {
		return nil
	}
	listener, err := net.Listen("tcp", service.config.Addr)
	if err != nil {
		return fmt.Errorf("监听可观测性端口: %w", err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	serverErrors := make(chan error, 1)
	go func() {
		serverErrors <- normalizeServerError(service.server.Serve(listener))
	}()
	workloadErrors := make(chan error, 1)
	go func() {
		workloadErrors <- workload(runCtx)
	}()

	var result error
	workloadDone, serverDone := false, false
	select {
	case result = <-workloadErrors:
		workloadDone = true
	case serveErr := <-serverErrors:
		serverDone = true
		if serveErr != nil {
			result = fmt.Errorf("可观测性服务退出: %w", serveErr)
		}
	case <-ctx.Done():
	}
	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), service.config.ShutdownTimeout)
	defer shutdownCancel()
	if shutdownErr := service.server.Shutdown(shutdownCtx); shutdownErr != nil {
		result = errors.Join(result, fmt.Errorf("关闭可观测性服务: %w", shutdownErr))
	}
	if !serverDone {
		if serveErr := <-serverErrors; serveErr != nil {
			result = errors.Join(result, fmt.Errorf("可观测性服务退出: %w", serveErr))
		}
	}
	if !workloadDone {
		workloadErr, stopped := waitForWorkload(shutdownCtx, workloadErrors)
		if !stopped {
			result = errors.Join(result, errors.New("工作负载未在关闭超时内退出"))
		} else if workloadErr != nil && !errors.Is(workloadErr, context.Canceled) {
			result = errors.Join(result, workloadErr)
		}
	}
	return result
}

func waitForWorkload(ctx context.Context, errorsChannel <-chan error) (error, bool) {
	select {
	case err := <-errorsChannel:
		return err, true
	default:
	}
	select {
	case err := <-errorsChannel:
		return err, true
	case <-ctx.Done():
		return nil, false
	}
}

func normalizeServerError(err error) error {
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
