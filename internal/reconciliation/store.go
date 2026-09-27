// Package reconciliation 负责只读检测账实差异并保存可审计工单。
package reconciliation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/identity"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	RunKindLedgerIntegrity = "ledger_integrity"
	RunKindWalletAssets    = "wallet_assets"
	SeverityWarning        = "warning"
	SeverityCritical       = "critical"
	reconciliationLockKey  = int64(0x7374677265636f6e)
)

var (
	ErrDatabaseRequired = errors.New("数据库连接不能为空")
	ErrRunInProgress    = errors.New("已有对账任务正在执行")
	ErrInvalidList      = errors.New("对账工单查询请求无效")
	ErrInvalidResolve   = errors.New("对账工单关闭请求无效")
	ErrCaseNotOpen      = errors.New("对账工单不存在或已关闭")
)

// RunResult 是一次一致性快照对账的结果。
type RunResult struct {
	RunID        string    `json:"run_id"`
	Kind         string    `json:"kind"`
	SnapshotAt   time.Time `json:"snapshot_at"`
	CheckedItems int64     `json:"checked_items"`
	FindingCount int       `json:"finding_count"`
}

// ResolveRequest 包含关闭差异工单所需的审计信息。
type ResolveRequest struct {
	CaseID string
	Actor  string
	Reason string
}

// Case 是运营人员需要处置的对账差异工单。
type Case struct {
	ID              string    `json:"id"`
	RuleCode        string    `json:"rule_code"`
	Severity        string    `json:"severity"`
	ResourceType    string    `json:"resource_type"`
	ResourceID      string    `json:"resource_id"`
	AssetID         string    `json:"asset_id,omitempty"`
	OccurrenceCount int64     `json:"occurrence_count"`
	OpenedAt        time.Time `json:"opened_at"`
	LastSeenAt      time.Time `json:"last_seen_at"`
}

type finding struct {
	RuleCode     string
	Severity     string
	ResourceType string
	ResourceID   string
	AssetID      string
	Evidence     []byte
}

type detector func(context.Context, pgx.Tx) ([]finding, int64, error)

// Store 使用 PostgreSQL 保存对账运行、差异观察和工单。
type Store struct {
	db *pgxpool.Pool
}

// NewStore 创建对账 Store。
func NewStore(db *pgxpool.Pool) (*Store, error) {
	if db == nil {
		return nil, ErrDatabaseRequired
	}
	return &Store{db: db}, nil
}

// RunLedgerIntegrity 在同一可重复读快照中检测账务及业务引用差异。
func (store *Store) RunLedgerIntegrity(ctx context.Context) (result RunResult, err error) {
	return store.run(ctx, RunKindLedgerIntegrity, []detector{
		detectUnbalancedTransactions,
		detectDepositLedgerMismatches,
		detectDepositLedgerSemanticMismatches,
		detectPayoutLedgerMismatches,
		detectPayoutLedgerSemanticMismatches,
	})
}

// RunWalletAssets 比较同一快照内的链上托管余额与账本托管余额。
func (store *Store) RunWalletAssets(ctx context.Context) (result RunResult, err error) {
	return store.run(ctx, RunKindWalletAssets, []detector{detectWalletCustodyBalanceMismatches})
}

func (store *Store) run(ctx context.Context, kind string, detectors []detector) (result RunResult, err error) {
	transaction, err := store.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return RunResult{}, fmt.Errorf("开始对账事务: %w", err)
	}
	defer func() {
		if rollbackErr := transaction.Rollback(context.Background()); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			err = errors.Join(err, fmt.Errorf("回滚对账事务: %w", rollbackErr))
		}
	}()
	var acquired bool
	if err = transaction.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1)`, reconciliationLockKey).Scan(&acquired); err != nil {
		return RunResult{}, fmt.Errorf("获取对账任务锁: %w", err)
	}
	if !acquired {
		return RunResult{}, ErrRunInProgress
	}

	result.Kind = kind
	if err = transaction.QueryRow(ctx, `SELECT CURRENT_TIMESTAMP`).Scan(&result.SnapshotAt); err != nil {
		return RunResult{}, fmt.Errorf("读取对账快照时间: %w", err)
	}
	result.RunID, err = identity.NewUUID()
	if err != nil {
		return RunResult{}, err
	}

	findings := make([]finding, 0)
	for _, detect := range detectors {
		var detected []finding
		var checked int64
		detected, checked, err = detect(ctx, transaction)
		if err != nil {
			return RunResult{}, err
		}
		result.CheckedItems += checked
		findings = append(findings, detected...)
	}
	result.FindingCount = len(findings)

	if _, err = transaction.Exec(ctx, `
		INSERT INTO reconciliation_runs (
			id, kind, snapshot_at, checked_items, finding_count
		) VALUES ($1, $2, $3, $4, $5)
	`, result.RunID, result.Kind, result.SnapshotAt, result.CheckedItems, result.FindingCount); err != nil {
		return RunResult{}, fmt.Errorf("记录对账运行: %w", err)
	}
	for _, detected := range findings {
		if err = store.recordFinding(ctx, transaction, result, detected); err != nil {
			return RunResult{}, err
		}
	}
	if err = transaction.Commit(ctx); err != nil {
		return RunResult{}, fmt.Errorf("提交对账事务: %w", err)
	}
	return result, nil
}

// ListOpenCases 返回最近出现的未关闭差异工单。
func (store *Store) ListOpenCases(ctx context.Context, limit int) ([]Case, error) {
	if limit < 1 || limit > 1000 {
		return nil, ErrInvalidList
	}
	rows, err := store.db.Query(ctx, `
		SELECT id::TEXT, rule_code, severity, resource_type, resource_id,
		       COALESCE(asset_id, ''), occurrence_count, opened_at, last_seen_at
		FROM reconciliation_cases
		WHERE status = 'open'
		ORDER BY CASE severity WHEN 'critical' THEN 0 ELSE 1 END,
		         last_seen_at DESC, id
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("查询未关闭对账工单: %w", err)
	}
	defer rows.Close()
	cases := make([]Case, 0)
	for rows.Next() {
		var item Case
		if err := rows.Scan(
			&item.ID, &item.RuleCode, &item.Severity, &item.ResourceType,
			&item.ResourceID, &item.AssetID, &item.OccurrenceCount,
			&item.OpenedAt, &item.LastSeenAt,
		); err != nil {
			return nil, fmt.Errorf("读取未关闭对账工单: %w", err)
		}
		cases = append(cases, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历未关闭对账工单: %w", err)
	}
	return cases, nil
}

