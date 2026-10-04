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
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestInboundStoreDiscoversLeasesAndRescreensExpiredResult(t *testing.T) {
	databaseURL := os.Getenv("GATEWAY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("未设置 GATEWAY_TEST_DATABASE_URL")
	}
	ctx := context.Background()
	pool, err := database.Open(ctx, database.DefaultConfig(databaseURL, "inbound-screening-integration-test"))
	if err != nil {
		t.Fatalf("连接测试数据库: %v", err)
	}
	t.Cleanup(pool.Close)
	uniqueID, err := identity.NewUUID()
	if err != nil {
		t.Fatal(err)
	}
	network := "inbound-" + uniqueID
	contract := "41" + strings.Repeat("1", 40)
	destination := "41" + strings.Repeat("2", 40)
	source := "41" + strings.Repeat("3", 40)
	assetID := "inbound-asset-" + uniqueID
	merchantID := mustScreeningUUID(t)
	addressID := mustScreeningUUID(t)
	transactionID := "tx-" + uniqueID
	mustInboundScreeningExec(t, pool, `
		INSERT INTO assets (id, network, contract_address, symbol, decimals, status)
		VALUES ($1, $2, $3, 'USDT', 6, 'active')
	`, assetID, network, contract)
	mustInboundScreeningExec(t, pool, `
		INSERT INTO merchants (id, name, status) VALUES ($1, 'inbound screening merchant', 'active')
	`, merchantID)
	mustInboundScreeningExec(t, pool, `
		INSERT INTO chain_scan_cursors (
			network, start_height, anchor_hash, next_height, previous_hash, tracked_contract
		) VALUES ($1, 1, 'anchor', 2, 'previous', $2)
	`, network, contract)
	mustInboundScreeningExec(t, pool, `
		INSERT INTO deposit_addresses (id, merchant_id, asset_id, address, status)
		VALUES ($1, $2, $3, $4, 'active')
	`, addressID, merchantID, assetID, destination)
	mustInboundScreeningExec(t, pool, `
		INSERT INTO chain_events (
			network, contract, transaction_id, log_index, block_height,
			block_hash, block_time, from_address, to_address, amount
		) VALUES ($1, $2, $3, 0, 1, 'block', CURRENT_TIMESTAMP, $4, $5, 99)
	`, network, contract, transactionID, source, destination)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `
			UPDATE deposit_screening_jobs
			SET status = 'closed', lease_owner = NULL, lease_until = NULL,
			    updated_at = clock_timestamp()
			WHERE network = $1 AND contract = $2 AND transaction_id = $3 AND log_index = 0
			  AND status <> 'closed'
		`, network, contract, transactionID)
		_, _ = pool.Exec(context.Background(), `
			INSERT INTO deposit_event_matches (
				network, contract, transaction_id, log_index, deposit_address_id,
				deposit_intent_id, status, reason, amount
			) VALUES ($1, $2, $3, 0, $4, NULL, 'review', 'no_intent', 99)
			ON CONFLICT DO NOTHING
		`, network, contract, transactionID, addressID)
	})
	store, err := NewInboundStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.Claim(ctx, "inbound-1", 20*time.Millisecond)
	if err != nil || first.LeaseEpoch != 1 || first.Request.Direction != DirectionInbound ||
		first.Request.SourceAddress != source || first.Request.DestinationAddress != destination ||
		first.Request.Amount != "99" || first.Request.AssetID != assetID {
		t.Fatalf("第一次 Claim() = %+v, %v", first, err)
	}
	time.Sleep(30 * time.Millisecond)
	second, err := store.Claim(ctx, "inbound-2", time.Minute)
	if err != nil || second.JobID != first.JobID || second.LeaseEpoch != 2 {
		t.Fatalf("租约接管 Claim() = %+v, %v", second, err)
	}
	now := time.Now().UTC()
	result := Result{
		Provider: "integration-provider", Decision: DecisionReview,
		ReasonCodes: []string{"risk_review"}, ProviderReference: "inbound-reference",
		ResponseHash: strings.Repeat("d", 64), CheckedAt: now, ValidUntil: now.Add(time.Second),
	}
	if err := store.Complete(ctx, first, result); !errors.Is(err, ErrInboundScreeningLeaseLost) {
		t.Fatalf("旧租约 Complete() error = %v", err)
	}
	if err := store.Complete(ctx, second, result); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if _, err := store.Claim(ctx, "inbound-3", time.Minute); !errors.Is(err, ErrNoInboundScreeningJob) {
		t.Fatalf("有效结果后 Claim() error = %v", err)
	}
	actions, err := store.ListInboundActionRequired(ctx, 100)
	if err != nil || !containsInboundScreeningAction(actions, first.JobID, DecisionReview) {
		t.Fatalf("ListInboundActionRequired() = %+v, %v", actions, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE deposit_screening_results SET decision = 'allow' WHERE job_id = $1`, first.JobID); err == nil ||
		!strings.Contains(err.Error(), "deposit screening results are immutable") {
		t.Fatalf("修改入金筛查结果 error = %v", err)
	}
	time.Sleep(time.Until(result.ValidUntil) + 50*time.Millisecond)
	third, err := store.Claim(ctx, "inbound-3", time.Minute)
	if err != nil || third.JobID != first.JobID || third.LeaseEpoch != 3 {
		t.Fatalf("结果过期后 Claim() = %+v, %v", third, err)
	}
	if err := store.Release(ctx, third, "test_release"); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
}

func mustInboundScreeningExec(t *testing.T, pool *pgxpool.Pool, query string, arguments ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), query, arguments...); err != nil {
		t.Fatalf("准备入金筛查数据: %v", err)
	}
}

func containsInboundScreeningAction(actions []InboundActionRequired, jobID, decision string) bool {
	for _, action := range actions {
		if action.JobID == jobID && action.Decision == decision {
			return true
		}
	}
	return false
}
