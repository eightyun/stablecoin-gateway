package monitoring

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

const metricNamespace = "stablecoin_gateway"

// Metrics 以单一内存快照暴露业务指标，Prometheus 抓取不访问数据库。
type Metrics struct {
	mutex    sync.RWMutex
	snapshot Snapshot

	reconciliationCases *prometheus.Desc
	payouts             *prometheus.Desc
	payoutOldest        *prometheus.Desc
	outbox              *prometheus.Desc
	outboxOldest        *prometheus.Desc
	indexerNextHeight   *prometheus.Desc
	indexerUpdated      *prometheus.Desc
	reconciliationRun   *prometheus.Desc
	walletSnapshot      *prometheus.Desc
}

var _ prometheus.Collector = (*Metrics)(nil)

// NewMetrics 创建业务指标 Collector。
func NewMetrics() *Metrics {
	return &Metrics{
		snapshot: emptySnapshot(),
		reconciliationCases: prometheus.NewDesc(
			prometheus.BuildFQName(metricNamespace, "business", "reconciliation_open_cases"),
			"当前未关闭的对账工单数。", []string{"severity"}, nil,
		),
		payouts: prometheus.NewDesc(
			prometheus.BuildFQName(metricNamespace, "business", "payouts"),
			"当前各待处理状态的出款数。", []string{"status"}, nil,
		),
		payoutOldest: prometheus.NewDesc(
			prometheus.BuildFQName(metricNamespace, "business", "payout_oldest_created_timestamp_seconds"),
			"各待处理状态最早出款的创建时间 Unix 秒；无记录时为零。", []string{"status"}, nil,
		),
		outbox: prometheus.NewDesc(
			prometheus.BuildFQName(metricNamespace, "business", "outbox_events"),
			"当前待处理、处理中和死信 Outbox 事件数。", []string{"status"}, nil,
		),
		outboxOldest: prometheus.NewDesc(
			prometheus.BuildFQName(metricNamespace, "business", "outbox_oldest_created_timestamp_seconds"),
			"各可操作状态最早 Outbox 事件的创建时间 Unix 秒；无记录时为零。", []string{"status"}, nil,
		),
		indexerNextHeight: prometheus.NewDesc(
			prometheus.BuildFQName(metricNamespace, "business", "indexer_next_height"),
			"链索引器下一待扫描高度。", []string{"network"}, nil,
		),
		indexerUpdated: prometheus.NewDesc(
			prometheus.BuildFQName(metricNamespace, "business", "indexer_cursor_updated_timestamp_seconds"),
			"链索引游标最后活动时间 Unix 秒。", []string{"network"}, nil,
		),
		reconciliationRun: prometheus.NewDesc(
			prometheus.BuildFQName(metricNamespace, "business", "reconciliation_last_run_timestamp_seconds"),
			"各类对账最近快照时间 Unix 秒；从未执行时为零。", []string{"kind"}, nil,
		),
		walletSnapshot: prometheus.NewDesc(
			prometheus.BuildFQName(metricNamespace, "business", "wallet_snapshot_last_capture_timestamp_seconds"),
			"各活动托管资产最近钱包余额快照时间 Unix 秒；从未执行时为零。", []string{"asset_id"}, nil,
		),
	}
}

// Describe 实现 prometheus.Collector。
func (metrics *Metrics) Describe(descriptions chan<- *prometheus.Desc) {
	descriptions <- metrics.reconciliationCases
	descriptions <- metrics.payouts
	descriptions <- metrics.payoutOldest
	descriptions <- metrics.outbox
	descriptions <- metrics.outboxOldest
	descriptions <- metrics.indexerNextHeight
	descriptions <- metrics.indexerUpdated
	descriptions <- metrics.reconciliationRun
	descriptions <- metrics.walletSnapshot
}

// Collect 从同一个不可变快照生成一轮指标。
func (metrics *Metrics) Collect(output chan<- prometheus.Metric) {
	metrics.mutex.RLock()
	defer metrics.mutex.RUnlock()
	for _, severity := range reconciliationSeverity {
		output <- prometheus.MustNewConstMetric(
			metrics.reconciliationCases, prometheus.GaugeValue,
			float64(metrics.snapshot.OpenReconciliationCases[severity]), severity,
		)
	}
	for _, status := range payoutStatuses {
		value := metrics.snapshot.Payouts[status]
		output <- prometheus.MustNewConstMetric(metrics.payouts, prometheus.GaugeValue, float64(value.Count), status)
		output <- prometheus.MustNewConstMetric(metrics.payoutOldest, prometheus.GaugeValue, value.OldestCreatedUnixTime, status)
	}
	for _, status := range outboxStatuses {
		value := metrics.snapshot.Outbox[status]
		output <- prometheus.MustNewConstMetric(metrics.outbox, prometheus.GaugeValue, float64(value.Count), status)
		output <- prometheus.MustNewConstMetric(metrics.outboxOldest, prometheus.GaugeValue, value.OldestCreatedUnixTime, status)
	}
	for network, cursor := range metrics.snapshot.IndexerCursors {
		output <- prometheus.MustNewConstMetric(metrics.indexerNextHeight, prometheus.GaugeValue, float64(cursor.NextHeight), network)
		output <- prometheus.MustNewConstMetric(metrics.indexerUpdated, prometheus.GaugeValue, cursor.UpdatedUnixTime, network)
	}
	for _, kind := range reconciliationKinds {
		output <- prometheus.MustNewConstMetric(
			metrics.reconciliationRun, prometheus.GaugeValue,
			metrics.snapshot.LastReconciliationRun[kind], kind,
		)
	}
	for assetID, timestamp := range metrics.snapshot.LastWalletSnapshot {
		output <- prometheus.MustNewConstMetric(metrics.walletSnapshot, prometheus.GaugeValue, timestamp, assetID)
	}
}

// Update 以一次锁内替换发布最近成功快照。
func (metrics *Metrics) Update(snapshot Snapshot) {
	cloned := cloneSnapshot(snapshot)
	metrics.mutex.Lock()
	metrics.snapshot = cloned
	metrics.mutex.Unlock()
}

func cloneSnapshot(source Snapshot) Snapshot {
	target := emptySnapshot()
	for key, value := range source.OpenReconciliationCases {
		target.OpenReconciliationCases[key] = value
	}
	for key, value := range source.Payouts {
		target.Payouts[key] = value
	}
	for key, value := range source.Outbox {
		target.Outbox[key] = value
	}
	for key, value := range source.IndexerCursors {
		target.IndexerCursors[key] = value
	}
	for key, value := range source.LastReconciliationRun {
		target.LastReconciliationRun[key] = value
	}
	for key, value := range source.LastWalletSnapshot {
		target.LastWalletSnapshot[key] = value
	}
	return target
}
