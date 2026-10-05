//go:build integration

package sweep

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/chain/tron"
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

func TestStoreHonorsAcceptanceBounds(t *testing.T) {
	store, pool, policy := newSweepFixture(t, "allow", "100", time.Hour)
	var sourceAddress, destinationAddress string
	if err := pool.QueryRow(context.Background(), `
		SELECT source.address, destination.address
		FROM custody_wallets AS source
		CROSS JOIN custody_wallets AS destination
		WHERE source.asset_id = $1 AND source.role = 'deposit'
		  AND destination.asset_id = $1 AND destination.role = 'hot'
	`, policy.AssetID).Scan(&sourceAddress, &destinationAddress); err != nil {
		t.Fatalf("查询归集验收钱包: %v", err)
	}

	bounded := policy
	bounded.MaximumAmount = "99"
	bounded.SourceAddress = sourceAddress
	bounded.DestinationAddress = destinationAddress
	if _, err := store.PlanNext(context.Background(), bounded); !errors.Is(err, ErrNoCandidate) {
		t.Fatalf("超过验收上限 PlanNext() error = %v", err)
	}
	bounded.MaximumAmount = "100"
	bounded.SourceAddress = "41" + strings.Repeat("f", 40)
	if _, err := store.PlanNext(context.Background(), bounded); !errors.Is(err, ErrNoCandidate) {
		t.Fatalf("来源地址不匹配 PlanNext() error = %v", err)
	}
	bounded.SourceAddress = sourceAddress
	bounded.DestinationAddress = "41" + strings.Repeat("e", 40)
	if _, err := store.PlanNext(context.Background(), bounded); !errors.Is(err, ErrNoCandidate) {
		t.Fatalf("目标地址不匹配 PlanNext() error = %v", err)
	}
	bounded.DestinationAddress = destinationAddress
	plan, err := store.PlanNext(context.Background(), bounded)
	if err != nil || plan.SourceAddress != sourceAddress || plan.DestinationAddress != destinationAddress {
		t.Fatalf("受限 PlanNext() = %+v, %v", plan, err)
	}
}

func TestStoreLeasesAndCompletesSweepSigning(t *testing.T) {
	store, pool, policy := newSweepFixture(t, "allow", "100", time.Hour)
	plan, err := store.PlanNext(context.Background(), policy)
	if err != nil {
		t.Fatalf("PlanNext() error = %v", err)
	}
	first, err := store.ClaimSigning(context.Background(), "signer-1", "sweep-network-"+strings.TrimPrefix(policy.AssetID, "sweep-"), 20*time.Millisecond)
	if err != nil || first.PlanID != plan.ID || first.LeaseEpoch != 1 || first.Amount != "100" {
		t.Fatalf("第一次 ClaimSigning() = %+v, %v", first, err)
	}
	if _, err := store.ClaimSigning(context.Background(), "signer-2", first.Network, time.Minute); !errors.Is(err, ErrNoSigningJob) {
		t.Fatalf("租约占用期间 ClaimSigning() error = %v", err)
	}
	time.Sleep(30 * time.Millisecond)
	second, err := store.ClaimSigning(context.Background(), "signer-2", first.Network, time.Minute)
	if err != nil || second.PlanID != plan.ID || second.LeaseEpoch != 2 {
		t.Fatalf("接管 ClaimSigning() = %+v, %v", second, err)
	}
	transaction := sweepSignedTransaction(t)
	observation := BalanceObservation{Balance: "100", BlockHeight: 12, BlockHash: strings.Repeat("d", 64)}
	if err := store.CompleteSigning(context.Background(), first, observation, transaction); !errors.Is(err, ErrSigningLeaseLost) {
		t.Fatalf("旧租约 CompleteSigning() error = %v", err)
	}
	if err := store.ReleaseSigning(context.Background(), second, "retry_test"); err != nil {
		t.Fatalf("ReleaseSigning() error = %v", err)
	}
	third, err := store.ClaimSigning(context.Background(), "signer-3", first.Network, time.Minute)
	if err != nil || third.LeaseEpoch != 3 {
		t.Fatalf("重试 ClaimSigning() = %+v, %v", third, err)
	}
	if err := store.CompleteSigning(context.Background(), third, observation, transaction); err != nil {
		t.Fatalf("CompleteSigning() error = %v", err)
	}
	var status, transactionID string
	if err := pool.QueryRow(context.Background(), `
		SELECT status, transaction_id FROM sweep_executions WHERE plan_id = $1
	`, plan.ID).Scan(&status, &transactionID); err != nil || status != "ready_for_broadcast" || transactionID != transaction.ID {
		t.Fatalf("执行状态 status=%s transaction=%s error=%v", status, transactionID, err)
	}
	if _, err := pool.Exec(context.Background(), `
		UPDATE sweep_executions SET transaction_id = $2 WHERE plan_id = $1
	`, plan.ID, strings.Repeat("e", 64)); err == nil || !strings.Contains(err.Error(), "signed sweep transaction is immutable") {
		t.Fatalf("修改签名交易 error=%v", err)
	}
}

