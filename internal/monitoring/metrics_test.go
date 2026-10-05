package monitoring

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestMetricsUpdateReplacesDynamicLabels(t *testing.T) {
	metrics := NewMetrics()
	registry := prometheus.NewRegistry()
	registry.MustRegister(metrics)
	metrics.Update(Snapshot{
		OpenReconciliationCases: map[string]int64{"critical": 2},
		Payouts: map[string]CountAndOldest{
			"confirming": {Count: 3, OldestCreatedUnixTime: 100},
		},
		SweepExecutions: map[string]CountAndOldest{
			"ready_for_broadcast": {Count: 2, OldestCreatedUnixTime: 150},
		},
		Outbox: map[string]CountAndOldest{"dead": {Count: 1, OldestCreatedUnixTime: 200}},
		IndexerCursors: map[string]CursorSnapshot{
			"tron-nile": {NextHeight: 42, UpdatedUnixTime: 300},
		},
		LastReconciliationRun: map[string]float64{"wallet_assets": 400},
		LastWalletSnapshot:    map[string]float64{"usdt-tron-nile": 500},
		ScreeningJobs: map[string]CountAndOldest{
			"pending": {Count: 4, OldestCreatedUnixTime: 600},
		},
		ScreeningDecisions: map[string]int64{"deny": 1},
		DepositScreeningJobs: map[string]CountAndOldest{
			"processing": {Count: 2, OldestCreatedUnixTime: 700},
		},
		DepositScreeningDecisions: map[string]int64{"review": 3},
	})
	assertGaugeValue(t, registry, "stablecoin_gateway_business_reconciliation_open_cases", map[string]string{"severity": "critical"}, 2)
	assertGaugeValue(t, registry, "stablecoin_gateway_business_payouts", map[string]string{"status": "confirming"}, 3)
	assertGaugeValue(t, registry, "stablecoin_gateway_business_sweep_executions", map[string]string{"status": "ready_for_broadcast"}, 2)
	assertGaugeValue(t, registry, "stablecoin_gateway_business_indexer_next_height", map[string]string{"network": "tron-nile"}, 42)
	assertGaugeValue(t, registry, "stablecoin_gateway_business_wallet_snapshot_last_capture_timestamp_seconds", map[string]string{"asset_id": "usdt-tron-nile"}, 500)
	assertGaugeValue(t, registry, "stablecoin_gateway_business_payout_screening_jobs", map[string]string{"status": "pending"}, 4)
	assertGaugeValue(t, registry, "stablecoin_gateway_business_payout_screening_decisions", map[string]string{"decision": "deny"}, 1)
	assertGaugeValue(t, registry, "stablecoin_gateway_business_deposit_screening_jobs", map[string]string{"status": "processing"}, 2)
	assertGaugeValue(t, registry, "stablecoin_gateway_business_deposit_screening_decisions", map[string]string{"decision": "review"}, 3)

	metrics.Update(Snapshot{})
	if hasLabels(t, registry, "stablecoin_gateway_business_indexer_next_height", map[string]string{"network": "tron-nile"}) {
		t.Fatal("退役网络标签仍然存在")
	}
	if hasLabels(t, registry, "stablecoin_gateway_business_wallet_snapshot_last_capture_timestamp_seconds", map[string]string{"asset_id": "usdt-tron-nile"}) {
		t.Fatal("退役资产标签仍然存在")
	}
	assertGaugeValue(t, registry, "stablecoin_gateway_business_payouts", map[string]string{"status": "confirming"}, 0)
}

func assertGaugeValue(t *testing.T, gatherer prometheus.Gatherer, name string, labels map[string]string, want float64) {
	t.Helper()
	family := findMetricFamily(t, gatherer, name)
	for _, metric := range family.Metric {
		if labelsEqual(metric.Label, labels) {
			if metric.Gauge == nil || metric.Gauge.GetValue() != want {
				t.Fatalf("metric %s labels=%v value=%v, want %v", name, labels, metric.Gauge, want)
			}
			return
		}
	}
	t.Fatalf("metric %s labels=%v not found", name, labels)
}

func hasLabels(t *testing.T, gatherer prometheus.Gatherer, name string, labels map[string]string) bool {
	t.Helper()
	families, err := gatherer.Gather()
	if err != nil {
		t.Fatalf("Gather() error = %v", err)
	}
	var family *dto.MetricFamily
	for _, candidate := range families {
		if candidate.GetName() == name {
			family = candidate
			break
		}
	}
	if family == nil {
		return false
	}
	for _, metric := range family.Metric {
		if labelsEqual(metric.Label, labels) {
			return true
		}
	}
	return false
}

func findMetricFamily(t *testing.T, gatherer prometheus.Gatherer, name string) *dto.MetricFamily {
	t.Helper()
	families, err := gatherer.Gather()
	if err != nil {
		t.Fatalf("Gather() error = %v", err)
	}
	for _, family := range families {
		if family.GetName() == name {
			return family
		}
	}
	t.Fatalf("metric family %s not found", name)
	return nil
}

func labelsEqual(pairs []*dto.LabelPair, want map[string]string) bool {
	if len(pairs) != len(want) {
		return false
	}
	for _, pair := range pairs {
		if want[pair.GetName()] != pair.GetValue() {
			return false
		}
	}
	return true
}
