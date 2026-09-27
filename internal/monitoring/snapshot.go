// Package monitoring 从业务数据库生成低基数、只读的风险指标快照。
package monitoring

import "context"

var (
	payoutStatuses         = []string{"pending_review", "approved", "ready_for_broadcast", "confirming"}
	outboxStatuses         = []string{"pending", "processing", "dead"}
	reconciliationKinds    = []string{"ledger_integrity", "wallet_assets"}
	reconciliationSeverity = []string{"warning", "critical"}
)

// CountAndOldest 表示某状态的数量和最早记录创建时间 Unix 秒。
type CountAndOldest struct {
	Count                 int64
	OldestCreatedUnixTime float64
}

// CursorSnapshot 表示一个链扫描游标的当前进度。
type CursorSnapshot struct {
	NextHeight      int64
	UpdatedUnixTime float64
}

// Snapshot 是同一个数据库可重复读事务内得到的业务风险视图。
type Snapshot struct {
	OpenReconciliationCases map[string]int64
	Payouts                 map[string]CountAndOldest
	Outbox                  map[string]CountAndOldest
	IndexerCursors          map[string]CursorSnapshot
	LastReconciliationRun   map[string]float64
	LastWalletSnapshot      map[string]float64
}

// Source 读取一次业务风险快照。
type Source interface {
	Snapshot(context.Context) (Snapshot, error)
}
