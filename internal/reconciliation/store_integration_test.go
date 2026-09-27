//go:build integration

package reconciliation

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/database"
	"github.com/eightyun/stablecoin-gateway/internal/identity"
	"github.com/eightyun/stablecoin-gateway/internal/ledger"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestStoreReusesResolvesAndReopensReconciliationCase(t *testing.T) {
	fixture := newReconciliationFixture(t)
	first, err := fixture.store.RunLedgerIntegrity(context.Background())
	if err != nil || first.FindingCount < 1 || first.CheckedItems < 2 {
		t.Fatalf("首次 RunLedgerIntegrity() = %+v, %v", first, err)
	}
	caseID, status, occurrences := fixture.caseState(t, fixture.intentID, "open")
	if occurrences != 1 {
		t.Fatalf("首次工单 occurrences=%d", occurrences)
	}
	openCases, err := fixture.store.ListOpenCases(context.Background(), 1000)
	if err != nil || !containsCase(openCases, caseID) {
		t.Fatalf("ListOpenCases() 未返回新工单 id=%s cases=%+v error=%v", caseID, openCases, err)
	}
	fixture.assertObservation(t, first.RunID, caseID)

	second, err := fixture.store.RunLedgerIntegrity(context.Background())
	if err != nil || second.RunID == first.RunID {
		t.Fatalf("重复 RunLedgerIntegrity() = %+v, %v", second, err)
	}
	reusedID, status, occurrences := fixture.caseState(t, fixture.intentID, "open")
	if reusedID != caseID || status != "open" || occurrences != 2 {
		t.Fatalf("重复工单 id=%s status=%s occurrences=%d", reusedID, status, occurrences)
	}
	fixture.assertObservation(t, second.RunID, caseID)

	request := ResolveRequest{CaseID: caseID, Actor: "ops@example.com", Reason: "已核实测试差异"}
	if err := fixture.store.ResolveCase(context.Background(), request); err != nil {
		t.Fatalf("ResolveCase() error = %v", err)
	}
	if err := fixture.store.ResolveCase(context.Background(), request); !errors.Is(err, ErrCaseNotOpen) {
		t.Fatalf("重复 ResolveCase() error = %v", err)
	}
	fixture.assertResolution(t, caseID, request)

	third, err := fixture.store.RunLedgerIntegrity(context.Background())
	if err != nil {
		t.Fatalf("再次 RunLedgerIntegrity() error = %v", err)
	}
	reopenedID, _, reopenedOccurrences := fixture.caseState(t, fixture.intentID, "open")
	if reopenedID == caseID || reopenedOccurrences != 1 {
		t.Fatalf("重新打开工单 id=%s old=%s occurrences=%d", reopenedID, caseID, reopenedOccurrences)
	}
	fixture.assertObservation(t, third.RunID, reopenedID)
	fixture.assertImmutable(t, first.RunID, caseID)
	fixture.assertConcurrentRunRejected(t)
}

type reconciliationFixture struct {
	store    *Store
	pool     *pgxpool.Pool
	intentID string
}