func (store *Store) recordFinding(ctx context.Context, transaction pgx.Tx, run RunResult, detected finding) error {
	if err := validateFinding(detected); err != nil {
		return err
	}
	fingerprint := findingFingerprint(detected)
	caseID, err := identity.NewUUID()
	if err != nil {
		return err
	}
	err = transaction.QueryRow(ctx, `
		INSERT INTO reconciliation_cases (
			id, fingerprint, rule_code, severity, resource_type, resource_id, asset_id,
			status, first_run_id, last_run_id, occurrence_count, opened_at, last_seen_at
		) VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, ''), 'open', $8, $8, 1, $9, $9)
		ON CONFLICT (fingerprint) WHERE status = 'open'
		DO UPDATE SET
			last_run_id = EXCLUDED.last_run_id,
			occurrence_count = reconciliation_cases.occurrence_count + 1,
			last_seen_at = EXCLUDED.last_seen_at
		RETURNING id::TEXT
	`, caseID, fingerprint, detected.RuleCode, detected.Severity,
		detected.ResourceType, detected.ResourceID, detected.AssetID, run.RunID, run.SnapshotAt,
	).Scan(&caseID)
	if err != nil {
		return fmt.Errorf("创建或更新对账工单: %w", err)
	}
	if _, err = transaction.Exec(ctx, `
		INSERT INTO reconciliation_observations (run_id, case_id, evidence, observed_at)
		VALUES ($1, $2, $3::JSONB, $4)
	`, run.RunID, caseID, string(detected.Evidence), run.SnapshotAt); err != nil {
		return fmt.Errorf("记录对账差异观察: %w", err)
	}
	return nil
}

// ResolveCase 原子关闭工单并追加不可修改的操作审计。
func (store *Store) ResolveCase(ctx context.Context, request ResolveRequest) (err error) {
	request.CaseID = strings.TrimSpace(request.CaseID)
	request.Actor = strings.TrimSpace(request.Actor)
	request.Reason = strings.TrimSpace(request.Reason)
	if !identity.ValidUUID(request.CaseID) || request.Actor == "" || len(request.Actor) > 128 ||
		request.Reason == "" || len(request.Reason) > 512 {
		return ErrInvalidResolve
	}
	transaction, err := store.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return fmt.Errorf("开始关闭对账工单事务: %w", err)
	}
	defer func() {
		if rollbackErr := transaction.Rollback(context.Background()); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			err = errors.Join(err, fmt.Errorf("回滚关闭对账工单事务: %w", rollbackErr))
		}
	}()
	command, err := transaction.Exec(ctx, `
		UPDATE reconciliation_cases
		SET status = 'resolved', resolved_at = CURRENT_TIMESTAMP,
		    resolved_by = $2, resolution_reason = $3
		WHERE id = $1 AND status = 'open'
	`, request.CaseID, request.Actor, request.Reason)
	if err != nil {
		return fmt.Errorf("关闭对账工单: %w", err)
	}
	if command.RowsAffected() != 1 {
		return ErrCaseNotOpen
	}
	actionID, err := identity.NewUUID()
	if err != nil {
		return err
	}
	if _, err = transaction.Exec(ctx, `
		INSERT INTO reconciliation_case_actions (id, case_id, action, actor, reason)
		VALUES ($1, $2, 'resolved', $3, $4)
	`, actionID, request.CaseID, request.Actor, request.Reason); err != nil {
		return fmt.Errorf("记录对账工单操作: %w", err)
	}
	if err = transaction.Commit(ctx); err != nil {
		return fmt.Errorf("提交关闭对账工单事务: %w", err)
	}
	return nil
}

func validateFinding(detected finding) error {
	if detected.RuleCode == "" || len(detected.RuleCode) > 128 ||
		(detected.Severity != SeverityWarning && detected.Severity != SeverityCritical) ||
		detected.ResourceType == "" || len(detected.ResourceType) > 64 ||
		detected.ResourceID == "" || len(detected.ResourceID) > 256 || !json.Valid(detected.Evidence) {
		return errors.New("对账差异无效")
	}
	return nil
}

func findingFingerprint(detected finding) string {
	hash := sha256.Sum256([]byte(strings.Join([]string{
		detected.RuleCode,
		detected.ResourceType,
		detected.ResourceID,
		detected.AssetID,
	}, "\x00")))
	return hex.EncodeToString(hash[:])
}
