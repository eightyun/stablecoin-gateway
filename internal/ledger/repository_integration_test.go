//go:build integration

package ledger

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/eightyun/stablecoin-gateway/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const testAssetID = "usdt-tron"

type testLedgerFixture struct {
	pool            *pgxpool.Pool
	repository      *PostgreSQLRepository
	debitAccountID  string
	creditAccountID string
}

func TestPostgreSQLRepositoryPost(t *testing.T) {
	fixture := newTestLedgerFixture(t)
	transaction := fixture.balancedTransaction(t, 1_000_000)

	created, err := fixture.repository.Post(context.Background(), transaction)
	if err != nil {
		t.Fatalf("Post() 创建交易 error = %v", err)
	}
	if !created.Created || created.TransactionID != transaction.ID {
		t.Fatalf("Post() 创建结果 = %+v", created)
	}

	var status string
	var entryCount int
	err = fixture.pool.QueryRow(context.Background(), `
		SELECT journal.status, COUNT(entry.transaction_id)
		FROM journal_transactions AS journal
		JOIN journal_entries AS entry ON entry.transaction_id = journal.id
		WHERE journal.id = $1
		GROUP BY journal.status
	`, transaction.ID).Scan(&status, &entryCount)
	if err != nil {
		t.Fatalf("查询过账结果: %v", err)
	}
	if status != "posted" || entryCount != 2 {
		t.Fatalf("过账状态 = %s, 分录数 = %d", status, entryCount)
	}

	retry := transaction
	retry.ID = randomUUID(t)
	reused, err := fixture.repository.Post(context.Background(), retry)
	if err != nil {
		t.Fatalf("Post() 幂等重试 error = %v", err)
	}
	if reused.Created || reused.TransactionID != transaction.ID {
		t.Fatalf("Post() 幂等结果 = %+v", reused)
	}

	referenceRetry := transaction
	referenceRetry.ID = randomUUID(t)
	referenceRetry.IdempotencyKey = "deposit:" + randomUUID(t)
	reusedByReference, err := fixture.repository.Post(context.Background(), referenceRetry)
	if err != nil {
		t.Fatalf("Post() 业务引用重试 error = %v", err)
	}
	if reusedByReference.Created || reusedByReference.TransactionID != transaction.ID {
		t.Fatalf("Post() 业务引用幂等结果 = %+v", reusedByReference)
	}

	conflict := referenceRetry
	conflict.ID = randomUUID(t)
	conflict.IdempotencyKey = "deposit:" + randomUUID(t)
	conflict.Entries = append([]Entry(nil), retry.Entries...)
	conflict.Entries[0].Amount++
	conflict.Entries[1].Amount++
	_, err = fixture.repository.Post(context.Background(), conflict)
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("Post() 冲突 error = %v, 期望 %v", err, ErrIdempotencyConflict)
	}
}

func TestPostgreSQLRepositoryConcurrentIdempotency(t *testing.T) {
	fixture := newTestLedgerFixture(t)
	base := fixture.balancedTransaction(t, 2_000_000)

	const workers = 8
	results := make(chan PostResult, workers)
	errorsChannel := make(chan error, workers)
	transactionIDs := make([]string, workers)
	for index := range workers {
		transactionIDs[index] = randomUUID(t)
	}
	var waitGroup sync.WaitGroup
	for index := range workers {
		waitGroup.Add(1)
		go func(transactionID string) {
			defer waitGroup.Done()
			transaction := base
			transaction.ID = transactionID
			result, err := fixture.repository.Post(context.Background(), transaction)
			if err != nil {
				errorsChannel <- err
				return
			}
			results <- result
		}(transactionIDs[index])
	}
	waitGroup.Wait()
	close(results)
	close(errorsChannel)

	for err := range errorsChannel {
		t.Errorf("并发 Post() error = %v", err)
	}

	createdCount := 0
	var transactionID string
	for result := range results {
		if result.Created {
			createdCount++
		}
		if transactionID == "" {
			transactionID = result.TransactionID
		}
		if result.TransactionID != transactionID {
			t.Errorf("并发幂等返回了不同交易 ID: %s 和 %s", transactionID, result.TransactionID)
		}
	}
	if createdCount != 1 {
		t.Fatalf("并发创建次数 = %d, 期望 1", createdCount)
	}
}

