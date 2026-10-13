package observability

import (
	"fmt"
	"strconv"

	"github.com/eightyun/stablecoin-gateway/internal/chain/tron/nodehttp"
	"github.com/prometheus/client_golang/prometheus"
)

// TRONNodeObserver 将节点池事件记录为不含 URL 的固定基数指标。
type TRONNodeObserver struct {
	endpointCount *prometheus.GaugeVec
	preferred     *prometheus.GaugeVec
	failures      *prometheus.CounterVec
	switches      *prometheus.CounterVec
	exhausted     *prometheus.CounterVec
}

var _ nodehttp.Observer = (*TRONNodeObserver)(nil)

// NewTRONNodeObserver 注册 TRON 节点池指标。
func NewTRONNodeObserver(registerer prometheus.Registerer) (*TRONNodeObserver, error) {
	if registerer == nil {
		return nil, ErrInvalidConfig
	}
	observer := &TRONNodeObserver{
		endpointCount: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "stablecoin_gateway", Subsystem: "tron_node", Name: "endpoint_count",
			Help: "Configured TRON node endpoint count by operation.",
		}, []string{"operation"}),
		preferred: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "stablecoin_gateway", Subsystem: "tron_node", Name: "preferred",
			Help: "Whether a numbered TRON node endpoint is currently preferred.",
		}, []string{"operation", "endpoint"}),
		failures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "stablecoin_gateway", Subsystem: "tron_node", Name: "failures_total",
			Help: "Total TRON node endpoint failures by operation, endpoint number, and bounded reason.",
		}, []string{"operation", "endpoint", "reason"}),
		switches: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "stablecoin_gateway", Subsystem: "tron_node", Name: "switches_total",
			Help: "Total TRON node preferred-endpoint changes.",
		}, []string{"operation", "from", "to"}),
		exhausted: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "stablecoin_gateway", Subsystem: "tron_node", Name: "pool_exhausted_total",
			Help: "Total operations for which all configured TRON node endpoints failed.",
		}, []string{"operation"}),
	}
	collectors := []prometheus.Collector{
		observer.endpointCount, observer.preferred, observer.failures,
		observer.switches, observer.exhausted,
	}
	for _, collector := range collectors {
		if err := registerer.Register(collector); err != nil {
			return nil, fmt.Errorf("注册 TRON 节点指标: %w", err)
		}
	}
	return observer, nil
}

func (observer *TRONNodeObserver) Configured(operation string, endpointCount int) {
	observer.endpointCount.WithLabelValues(operation).Set(float64(endpointCount))
	for index := 0; index < endpointCount; index++ {
		value := 0.0
		if index == 0 {
			value = 1
		}
		observer.preferred.WithLabelValues(operation, endpointLabel(index)).Set(value)
	}
}

func (observer *TRONNodeObserver) Failed(operation string, endpointIndex int, reason string) {
	observer.failures.WithLabelValues(operation, endpointLabel(endpointIndex), reason).Inc()
}

func (observer *TRONNodeObserver) Switched(
	operation string,
	previousEndpointIndex, currentEndpointIndex int,
) {
	previous := endpointLabel(previousEndpointIndex)
	current := endpointLabel(currentEndpointIndex)
	observer.preferred.WithLabelValues(operation, previous).Set(0)
	observer.preferred.WithLabelValues(operation, current).Set(1)
	observer.switches.WithLabelValues(operation, previous, current).Inc()
}

func (observer *TRONNodeObserver) Exhausted(operation string) {
	observer.exhausted.WithLabelValues(operation).Inc()
}

func endpointLabel(index int) string {
	return strconv.Itoa(index + 1)
}
