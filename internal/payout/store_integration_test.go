//go:build integration

package payout

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/eightyun/stablecoin-gateway/internal/database"
	"github.com/eightyun/stablecoin-gateway/internal/deposit"
	"github.com/eightyun/stablecoin-gateway/internal/identity"
	"github.com/eightyun/stablecoin-gateway/internal/ledger"
	"github.com/jackc/pgx/v5/pgxpool"
)

const testDestination = "T9yD14Nj9j7xAB4dbGeiX9h8unkKHxuWwb"

type payoutFixture struct {
	store              *Store
	pool               *pgxpool.Pool
	merchantID         string
	assetID            string
	availableAccountID string
	frozenAccountID    string
}

func TestStoreCreatesPayoutAndFreezesBalance(t *testing.T) {
	fixture := newPayoutFixture(t, 100)
	request := fixture.request(t, "idem-1", "withdrawal-1", "60")
	created, err := fixture.store.Create(context.Background(), request)
	if err != nil || !created.Created || created.Payout.Status != "pending_review" {
		t.Fatalf("Create() = %+v, %v", created, err)
	}
	available, frozen := fixture.balances(t)
	if available != "40" || frozen != "60" {
		t.Fatalf("余额 available=%s frozen=%s", available, frozen)
	}
	depositStore, err := deposit.NewStore(fixture.pool)
	if err != nil {
		t.Fatalf("deposit.NewStore() error = %v", err)
	}
	balances, err := depositStore.ListBalances(context.Background(), fixture.merchantID)
	if err != nil || len(balances) != 1 || balances[0].Available != "40" || balances[0].Frozen != "60" {
		t.Fatalf("ListBalances() = %+v, %v", balances, err)
	}
	assertFreezeJournal(t, fixture.pool, created.Payout.FreezeTransactionID, fixture.availableAccountID, fixture.frozenAccountID, "60")

	retry := request
	retry.ID = payoutUUID(t)
	retried, err := fixture.store.Create(context.Background(), retry)
	if err != nil || retried.Created || retried.Payout.ID != created.Payout.ID {
		t.Fatalf("重复 Create() = %+v, %v", retried, err)
	}
	conflict := retry
	conflict.ID = payoutUUID(t)
	conflict.Amount = "61"
	if _, err := fixture.store.Create(context.Background(), conflict); !errors.Is(err, ErrPayoutConflict) {
		t.Fatalf("冲突 Create() error = %v", err)
	}
	insufficient := fixture.request(t, "idem-2", "withdrawal-2", "50")
	if _, err := fixture.store.Create(context.Background(), insufficient); !errors.Is(err, ErrInsufficientBalance) {
		t.Fatalf("余额不足 Create() error = %v", err)
	}
	if _, err := fixture.store.Get(context.Background(), payoutUUID(t), created.Payout.ID); !errors.Is(err, ErrPayoutNotFound) {
		t.Fatalf("跨商户 Get() error = %v", err)
	}
}

func TestStorePreventsConcurrentOverdraft(t *testing.T) {
	fixture := newPayoutFixture(t, 100)
	requests := []Request{
		fixture.request(t, "concurrent-1", "concurrent-order-1", "80"),
		fixture.request(t, "concurrent-2", "concurrent-order-2", "80"),
	}
	results := make(chan error, len(requests))
	var waitGroup sync.WaitGroup
	for _, request := range requests {
		waitGroup.Add(1)
		go func(request Request) {
			defer waitGroup.Done()
			_, err := fixture.store.Create(context.Background(), request)
			results <- err
		}(request)
	}
	waitGroup.Wait()
	close(results)
	succeeded, insufficient := 0, 0
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrInsufficientBalance):
			insufficient++
		default:
			t.Fatalf("并发 Create() error = %v", err)
		}
	}
	if succeeded != 1 || insufficient != 1 {
		t.Fatalf("并发结果 succeeded=%d insufficient=%d", succeeded, insufficient)
	}
	available, frozen := fixture.balances(t)
	if available != "20" || frozen != "80" {
		t.Fatalf("并发余额 available=%s frozen=%s", available, frozen)
	}
}

