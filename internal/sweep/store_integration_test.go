//go:build integration

package sweep

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/database"
	"github.com/eightyun/stablecoin-gateway/internal/identity"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestStoreCreatesImmutableSweepPlan(t *testing.T) {
	store, pool, policy := newSweepFixture(t, "allow", "100", time.Hour)
	plan, err := store.PlanNext(context.Background(), policy)
	if err != nil {
		t.Fatalf("PlanNext() error = %v", err)
	}
	if plan.AssetID != policy.AssetID || plan.Amount != "100" || plan.MinimumAmount != "50" ||
		plan.SourceWalletID == "" || plan.DestinationWalletID == "" || plan.SourceWalletID == plan.DestinationWalletID {
		t.Fatalf("PlanNext() = %+v", plan)
	}
	if _, err := store.PlanNext(context.Background(), policy); !errors.Is(err, ErrNoCandidate) {
		t.Fatalf("重复 PlanNext() error = %v", err)
	}
	if _, err := pool.Exec(context.Background(), `UPDATE sweep_plans SET amount = 99 WHERE id = $1`, plan.ID); err == nil ||
		!strings.Contains(err.Error(), "sweep plans are immutable") {
		t.Fatalf("修改不可变归集计划 error = %v", err)
	}
}

func TestStoreRejectsUnsafeProvenanceAndBalanceMismatch(t *testing.T) {
	for _, test := range []struct {
		name     string
		decision string
		balance  string
	}{
		{name: "筛查需复核", decision: "review", balance: "100"},
		{name: "余额与事件不一致", decision: "allow", balance: "101"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, _, policy := newSweepFixture(t, test.decision, test.balance, time.Hour)
			if _, err := store.PlanNext(context.Background(), policy); !errors.Is(err, ErrNoCandidate) {
				t.Fatalf("PlanNext() error = %v", err)
			}
		})
	}
}

func TestStoreRejectsExpiredScreeningEvidence(t *testing.T) {
	store, _, policy := newSweepFixture(t, "allow", "100", 20*time.Millisecond)
	time.Sleep(30 * time.Millisecond)
	if _, err := store.PlanNext(context.Background(), policy); !errors.Is(err, ErrNoCandidate) {
		t.Fatalf("PlanNext() error = %v", err)
	}
}

func TestStoreRejectsDisabledAsset(t *testing.T) {
	store, pool, policy := newSweepFixture(t, "allow", "100", time.Hour)
	mustSweepExec(t, pool, `UPDATE assets SET status = 'disabled' WHERE id = $1`, policy.AssetID)
	if _, err := store.PlanNext(context.Background(), policy); !errors.Is(err, ErrAssetUnavailable) {
		t.Fatalf("PlanNext() error = %v", err)
	}
}

func TestStorePlansConcurrentlyOnce(t *testing.T) {
	store, _, policy := newSweepFixture(t, "allow", "100", time.Hour)
	start := make(chan struct{})
	errorsChannel := make(chan error, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, err := store.PlanNext(context.Background(), policy)
			errorsChannel <- err
		}()
	}
	close(start)
	wait.Wait()
	close(errorsChannel)
	var succeeded, idle int
	for err := range errorsChannel {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrNoCandidate):
			idle++
		default:
			t.Fatalf("并发 PlanNext() error = %v", err)
		}
	}
	if succeeded != 1 || idle != 1 {
		t.Fatalf("并发结果 succeeded=%d idle=%d", succeeded, idle)
	}
}