func newReconciliationFixture(t *testing.T) reconciliationFixture {
	t.Helper()
	databaseURL := os.Getenv("GATEWAY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("未设置 GATEWAY_TEST_DATABASE_URL")
	}
	ctx := context.Background()
	pool, err := database.Open(ctx, database.DefaultConfig(databaseURL, "reconciliation-integration-test"))
	if err != nil {
		t.Fatalf("连接测试数据库: %v", err)
	}
	t.Cleanup(pool.Close)
	assetID := "recon-" + reconciliationUUID(t)
	merchantID := reconciliationUUID(t)
	custodyAccountID := reconciliationUUID(t)
	availableAccountID := reconciliationUUID(t)
	if _, err := pool.Exec(ctx, `
		INSERT INTO assets (id, network, contract_address, symbol, decimals, status)
		VALUES ($1, $2, $3, 'USDT', 6, 'active')
	`, assetID, "tron-reconciliation", "contract-"+assetID); err != nil {
		t.Fatalf("创建测试资产: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO merchants (id, name, status) VALUES ($1, 'reconciliation merchant', 'active')
	`, merchantID); err != nil {
		t.Fatalf("创建测试商户: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO ledger_accounts (id, owner_type, owner_id, asset_id, code, normal_side, status)
		VALUES
			($1, 'platform', 'gateway', $3, 'custody', 'D', 'active'),
			($2, 'merchant', $4, $3, 'available', 'C', 'active')
	`, custodyAccountID, availableAccountID, assetID, merchantID); err != nil {
		t.Fatalf("创建测试账本科目: %v", err)
	}
	repository, err := ledger.NewPostgreSQLRepository(pool)
	if err != nil {
		t.Fatalf("NewPostgreSQLRepository() error = %v", err)
	}
	journalID := reconciliationUUID(t)
	if _, err := repository.Post(ctx, ledger.Transaction{
		ID: journalID, RequesterType: "system", RequesterID: "reconciliation-test",
		IdempotencyKey: "seed:" + journalID, ReferenceType: "seed", ReferenceID: journalID,
		Entries: []ledger.Entry{
			{AccountID: custodyAccountID, AssetID: assetID, Side: ledger.Debit, Amount: 100},
			{AccountID: availableAccountID, AssetID: assetID, Side: ledger.Credit, Amount: 100},
		},
	}); err != nil {
		t.Fatalf("准备测试账务交易: %v", err)
	}
	addressID := reconciliationUUID(t)
	if _, err := pool.Exec(ctx, `
		INSERT INTO deposit_addresses (id, merchant_id, asset_id, address, status)
		VALUES ($1, $2, $3, $4, 'active')
	`, addressID, merchantID, assetID, "reconciliation-address-"+addressID); err != nil {
		t.Fatalf("创建测试充值地址: %v", err)
	}
	intentID := reconciliationUUID(t)
	if _, err := pool.Exec(ctx, `
		INSERT INTO deposit_intents (
			id, merchant_id, asset_id, deposit_address_id, idempotency_key,
			merchant_reference, request_hash, expected_amount, received_amount,
			status, expires_at, ledger_transaction_id, credited_amount, credited_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, 100, 100, 'paid',
		          CURRENT_TIMESTAMP + INTERVAL '1 hour', $8, 100, CURRENT_TIMESTAMP)
	`, intentID, merchantID, assetID, addressID, "recon-idem-"+intentID,
		"recon-ref-"+intentID, strings.Repeat("a", 64), journalID); err != nil {
		t.Fatalf("创建账务引用错配充值: %v", err)
	}
	store, err := NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	fixture := reconciliationFixture{store: store, pool: pool, intentID: intentID}
	t.Cleanup(func() { fixture.cleanup(t) })
	return fixture
}

func (fixture reconciliationFixture) caseState(t *testing.T, intentID, expectedStatus string) (string, string, int64) {
	t.Helper()
	var caseID, status string
	var occurrences int64
	if err := fixture.pool.QueryRow(context.Background(), `
		SELECT id::TEXT, status, occurrence_count
		FROM reconciliation_cases
		WHERE rule_code = $1 AND resource_type = 'deposit_intent'
		  AND resource_id = $2 AND status = $3
		ORDER BY opened_at DESC LIMIT 1
	`, ruleDepositLedgerMismatch, intentID, expectedStatus).Scan(&caseID, &status, &occurrences); err != nil {
		t.Fatalf("查询对账工单: %v", err)
	}
	return caseID, status, occurrences
}

func (fixture reconciliationFixture) assertObservation(t *testing.T, runID, caseID string) {
	t.Helper()
	var evidence string
	if err := fixture.pool.QueryRow(context.Background(), `
		SELECT evidence::TEXT FROM reconciliation_observations
		WHERE run_id = $1 AND case_id = $2
	`, runID, caseID).Scan(&evidence); err != nil || !strings.Contains(evidence, `"expected_reference_type": "deposit"`) {
		t.Fatalf("对账观察 evidence=%s error=%v", evidence, err)
	}
}

func (fixture reconciliationFixture) assertResolution(t *testing.T, caseID string, request ResolveRequest) {
	t.Helper()
	var status, actor, reason string
	var resolvedAt time.Time
	if err := fixture.pool.QueryRow(context.Background(), `
		SELECT status, resolved_by, resolution_reason, resolved_at
		FROM reconciliation_cases WHERE id = $1
	`, caseID).Scan(&status, &actor, &reason, &resolvedAt); err != nil || status != "resolved" ||
		actor != request.Actor || reason != request.Reason || resolvedAt.IsZero() {
		t.Fatalf("关闭工单 status=%s actor=%s reason=%s resolved_at=%s error=%v",
			status, actor, reason, resolvedAt, err)
	}
	var actionCount int
	if err := fixture.pool.QueryRow(context.Background(), `
		SELECT COUNT(*) FROM reconciliation_case_actions
		WHERE case_id = $1 AND action = 'resolved' AND actor = $2 AND reason = $3
	`, caseID, request.Actor, request.Reason).Scan(&actionCount); err != nil || actionCount != 1 {
		t.Fatalf("工单操作审计数量=%d error=%v", actionCount, err)
	}
}

func (fixture reconciliationFixture) assertImmutable(t *testing.T, runID, caseID string) {
	t.Helper()
	if _, err := fixture.pool.Exec(context.Background(), `
		UPDATE reconciliation_runs SET finding_count = 0 WHERE id = $1
	`, runID); err == nil || !strings.Contains(err.Error(), "reconciliation_runs rows are immutable") {
		t.Fatalf("修改对账运行 error=%v", err)
	}
	if _, err := fixture.pool.Exec(context.Background(), `
		UPDATE reconciliation_observations SET evidence = '{}' WHERE run_id = $1 AND case_id = $2
	`, runID, caseID); err == nil || !strings.Contains(err.Error(), "reconciliation_observations rows are immutable") {
		t.Fatalf("修改对账观察 error=%v", err)
	}
	if _, err := fixture.pool.Exec(context.Background(), `
		UPDATE reconciliation_case_actions SET reason = 'tampered' WHERE case_id = $1
	`, caseID); err == nil || !strings.Contains(err.Error(), "reconciliation_case_actions rows are immutable") {
		t.Fatalf("修改工单操作审计 error=%v", err)
	}
}

func (fixture reconciliationFixture) assertConcurrentRunRejected(t *testing.T) {
	t.Helper()
	transaction, err := fixture.pool.Begin(context.Background())
	if err != nil {
		t.Fatalf("开始对账锁测试事务: %v", err)
	}
	defer func() { _ = transaction.Rollback(context.Background()) }()
	if _, err := transaction.Exec(context.Background(), `SELECT pg_advisory_xact_lock($1)`, reconciliationLockKey); err != nil {
		t.Fatalf("获取对账锁: %v", err)
	}
	if _, err := fixture.store.RunLedgerIntegrity(context.Background()); !errors.Is(err, ErrRunInProgress) {
		t.Fatalf("并发 RunLedgerIntegrity() error = %v", err)
	}
}

func (fixture reconciliationFixture) cleanup(t *testing.T) {
	ctx := context.Background()
	if _, err := fixture.pool.Exec(ctx, `
		UPDATE deposit_intents
		SET status = 'pending', received_amount = 0,
		    ledger_transaction_id = NULL, credited_amount = 0, credited_at = NULL,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $1
	`, fixture.intentID); err != nil {
		t.Errorf("清理对账测试充值意图: %v", err)
		return
	}
	rows, err := fixture.pool.Query(ctx, `
		SELECT id::TEXT FROM reconciliation_cases
		WHERE resource_type = 'deposit_intent' AND resource_id = $1 AND status = 'open'
	`, fixture.intentID)
	if err != nil {
		t.Errorf("查询待清理对账工单: %v", err)
		return
	}
	caseIDs := make([]string, 0)
	for rows.Next() {
		var caseID string
		if err := rows.Scan(&caseID); err != nil {
			rows.Close()
			t.Errorf("读取待清理对账工单: %v", err)
			return
		}
		caseIDs = append(caseIDs, caseID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Errorf("遍历待清理对账工单: %v", err)
		return
	}
	rows.Close()
	for _, caseID := range caseIDs {
		if err := fixture.store.ResolveCase(ctx, ResolveRequest{
			CaseID: caseID, Actor: "integration-test", Reason: "测试差异已清理",
		}); err != nil {
			t.Errorf("关闭测试对账工单 %s: %v", caseID, err)
		}
	}
}

func reconciliationUUID(t *testing.T) string {
	t.Helper()
	value, err := identity.NewUUID()
	if err != nil {
		t.Fatalf("生成 UUID: %v", err)
	}
	return value
}

func containsCase(cases []Case, caseID string) bool {
	for _, item := range cases {
		if item.ID == caseID {
			return true
		}
	}
	return false
}