func TestStoreApprovesPayoutIdempotently(t *testing.T) {
	fixture := newPayoutFixture(t, 100)
	created, err := fixture.store.Create(context.Background(), fixture.request(t, "approve-1", "approve-order-1", "60"))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	request := ReviewRequest{
		PayoutID: created.Payout.ID, Decision: DecisionApprove,
		Reviewer: "risk@example.com", Reason: "manual screening passed",
	}
	result, err := fixture.store.Review(context.Background(), request)
	if err != nil || !result.Changed || result.Status != StatusReadyForBroadcast || result.UnfreezeTransactionID != "" {
		t.Fatalf("Review() = %+v, %v", result, err)
	}
	available, frozen := fixture.balances(t)
	if available != "40" || frozen != "60" {
		t.Fatalf("审批后余额 available=%s frozen=%s", available, frozen)
	}
	details, err := fixture.store.Get(context.Background(), fixture.merchantID, created.Payout.ID)
	if err != nil || details.Status != StatusReadyForBroadcast {
		t.Fatalf("Get() = %+v, %v", details, err)
	}
	retry, err := fixture.store.Review(context.Background(), request)
	if err != nil || retry.Changed || retry.ReviewedAt != result.ReviewedAt {
		t.Fatalf("重复 Review() = %+v, %v", retry, err)
	}
	request.Decision = DecisionReject
	if _, err := fixture.store.Review(context.Background(), request); !errors.Is(err, ErrPayoutStateConflict) {
		t.Fatalf("冲突 Review() error = %v", err)
	}
}

func TestStoreRejectsPayoutAndUnfreezesBalance(t *testing.T) {
	fixture := newPayoutFixture(t, 100)
	created, err := fixture.store.Create(context.Background(), fixture.request(t, "reject-1", "reject-order-1", "60"))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	request := ReviewRequest{
		PayoutID: created.Payout.ID, Decision: DecisionReject,
		Reviewer: "risk@example.com", Reason: "destination denied",
	}
	result, err := fixture.store.Review(context.Background(), request)
	if err != nil || !result.Changed || result.Status != StatusRejected || result.UnfreezeTransactionID == "" {
		t.Fatalf("Review() = %+v, %v", result, err)
	}
	available, frozen := fixture.balances(t)
	if available != "100" || frozen != "0" {
		t.Fatalf("拒绝后余额 available=%s frozen=%s", available, frozen)
	}
	assertFreezeJournal(
		t, fixture.pool, result.UnfreezeTransactionID,
		fixture.frozenAccountID, fixture.availableAccountID, "60",
	)
	retry, err := fixture.store.Review(context.Background(), request)
	if err != nil || retry.Changed || retry.UnfreezeTransactionID != result.UnfreezeTransactionID {
		t.Fatalf("重复 Review() = %+v, %v", retry, err)
	}
	request.Reason = "different reason"
	if _, err := fixture.store.Review(context.Background(), request); !errors.Is(err, ErrPayoutStateConflict) {
		t.Fatalf("冲突 Review() error = %v", err)
	}
}

func TestStoreSerializesConcurrentPayoutReviews(t *testing.T) {
	fixture := newPayoutFixture(t, 100)
	created, err := fixture.store.Create(context.Background(), fixture.request(t, "review-race", "review-race-order", "60"))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	requests := []ReviewRequest{
		{PayoutID: created.Payout.ID, Decision: DecisionApprove, Reviewer: "approver", Reason: "approved"},
		{PayoutID: created.Payout.ID, Decision: DecisionReject, Reviewer: "rejector", Reason: "rejected"},
	}
	results := make(chan error, len(requests))
	var waitGroup sync.WaitGroup
	for _, request := range requests {
		waitGroup.Add(1)
		go func(request ReviewRequest) {
			defer waitGroup.Done()
			_, err := fixture.store.Review(context.Background(), request)
			results <- err
		}(request)
	}
	waitGroup.Wait()
	close(results)
	succeeded, conflicted := 0, 0
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrPayoutStateConflict):
			conflicted++
		default:
			t.Fatalf("并发 Review() error = %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("并发审批结果 succeeded=%d conflicted=%d", succeeded, conflicted)
	}
	details, err := fixture.store.Get(context.Background(), fixture.merchantID, created.Payout.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	available, frozen := fixture.balances(t)
	if details.Status == StatusReadyForBroadcast && (available != "40" || frozen != "60") {
		t.Fatalf("审批胜出余额 available=%s frozen=%s", available, frozen)
	}
	if details.Status == StatusRejected && (available != "100" || frozen != "0") {
		t.Fatalf("拒绝胜出余额 available=%s frozen=%s", available, frozen)
	}
}