func newSweepFixture(t *testing.T, decision, snapshotBalance string, screeningValidity time.Duration) (*Store, *pgxpool.Pool, Policy) {
	t.Helper()
	databaseURL := os.Getenv("GATEWAY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("未设置 GATEWAY_TEST_DATABASE_URL")
	}
	ctx := context.Background()
	pool, err := database.Open(ctx, database.DefaultConfig(databaseURL, "sweep-integration-test"))
	if err != nil {
		t.Fatalf("连接测试数据库: %v", err)
	}
	t.Cleanup(pool.Close)
	unique := sweepUUID(t)
	assetID := "sweep-" + unique
	network := "sweep-network-" + unique
	contract := "41" + strings.ReplaceAll(unique, "-", "") + strings.Repeat("0", 8)
	merchantID := sweepUUID(t)
	addressID := sweepUUID(t)
	intentID := sweepUUID(t)
	sourceAddress := "41" + strings.ReplaceAll(sweepUUID(t), "-", "") + strings.Repeat("0", 8)
	destinationAddress := "41" + strings.ReplaceAll(sweepUUID(t), "-", "") + strings.Repeat("0", 8)
	transactionID := "transaction-" + unique

	mustSweepExec(t, pool, `
		INSERT INTO assets (id, network, contract_address, symbol, decimals, status)
		VALUES ($1, $2, $3, 'USDT', 6, 'active')
	`, assetID, network, contract)
	mustSweepExec(t, pool, `
		INSERT INTO merchants (id, name, status) VALUES ($1, 'sweep merchant', 'active')
	`, merchantID)
	mustSweepExec(t, pool, `
		INSERT INTO ledger_accounts (id, owner_type, owner_id, asset_id, code, normal_side, status)
		VALUES ($1, 'platform', 'gateway', $2, 'custody', 'D', 'active')
	`, sweepUUID(t), assetID)
	mustSweepExec(t, pool, `
		INSERT INTO chain_scan_cursors (
			network, start_height, anchor_hash, next_height, previous_hash, tracked_contract
		) VALUES ($1, 1, 'anchor', 12, 'previous', $2)
	`, network, contract)
	mustSweepExec(t, pool, `
		INSERT INTO deposit_addresses (id, merchant_id, asset_id, address, status)
		VALUES ($1, $2, $3, $4, 'active')
	`, addressID, merchantID, assetID, destinationAddress)
	mustSweepExec(t, pool, `
		INSERT INTO deposit_intents (
			id, merchant_id, asset_id, deposit_address_id, idempotency_key,
			merchant_reference, request_hash, expected_amount, status, expires_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, 100, 'pending', CURRENT_TIMESTAMP + INTERVAL '1 hour')
	`, intentID, merchantID, assetID, addressID, "idem-"+unique, "ref-"+unique, strings.Repeat("a", 64))
	mustSweepExec(t, pool, `
		INSERT INTO chain_events (
			network, contract, transaction_id, log_index, block_height,
			block_hash, block_time, from_address, to_address, amount
		) VALUES ($1, $2, $3, 0, 10, 'block', CURRENT_TIMESTAMP, $4, $5, 100)
	`, network, contract, transactionID, sourceAddress, destinationAddress)
	mustSweepExec(t, pool, `
		INSERT INTO deposit_event_matches (
			network, contract, transaction_id, log_index, deposit_address_id,
			deposit_intent_id, status, amount
		) VALUES ($1, $2, $3, 0, $4, $5, 'matched', 100)
	`, network, contract, transactionID, addressID, intentID)
	jobID := sweepUUID(t)
	resultID := sweepUUID(t)
	mustSweepExec(t, pool, `
		INSERT INTO deposit_screening_jobs (
			id, network, contract, transaction_id, log_index, source_address,
			destination_address, status, lease_owner, lease_until, lease_epoch, attempts
		) VALUES ($1, $2, $3, $4, 0, $5, $6, 'processing',
		          'sweep-test', CURRENT_TIMESTAMP + INTERVAL '1 minute', 1, 1)
	`, jobID, network, contract, transactionID, sourceAddress, destinationAddress)
	mustSweepExec(t, pool, `
		INSERT INTO deposit_screening_results (
			id, job_id, attempt, provider, decision, reason_codes,
			provider_reference, response_hash, checked_at, valid_until
		) VALUES ($1, $2, 1, 'sweep-test', $3, '[]'::jsonb,
		          $4, $5, CURRENT_TIMESTAMP,
		          CURRENT_TIMESTAMP + ($6 * INTERVAL '1 millisecond'))
	`, resultID, jobID, decision, "screening-"+unique, strings.Repeat("b", 64), screeningValidity.Milliseconds())
	mustSweepExec(t, pool, `
		UPDATE deposit_screening_jobs
		SET status = 'completed', current_result_id = $2,
		    lease_owner = NULL, lease_until = NULL, updated_at = clock_timestamp()
		WHERE id = $1
	`, jobID, resultID)
	mustSweepExec(t, pool, `
		UPDATE deposit_screening_jobs
		SET status = 'closed', updated_at = clock_timestamp()
		WHERE id = $1
	`, jobID)

	hotWalletID := sweepUUID(t)
	hotAddress := "41" + strings.ReplaceAll(sweepUUID(t), "-", "") + strings.Repeat("0", 8)
	mustSweepExec(t, pool, `
		WITH inserted_wallet AS (
			INSERT INTO custody_wallets (id, asset_id, address, role, status)
			VALUES ($1, $2, $3, 'hot', 'active')
			RETURNING id
		)
		INSERT INTO custody_wallet_registration_audits (id, wallet_id, actor, reason)
		SELECT $4, id, 'integration-test', 'sweep destination'
		FROM inserted_wallet
	`, hotWalletID, assetID, hotAddress, sweepUUID(t))
	var custodyAccountID string
	if err := pool.QueryRow(ctx, `
		SELECT id::TEXT FROM ledger_accounts
		WHERE asset_id = $1 AND owner_type = 'platform' AND owner_id = 'gateway' AND code = 'custody'
	`, assetID).Scan(&custodyAccountID); err != nil {
		t.Fatalf("查询托管科目: %v", err)
	}
	runID := sweepUUID(t)
	mustSweepExec(t, pool, `
		INSERT INTO wallet_balance_snapshot_runs (
			id, asset_id, block_height, block_hash, block_time, wallet_count,
			total_balance, ledger_account_id, ledger_entry_count, ledger_balance
		) VALUES ($1, $2, 10, $3, CURRENT_TIMESTAMP, 2, $4, $5, 0, 0)
	`, runID, assetID, strings.Repeat("c", 64), snapshotBalance, custodyAccountID)
	mustSweepExec(t, pool, `
		INSERT INTO wallet_balance_snapshots (run_id, wallet_id, asset_id, balance)
		VALUES ($1, $2, $3, $4), ($1, $5, $3, 0)
	`, runID, addressID, assetID, snapshotBalance, hotWalletID)
	store, err := NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	return store, pool, Policy{AssetID: assetID, MinimumAmount: "50", MaxSnapshotAge: time.Minute}
}

func mustSweepExec(t *testing.T, pool *pgxpool.Pool, query string, arguments ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), query, arguments...); err != nil {
		t.Fatalf("准备归集测试数据: %v", err)
	}
}

func sweepUUID(t *testing.T) string {
	t.Helper()
	value, err := identity.NewUUID()
	if err != nil {
		t.Fatal(err)
	}
	return value
}