func TestPostgreSQLLedgerDatabaseGuards(t *testing.T) {
	fixture := newTestLedgerFixture(t)

	t.Run("拒绝提交草稿交易", func(t *testing.T) {
		ctx := context.Background()
		databaseTransaction, err := fixture.pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			t.Fatalf("开始事务: %v", err)
		}
		defer databaseTransaction.Rollback(ctx)

		transactionID := randomUUID(t)
		_, err = databaseTransaction.Exec(ctx, `
			INSERT INTO journal_transactions (
				id, requester_type, requester_id, idempotency_key,
				reference_type, reference_id, request_hash, status
			) VALUES ($1, 'system', 'integration-test', $2, 'draft-test', $3, $4, 'draft')
		`, transactionID, randomUUID(t), randomUUID(t), hashForTest(t, transactionID))
		if err != nil {
			t.Fatalf("创建草稿交易: %v", err)
		}
		if err = databaseTransaction.Commit(ctx); err == nil {
			t.Fatal("草稿交易提交成功，数据库约束未生效")
		}
	})

	t.Run("拒绝不平衡交易", func(t *testing.T) {
		ctx := context.Background()
		databaseTransaction, err := fixture.pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			t.Fatalf("开始事务: %v", err)
		}
		defer databaseTransaction.Rollback(ctx)

		transactionID := randomUUID(t)
		_, err = databaseTransaction.Exec(ctx, `
			INSERT INTO journal_transactions (
				id, requester_type, requester_id, idempotency_key,
				reference_type, reference_id, request_hash, status
			) VALUES ($1, 'system', 'integration-test', $2, 'guard-test', $3, $4, 'draft')
		`, transactionID, randomUUID(t), randomUUID(t), hashForTest(t, transactionID))
		if err != nil {
			t.Fatalf("创建草稿交易: %v", err)
		}

		_, err = databaseTransaction.Exec(ctx, `
			INSERT INTO journal_entries (transaction_id, line_no, account_id, asset_id, side, amount)
			VALUES ($1, 1, $2, $4, 'D', 10), ($1, 2, $3, $4, 'C', 9)
		`, transactionID, fixture.debitAccountID, fixture.creditAccountID, testAssetID)
		if err != nil {
			t.Fatalf("写入不平衡分录: %v", err)
		}

		_, err = databaseTransaction.Exec(ctx, `
			UPDATE journal_transactions
			SET status = 'posted', posted_at = CURRENT_TIMESTAMP
			WHERE id = $1
		`, transactionID)
		if err != nil {
			t.Fatalf("标记过账: %v", err)
		}
		if err = databaseTransaction.Commit(ctx); err == nil {
			t.Fatal("不平衡交易提交成功，数据库约束未生效")
		}
	})

	t.Run("已过账分录不可修改", func(t *testing.T) {
		transaction := fixture.balancedTransaction(t, 3_000_000)
		if _, err := fixture.repository.Post(context.Background(), transaction); err != nil {
			t.Fatalf("创建已过账交易: %v", err)
		}

		_, err := fixture.pool.Exec(context.Background(), `
			UPDATE journal_entries
			SET amount = amount + 1
			WHERE transaction_id = $1 AND line_no = 1
		`, transaction.ID)
		if err == nil {
			t.Fatal("已过账分录修改成功，数据库约束未生效")
		}

		_, err = fixture.pool.Exec(context.Background(), `
			DELETE FROM journal_transactions WHERE id = $1
		`, transaction.ID)
		if err == nil {
			t.Fatal("已过账交易删除成功，数据库约束未生效")
		}
	})
}

func newTestLedgerFixture(t *testing.T) testLedgerFixture {
	t.Helper()
	databaseURL := os.Getenv("GATEWAY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("未设置 GATEWAY_TEST_DATABASE_URL")
	}

	pool, err := database.Open(context.Background(), database.DefaultConfig(databaseURL, "ledger-integration-test"))
	if err != nil {
		t.Fatalf("创建测试数据库连接池: %v", err)
	}
	t.Cleanup(pool.Close)

	debitAccountID := randomUUID(t)
	creditAccountID := randomUUID(t)
	_, err = pool.Exec(context.Background(), `
		INSERT INTO assets (id, network, contract_address, symbol, decimals, status)
		VALUES ($1, 'tron', 'integration-test-usdt', 'USDT', 6, 'active')
		ON CONFLICT (id) DO NOTHING
	`, testAssetID)
	if err != nil {
		t.Fatalf("准备账本测试资产: %v", err)
	}

	_, err = pool.Exec(context.Background(), `
		INSERT INTO ledger_accounts (id, owner_type, owner_id, asset_id, code, normal_side, status)
		VALUES
			($2, 'platform', $4, $1, 'custody', 'D', 'active'),
			($3, 'merchant', $5, $1, 'available', 'C', 'active')
	`, testAssetID, debitAccountID, creditAccountID, "platform:"+debitAccountID, "merchant:"+creditAccountID)
	if err != nil {
		t.Fatalf("准备账本测试账户: %v", err)
	}

	repository, err := NewPostgreSQLRepository(pool)
	if err != nil {
		t.Fatalf("创建账本仓储: %v", err)
	}
	return testLedgerFixture{
		pool:            pool,
		repository:      repository,
		debitAccountID:  debitAccountID,
		creditAccountID: creditAccountID,
	}
}

func (fixture testLedgerFixture) balancedTransaction(t *testing.T, amount int64) Transaction {
	t.Helper()
	requestID := randomUUID(t)
	return Transaction{
		ID:             randomUUID(t),
		RequesterType:  "merchant",
		RequesterID:    randomUUID(t),
		IdempotencyKey: "deposit:" + requestID,
		ReferenceType:  "deposit",
		ReferenceID:    requestID,
		Entries: []Entry{
			{AccountID: fixture.debitAccountID, AssetID: testAssetID, Side: Debit, Amount: amount},
			{AccountID: fixture.creditAccountID, AssetID: testAssetID, Side: Credit, Amount: amount},
		},
	}
}

func randomUUID(t *testing.T) string {
	t.Helper()
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		t.Fatalf("生成随机 UUID: %v", err)
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		value[0:4], value[4:6], value[6:8], value[8:10], value[10:16])
}

func hashForTest(t *testing.T, value string) string {
	t.Helper()
	if len(value) > 64 {
		t.Fatalf("测试哈希内容过长: %d", len(value))
	}
	return fmt.Sprintf("%-64s", value)
}
