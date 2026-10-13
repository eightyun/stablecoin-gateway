package reconciliation

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// AuditFinding 是只读恢复审计发现的资金一致性问题。
type AuditFinding struct {
	RuleCode     string          `json:"rule_code"`
	Severity     string          `json:"severity"`
	ResourceType string          `json:"resource_type"`
	ResourceID   string          `json:"resource_id"`
	AssetID      string          `json:"asset_id,omitempty"`
	Evidence     json.RawMessage `json:"evidence"`
}

// AuditResult 是同一调用方事务快照中的只读检测结果。
type AuditResult struct {
	Kind          string         `json:"kind"`
	SnapshotAt    time.Time      `json:"snapshot_at"`
	CheckedItems  int64          `json:"checked_items"`
	CriticalCount int            `json:"critical_count"`
	WarningCount  int            `json:"warning_count"`
	Findings      []AuditFinding `json:"findings"`
}

// AuditLedgerIntegrityInTransaction 只读检测账本平衡及充值、出款业务引用语义。
func AuditLedgerIntegrityInTransaction(ctx context.Context, transaction pgx.Tx) (AuditResult, error) {
	return auditInTransaction(ctx, transaction, RunKindLedgerIntegrity, []detector{
		detectUnbalancedTransactions,
		detectDepositLedgerMismatches,
		detectDepositLedgerSemanticMismatches,
		detectPayoutLedgerMismatches,
		detectPayoutLedgerSemanticMismatches,
	})
}

// AuditWalletAssetsInTransaction 只读比较最新固化钱包快照和托管账本检查点。
func AuditWalletAssetsInTransaction(ctx context.Context, transaction pgx.Tx) (AuditResult, error) {
	return auditInTransaction(ctx, transaction, RunKindWalletAssets, []detector{
		detectWalletCustodyBalanceMismatches,
	})
}

func auditInTransaction(
	ctx context.Context,
	transaction pgx.Tx,
	kind string,
	detectors []detector,
) (AuditResult, error) {
	result := AuditResult{Kind: kind, Findings: make([]AuditFinding, 0)}
	if err := transaction.QueryRow(ctx, `SELECT CURRENT_TIMESTAMP`).Scan(&result.SnapshotAt); err != nil {
		return AuditResult{}, fmt.Errorf("读取只读对账快照时间: %w", err)
	}
	for _, detect := range detectors {
		findings, checked, err := detect(ctx, transaction)
		if err != nil {
			return AuditResult{}, err
		}
		result.CheckedItems += checked
		for _, detected := range findings {
			switch detected.Severity {
			case SeverityCritical:
				result.CriticalCount++
			case SeverityWarning:
				result.WarningCount++
			default:
				return AuditResult{}, fmt.Errorf("只读对账发现未知严重级别: %s", detected.Severity)
			}
			result.Findings = append(result.Findings, AuditFinding{
				RuleCode: detected.RuleCode, Severity: detected.Severity,
				ResourceType: detected.ResourceType, ResourceID: detected.ResourceID,
				AssetID: detected.AssetID, Evidence: append(json.RawMessage(nil), detected.Evidence...),
			})
		}
	}
	return result, nil
}
