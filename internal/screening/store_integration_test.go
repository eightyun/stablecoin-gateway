//go:build integration

package screening

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
	"github.com/eightyun/stablecoin-gateway/internal/payout"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestStoreLeasesPersistsAndEnforcesScreeningDecision(t *testing.T) {
	pool, payoutStore, merchantID, assetID := newScreeningFixture(t)
	created, err := payoutStore.Create(context.Background(), payout.Request{
		ID: mustScreeningUUID(t), MerchantID: merchantID, AssetID: assetID,
		IdempotencyKey: "screening-lease", MerchantReference: "screening-order",
		DestinationAddress: "T9yD14Nj9j7xAB4dbGeiX9h8unkKHxuWwb", Amount: "60",
	})
	if err != nil {
		t.Fatalf("创建测试出款: %v", err)
	}
	store, err := NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	first, err := store.Claim(context.Background(), "screening-1", 20*time.Millisecond)
	if err != nil || first.PayoutID != created.Payout.ID || first.LeaseEpoch != 1 ||
		first.Request.DestinationAddress != "410000000000000000000000000000000000000000" ||
		first.Request.Amount != "60" {
		t.Fatalf("第一次 Claim() = %+v, %v", first, err)
	}
	time.Sleep(30 * time.Millisecond)
	second, err := store.Claim(context.Background(), "screening-2", time.Minute)
	if err != nil || second.PayoutID != created.Payout.ID || second.LeaseEpoch != 2 {
		t.Fatalf("接管 Claim() = %+v, %v", second, err)
	}
	now := time.Now().UTC()
	result := Result{
		Provider: "integration-provider", Decision: DecisionDeny,
		ReasonCodes: []string{"sanctions_match"}, ProviderReference: "provider-reference",
		ResponseHash: strings.Repeat("b", 64), CheckedAt: now, ValidUntil: now.Add(time.Second),
	}
	if err := store.Complete(context.Background(), first, result); !errors.Is(err, ErrScreeningLeaseLost) {
		t.Fatalf("旧租约 Complete() error = %v", err)
	}
	if err := store.Complete(context.Background(), second, result); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if _, err := store.Claim(context.Background(), "screening-3", time.Minute); !errors.Is(err, ErrNoScreeningJob) {
		t.Fatalf("有效结果后 Claim() error = %v", err)
	}
	if _, err := payoutStore.Review(context.Background(), payout.ReviewRequest{
		PayoutID: created.Payout.ID, Decision: payout.DecisionApprove,
		Reviewer: "risk@example.com", Reason: "incorrect approval attempt",
	}); !errors.Is(err, payout.ErrScreeningApprovalRequired) {
		t.Fatalf("deny 结果后批准 error = %v", err)
	}
	actionRequired, err := store.ListActionRequired(context.Background(), 100)
	if err != nil || !containsActionRequired(actionRequired, created.Payout.ID, DecisionDeny) {
		t.Fatalf("ListActionRequired() = %+v, %v", actionRequired, err)
	}
	time.Sleep(time.Until(result.ValidUntil) + 50*time.Millisecond)
	expiredClaim, err := store.Claim(context.Background(), "screening-3", time.Minute)
	if err != nil || expiredClaim.PayoutID != created.Payout.ID || expiredClaim.LeaseEpoch != 3 {
		t.Fatalf("结果过期后 Claim() = %+v, %v", expiredClaim, err)
	}
	if _, err := payoutStore.Review(context.Background(), payout.ReviewRequest{
		PayoutID: created.Payout.ID, Decision: payout.DecisionReject,
		Reviewer: "risk@example.com", Reason: "screening denied destination",
	}); err != nil {
		t.Fatalf("deny 结果后拒绝 error = %v", err)
	}
	actionRequired, err = store.ListActionRequired(context.Background(), 100)
	if err != nil || containsActionRequired(actionRequired, created.Payout.ID, DecisionDeny) {
		t.Fatalf("拒绝后 ListActionRequired() = %+v, %v", actionRequired, err)
	}
	var jobStatus string
	if err := pool.QueryRow(context.Background(), `
		SELECT status FROM payout_screening_jobs WHERE payout_id = $1
	`, created.Payout.ID).Scan(&jobStatus); err != nil || jobStatus != "closed" {
		t.Fatalf("拒绝后筛查任务状态 = %q, %v", jobStatus, err)
	}
	lateResult := result
	lateResult.CheckedAt = time.Now().UTC()
	lateResult.ValidUntil = lateResult.CheckedAt.Add(time.Hour)
	if err := store.Complete(context.Background(), expiredClaim, lateResult); !errors.Is(err, ErrScreeningLeaseLost) {
		t.Fatalf("关闭后 Complete() error = %v", err)
	}
	assertScreeningResultImmutable(t, pool, created.Payout.ID)
}

