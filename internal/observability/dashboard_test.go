package observability

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/chain/tron/nodehttp"
	"github.com/eightyun/stablecoin-gateway/internal/monitoring"
	"github.com/eightyun/stablecoin-gateway/internal/signer"
	dto "github.com/prometheus/client_model/go"
)

// 验证面板查询引用真实 Collector，避免指标改名后仪表盘静默失效。
func TestDashboardQueriesMatchActualMetricsAndFreshnessControls(t *testing.T) {
	service := newTestService(t, &readinessStub{})
	business := monitoring.NewMetrics()
	business.Update(monitoring.Snapshot{
		IndexerCursors:     map[string]monitoring.CursorSnapshot{"tron-nile": {NextHeight: 42, UpdatedUnixTime: 100}},
		LastWalletSnapshot: map[string]float64{"usdt-tron-nile": 100},
	})
	if err := service.Register(business); err != nil {
		t.Fatal(err)
	}
	service.observer.Started("business-monitor")
	service.observer.Succeeded("business-monitor", true, time.Millisecond)
	node := service.TRONNodeObserver()
	node.Configured(nodehttp.OperationRead, 2)
	node.Failed(nodehttp.OperationRead, 0, nodehttp.FailureHTTPRetryable)
	node.Switched(nodehttp.OperationRead, 0, 1)
	node.Exhausted(nodehttp.OperationRead)
	observer, err := service.NewSignerObserver(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	observer.Completed(signer.HTTPResult{Operation: "transfer", Result: "success", Duration: time.Millisecond})
	families, err := service.registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	metrics := make(map[string]bool)
	for _, family := range families {
		metrics[family.GetName()] = true
		if family.GetType() == dto.MetricType_HISTOGRAM {
			for _, suffix := range []string{"_bucket", "_sum", "_count"} {
				metrics[family.GetName()+suffix] = true
			}
		}
	}
	encoded, err := os.ReadFile("../../configs/grafana/dashboards/stablecoin-gateway.json")
	if err != nil {
		t.Fatal(err)
	}
	var dashboard struct {
		UID    string `json:"uid"`
		Panels []struct {
			ID      int `json:"id"`
			Targets []struct {
				Expr string `json:"expr"`
			} `json:"targets"`
		} `json:"panels"`
		Templating struct {
			List []struct {
				Name       string `json:"name"`
				IncludeAll bool   `json:"includeAll"`
				Multi      bool   `json:"multi"`
			} `json:"list"`
		} `json:"templating"`
	}
	if err := json.Unmarshal(encoded, &dashboard); err != nil {
		t.Fatal(err)
	}
	if dashboard.UID != "stablecoin-gateway-ops" || len(dashboard.Panels) == 0 {
		t.Fatal("仪表盘身份或面板缺失")
	}
	ids := make(map[int]bool)
	metricPattern := regexp.MustCompile(`\bstablecoin_gateway_[a-z0-9_]+`)
	for _, panel := range dashboard.Panels {
		if ids[panel.ID] {
			t.Fatalf("重复面板 ID %d", panel.ID)
		}
		ids[panel.ID] = true
		for _, target := range panel.Targets {
			for _, name := range metricPattern.FindAllString(target.Expr, -1) {
				if !metrics[name] {
					t.Errorf("面板 %d 引用未导出的指标 %s", panel.ID, name)
				}
			}
			if strings.Contains(target.Expr, "stablecoin_gateway_business_") {
				for _, control := range []string{`$monitor_instance`, `$monitor_max_age`, `worker="business-monitor"`, `up{`} {
					if !strings.Contains(target.Expr, control) {
						t.Errorf("面板 %d 缺少新鲜度或单实例约束 %s", panel.ID, control)
					}
				}
				if strings.Contains(target.Expr, "vector(0)") {
					t.Errorf("面板 %d 不应将缺失数据伪装成零", panel.ID)
				}
			}
		}
	}
	monitorFound := false
	for _, variable := range dashboard.Templating.List {
		if variable.Name == "monitor_instance" {
			monitorFound = true
			if variable.Multi || variable.IncludeAll {
				t.Fatal("业务快照不能跨多个监控实例聚合计数")
			}
		}
	}
	if !monitorFound {
		t.Fatal("缺少业务监控实例选择器")
	}
}