func TestStoreBroadcastsAndFinalizesSweep(t *testing.T) {
	store, pool, policy := newSweepFixture(t, "allow", "100", time.Hour)
	plan, err := store.PlanNext(context.Background(), policy)
	if err != nil {
		t.Fatalf("PlanNext() error = %v", err)
	}
	network := "sweep-network-" + strings.TrimPrefix(policy.AssetID, "sweep-")
	signing, err := store.ClaimSigning(context.Background(), "signer-1", network, time.Minute)
	if err != nil {
		t.Fatalf("ClaimSigning() error = %v", err)
	}
	transaction := sweepSignedTransaction(t)
	observation := BalanceObservation{Balance: "100", BlockHeight: 12, BlockHash: strings.Repeat("d", 64)}
	if err := store.CompleteSigning(context.Background(), signing, observation, transaction); err != nil {
		t.Fatalf("CompleteSigning() error = %v", err)
	}
	if _, err := store.ClaimBroadcast(context.Background(), "executor-wrong", "other-network", time.Minute); !errors.Is(err, ErrNoBroadcastJob) {
		t.Fatalf("错误网络 ClaimBroadcast() error = %v", err)
	}
	broadcast, err := store.ClaimBroadcast(context.Background(), "executor-1", network, time.Minute)
	if err != nil || broadcast.PlanID != plan.ID || broadcast.Transaction.ID != transaction.ID {
		t.Fatalf("ClaimBroadcast() = %+v, %v", broadcast, err)
	}
	if err := store.BeginConfirmation(context.Background(), broadcast, "unknown", "broadcast_error"); err != nil {
		t.Fatalf("BeginConfirmation() error = %v", err)
	}
	confirmation, err := store.ClaimConfirmation(context.Background(), "executor-2", network, time.Minute)
	if err != nil || confirmation.PlanID != plan.ID || confirmation.Transaction.ID != transaction.ID {
		t.Fatalf("ClaimConfirmation() = %+v, %v", confirmation, err)
	}
	if err := store.CompleteConfirmationSuccess(context.Background(), confirmation); err != nil {
		t.Fatalf("CompleteConfirmationSuccess() error = %v", err)
	}
	var status, result string
	var confirmedAt time.Time
	if err := pool.QueryRow(context.Background(), `
		SELECT status, broadcast_result, confirmed_at
		FROM sweep_executions WHERE plan_id = $1
	`, plan.ID).Scan(&status, &result, &confirmedAt); err != nil ||
		status != "succeeded" || result != "unknown" || confirmedAt.IsZero() {
		t.Fatalf("执行终态 status=%s result=%s confirmed_at=%v error=%v", status, result, confirmedAt, err)
	}
	state, err := store.GetExecution(context.Background(), plan.ID)
	if err != nil || state.Status != "succeeded" || state.TransactionID != transaction.ID || state.Network != network {
		t.Fatalf("GetExecution() = %+v, %v", state, err)
	}
	if _, err := store.GetExecution(context.Background(), "invalid"); !errors.Is(err, ErrExecutionNotFound) {
		t.Fatalf("无效 GetExecution() error = %v", err)
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

func sweepSignedTransaction(t *testing.T) tron.SignedTransaction {
	t.Helper()
	rawData := []byte("sweep-signing-integration-" + sweepUUID(t))
	digest := sha256.Sum256(rawData)
	transactionID := hex.EncodeToString(digest[:])
	now := time.Now().UTC()
	payload, err := json.Marshal(map[string]any{
		"txID": transactionID,
		"raw_data": map[string]any{
			"contract":  []any{map[string]any{"type": "TriggerSmartContract"}},
			"timestamp": now.UnixMilli(), "expiration": now.Add(time.Minute).UnixMilli(),
		},
		"raw_data_hex": hex.EncodeToString(rawData),
		"signature":    []string{strings.Repeat("a", 130)},
	})
	if err != nil {
		t.Fatal(err)
	}
	return tron.SignedTransaction{ID: transactionID, Payload: payload}
}