func containsActionRequired(results []ActionRequired, payoutID, decision string) bool {
	for _, result := range results {
		if result.PayoutID == payoutID && result.Decision == decision {
			return true
		}
	}
	return false
}

func newScreeningFixture(t *testing.T) (*pgxpool.Pool, *payout.Store, string, string) {
	t.Helper()
	databaseURL := os.Getenv("GATEWAY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("未设置 GATEWAY_TEST_DATABASE_URL")
	}
	ctx := context.Background()
	pool, err := database.Open(ctx, database.DefaultConfig(databaseURL, "screening-integration-test"))
	if err != nil {
		t.Fatalf("连接测试数据库: %v", err)
	}
	t.Cleanup(pool.Close)
	merchantID := mustScreeningUUID(t)
	assetID := "screening-asset-" + mustScreeningUUID(t)
	custodyID, availableID, frozenID := mustScreeningUUID(t), mustScreeningUUID(t), mustScreeningUUID(t)
	if _, err := pool.Exec(ctx, `
		INSERT INTO assets (id, network, contract_address, symbol, decimals, status)
		VALUES ($1, $2, $3, 'USDT', 6, 'active')
	`, assetID, "tron-nile-"+mustScreeningUUID(t), "41"+strings.ReplaceAll(mustScreeningUUID(t), "-", "")); err != nil {
		t.Fatalf("创建筛查测试资产: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO merchants (id, name, status) VALUES ($1, 'screening merchant', 'active')
	`, merchantID); err != nil {
		t.Fatalf("创建筛查测试商户: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO ledger_accounts (id, owner_type, owner_id, asset_id, code, normal_side, status)
		VALUES
			($1, 'platform', 'gateway', $4, 'custody', 'D', 'active'),
			($2, 'merchant', $5, $4, 'available', 'C', 'active'),
			($3, 'merchant', $5, $4, 'frozen', 'C', 'active')
	`, custodyID, availableID, frozenID, assetID, merchantID); err != nil {
		t.Fatalf("创建筛查测试科目: %v", err)
	}
	repository, err := ledger.NewPostgreSQLRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	seedID := mustScreeningUUID(t)
	if _, err := repository.Post(ctx, ledger.Transaction{
		ID: seedID, RequesterType: "system", RequesterID: "screening-test",
		IdempotencyKey: "seed:" + seedID, ReferenceType: "seed", ReferenceID: seedID,
		Entries: []ledger.Entry{
			{AccountID: custodyID, AssetID: assetID, Side: ledger.Debit, Amount: 100},
			{AccountID: availableID, AssetID: assetID, Side: ledger.Credit, Amount: 100},
		},
	}); err != nil {
		t.Fatalf("准备筛查测试余额: %v", err)
	}
	payoutStore, err := payout.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	return pool, payoutStore, merchantID, assetID
}

func assertScreeningResultImmutable(t *testing.T, pool *pgxpool.Pool, payoutID string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		UPDATE payout_screening_results SET decision = 'allow' WHERE payout_id = $1
	`, payoutID); err == nil || !strings.Contains(err.Error(), "payout screening results are immutable") {
		t.Fatalf("修改筛查结果 error = %v", err)
	}
	if _, err := pool.Exec(context.Background(), `
		DELETE FROM payout_screening_results WHERE payout_id = $1
	`, payoutID); err == nil || !strings.Contains(err.Error(), "payout screening results are immutable") {
		t.Fatalf("删除筛查结果 error = %v", err)
	}
}

func mustScreeningUUID(t *testing.T) string {
	t.Helper()
	value, err := identity.NewUUID()
	if err != nil {
		t.Fatalf("生成测试 UUID: %v", err)
	}
	return value
}