func newPayoutFixture(t *testing.T, initialBalance int64) payoutFixture {
	t.Helper()
	databaseURL := os.Getenv("GATEWAY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("未设置 GATEWAY_TEST_DATABASE_URL")
	}
	ctx := context.Background()
	pool, err := database.Open(ctx, database.DefaultConfig(databaseURL, "payout-integration-test"))
	if err != nil {
		t.Fatalf("连接测试数据库: %v", err)
	}
	t.Cleanup(pool.Close)
	merchantID := payoutUUID(t)
	assetID := "asset-" + payoutUUID(t)
	custodyAccountID := payoutUUID(t)
	availableAccountID := payoutUUID(t)
	frozenAccountID := payoutUUID(t)
	if _, err := pool.Exec(ctx, `
		INSERT INTO assets (id, network, contract_address, symbol, decimals, status)
		VALUES ($1, $2, $3, 'USDT', 6, 'active')
	`, assetID, "tron-nile-"+payoutUUID(t), "41"+payoutUUID(t)); err != nil {
		t.Fatalf("创建测试资产: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO merchants (id, name, status) VALUES ($1, 'payout test merchant', 'active')
	`, merchantID); err != nil {
		t.Fatalf("创建测试商户: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO ledger_accounts (id, owner_type, owner_id, asset_id, code, normal_side, status)
		VALUES
			($1, 'platform', 'gateway', $4, 'custody', 'D', 'active'),
			($2, 'merchant', $5, $4, 'available', 'C', 'active'),
			($3, 'merchant', $5, $4, 'frozen', 'C', 'active')
	`, custodyAccountID, availableAccountID, frozenAccountID, assetID, merchantID); err != nil {
		t.Fatalf("创建测试账本科目: %v", err)
	}
	repository, err := ledger.NewPostgreSQLRepository(pool)
	if err != nil {
		t.Fatalf("NewPostgreSQLRepository() error = %v", err)
	}
	seedID := payoutUUID(t)
	if _, err := repository.Post(ctx, ledger.Transaction{
		ID: seedID, RequesterType: "system", RequesterID: "payout-test",
		IdempotencyKey: "seed:" + seedID, ReferenceType: "seed", ReferenceID: seedID,
		Entries: []ledger.Entry{
			{AccountID: custodyAccountID, AssetID: assetID, Side: ledger.Debit, Amount: initialBalance},
			{AccountID: availableAccountID, AssetID: assetID, Side: ledger.Credit, Amount: initialBalance},
		},
	}); err != nil {
		t.Fatalf("准备初始余额: %v", err)
	}
	store, err := NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	return payoutFixture{
		store: store, pool: pool, merchantID: merchantID, assetID: assetID,
		availableAccountID: availableAccountID, frozenAccountID: frozenAccountID,
	}
}

func (fixture payoutFixture) request(t *testing.T, idempotencyKey, reference, amount string) Request {
	t.Helper()
	return Request{
		ID: payoutUUID(t), MerchantID: fixture.merchantID, AssetID: fixture.assetID,
		IdempotencyKey: idempotencyKey, MerchantReference: reference,
		DestinationAddress: testDestination, Amount: amount,
	}
}

func (fixture payoutFixture) balances(t *testing.T) (string, string) {
	t.Helper()
	ctx := context.Background()
	var available, frozen string
	if err := fixture.pool.QueryRow(ctx, `
		SELECT
			COALESCE(SUM(CASE WHEN account.id = $1 THEN
				CASE WHEN entry.side = account.normal_side THEN entry.amount ELSE -entry.amount END
			ELSE 0 END), 0)::TEXT,
			COALESCE(SUM(CASE WHEN account.id = $2 THEN
				CASE WHEN entry.side = account.normal_side THEN entry.amount ELSE -entry.amount END
			ELSE 0 END), 0)::TEXT
		FROM ledger_accounts AS account
		LEFT JOIN journal_entries AS entry ON entry.account_id = account.id
		LEFT JOIN journal_transactions AS journal ON journal.id = entry.transaction_id
		WHERE account.id IN ($1, $2) AND journal.status = 'posted'
	`, fixture.availableAccountID, fixture.frozenAccountID).Scan(&available, &frozen); err != nil {
		t.Fatalf("查询测试余额: %v", err)
	}
	return available, frozen
}

func assertFreezeJournal(
	t *testing.T,
	pool *pgxpool.Pool,
	journalID, availableAccountID, frozenAccountID, amount string,
) {
	t.Helper()
	var debitAccount, creditAccount, debitAmount, creditAmount string
	if err := pool.QueryRow(context.Background(), `
		SELECT
			MAX(account_id::TEXT) FILTER (WHERE side = 'D'),
			MAX(account_id::TEXT) FILTER (WHERE side = 'C'),
			MAX(amount::TEXT) FILTER (WHERE side = 'D'),
			MAX(amount::TEXT) FILTER (WHERE side = 'C')
		FROM journal_entries WHERE transaction_id = $1
	`, journalID).Scan(&debitAccount, &creditAccount, &debitAmount, &creditAmount); err != nil ||
		debitAccount != availableAccountID || creditAccount != frozenAccountID || debitAmount != amount || creditAmount != amount {
		t.Fatalf("冻结分录 debit=%s/%s credit=%s/%s error=%v", debitAccount, debitAmount, creditAccount, creditAmount, err)
	}
}

func payoutUUID(t *testing.T) string {
	t.Helper()
	value, err := identity.NewUUID()
	if err != nil {
		t.Fatalf("生成 UUID: %v", err)
	}
	return value
}
