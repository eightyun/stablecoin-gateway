// Package payout 管理商户出款申请及其账本冻结。
package payout

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/chain/tron"
	"github.com/eightyun/stablecoin-gateway/internal/identity"
	"github.com/eightyun/stablecoin-gateway/internal/ledger"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	availableAccountCode = "available"
	frozenAccountCode    = "frozen"
)

var (
	ErrDatabaseRequired    = errors.New("数据库连接不能为空")
	ErrInvalidRequest      = errors.New("出款请求无效")
	ErrPayoutConflict      = errors.New("出款幂等键或业务引用冲突")
	ErrPayoutNotFound      = errors.New("出款单不存在")
	ErrMerchantUnavailable = errors.New("商户不存在或不可用")
	ErrAssetUnavailable    = errors.New("出款资产不存在或不可用")
	ErrUnsupportedNetwork  = errors.New("暂不支持该出款网络")
	ErrLedgerUnavailable   = errors.New("出款账本科目不存在或不可用")
	ErrInsufficientBalance = errors.New("可用余额不足")
)

// Request 是创建出款单所需的数据。金额使用资产最小单位。
type Request struct {
	ID                 string
	MerchantID         string
	AssetID            string
	IdempotencyKey     string
	MerchantReference  string
	DestinationAddress string
	Amount             string
}

// Details 是商户可查询的出款信息。
type Details struct {
	ID                  string    `json:"id"`
	MerchantReference   string    `json:"merchant_reference"`
	AssetID             string    `json:"asset_id"`
	Network             string    `json:"network"`
	ContractAddress     string    `json:"contract_address"`
	Symbol              string    `json:"symbol"`
	Decimals            int16     `json:"decimals"`
	DestinationAddress  string    `json:"destination_address"`
	Amount              string    `json:"amount"`
	Status              string    `json:"status"`
	FreezeTransactionID string    `json:"freeze_transaction_id"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// CreateResult 表示幂等创建结果。
type CreateResult struct {
	Payout  Details
	Created bool
}

// Store 使用 PostgreSQL 保存出款单并完成原子冻资。
type Store struct {
	db *pgxpool.Pool
}

// NewStore 创建出款 Store。
func NewStore(db *pgxpool.Pool) (*Store, error) {
	if db == nil {
		return nil, ErrDatabaseRequired
	}
	return &Store{db: db}, nil
}

// Create 原子创建出款单并将商户可用余额转入冻结余额。
func (store *Store) Create(ctx context.Context, request Request) (result CreateResult, err error) {
	request = normalizeRequest(request)
	amount, err := validateRequest(request)
	if err != nil {
		return CreateResult{}, err
	}
	requestHash, err := requestFingerprint(request)
	if err != nil {
		return CreateResult{}, err
	}
	databaseTransaction, err := store.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return CreateResult{}, fmt.Errorf("开始创建出款事务: %w", err)
	}
	defer func() {
		if rollbackErr := databaseTransaction.Rollback(context.Background()); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			err = errors.Join(err, fmt.Errorf("回滚创建出款事务: %w", rollbackErr))
		}
	}()

	var merchantStatus string
	err = databaseTransaction.QueryRow(ctx, `
		SELECT status FROM merchants WHERE id = $1 FOR UPDATE
	`, request.MerchantID).Scan(&merchantStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return CreateResult{}, ErrMerchantUnavailable
	}
	if err != nil {
		return CreateResult{}, fmt.Errorf("锁定出款商户: %w", err)
	}
	if existing, found, findErr := findExisting(ctx, databaseTransaction, request, requestHash); findErr != nil || found {
		if findErr != nil {
			return CreateResult{}, findErr
		}
		if err = databaseTransaction.Commit(ctx); err != nil {
			return CreateResult{}, fmt.Errorf("提交出款幂等查询: %w", err)
		}
		return CreateResult{Payout: existing, Created: false}, nil
	}
	if merchantStatus != "active" {
		return CreateResult{}, ErrMerchantUnavailable
	}

	var details Details
	err = databaseTransaction.QueryRow(ctx, `
		SELECT id, network, contract_address, symbol, decimals
		FROM assets
		WHERE id = $1 AND status = 'active'
		FOR SHARE
	`, request.AssetID).Scan(
		&details.AssetID, &details.Network, &details.ContractAddress, &details.Symbol, &details.Decimals,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return CreateResult{}, ErrAssetUnavailable
	}
	if err != nil {
		return CreateResult{}, fmt.Errorf("查询出款资产: %w", err)
	}
	if !strings.HasPrefix(strings.ToLower(details.Network), "tron") {
		return CreateResult{}, ErrUnsupportedNetwork
	}
	if _, err := tron.NormalizeAddress(request.DestinationAddress); err != nil {
		return CreateResult{}, ErrInvalidRequest
	}

	var availableAccountID, frozenAccountID string
	err = databaseTransaction.QueryRow(ctx, `
		SELECT available.id::TEXT, frozen.id::TEXT
		FROM ledger_accounts AS available
		JOIN ledger_accounts AS frozen
		  ON frozen.owner_type = available.owner_type
		 AND frozen.owner_id = available.owner_id
		 AND frozen.asset_id = available.asset_id
		WHERE available.owner_type = 'merchant'
		  AND available.owner_id = $1
		  AND available.asset_id = $2
		  AND available.code = $3
		  AND available.normal_side = 'C'
		  AND available.status = 'active'
		  AND frozen.code = $4
		  AND frozen.normal_side = 'C'
		  AND frozen.status = 'active'
		FOR UPDATE OF available, frozen
	`, request.MerchantID, request.AssetID, availableAccountCode, frozenAccountCode).Scan(
		&availableAccountID, &frozenAccountID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return CreateResult{}, ErrLedgerUnavailable
	}
	if err != nil {
		return CreateResult{}, fmt.Errorf("锁定出款账本科目: %w", err)
	}
	availableBalance, err := accountBalance(ctx, databaseTransaction, availableAccountID)
	if err != nil {
		return CreateResult{}, err
	}
	if availableBalance.Cmp(big.NewInt(amount)) < 0 {
		return CreateResult{}, ErrInsufficientBalance
	}

	journalID, err := identity.NewUUID()
	if err != nil {
		return CreateResult{}, err
	}
	postResult, err := ledger.PostInTransaction(ctx, databaseTransaction, ledger.Transaction{
		ID: journalID, RequesterType: "merchant", RequesterID: request.MerchantID,
		IdempotencyKey: "payout-freeze:" + request.IdempotencyKey,
		ReferenceType:  "payout_freeze", ReferenceID: request.ID,
		Entries: []ledger.Entry{
			{AccountID: availableAccountID, AssetID: request.AssetID, Side: ledger.Debit, Amount: amount},
			{AccountID: frozenAccountID, AssetID: request.AssetID, Side: ledger.Credit, Amount: amount},
		},
	})
	if err != nil {
		return CreateResult{}, fmt.Errorf("冻结出款余额: %w", err)
	}
	err = databaseTransaction.QueryRow(ctx, `
		INSERT INTO payouts (
			id, merchant_id, asset_id, idempotency_key, merchant_reference,
			request_hash, destination_address, amount, status, freeze_transaction_id
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING id::TEXT, merchant_reference, destination_address, amount::TEXT,
		          status, freeze_transaction_id::TEXT, created_at, updated_at
	`, request.ID, request.MerchantID, request.AssetID, request.IdempotencyKey,
		request.MerchantReference, requestHash, request.DestinationAddress, request.Amount,
		StatusPendingReview, postResult.TransactionID).Scan(
		&details.ID, &details.MerchantReference, &details.DestinationAddress, &details.Amount,
		&details.Status, &details.FreezeTransactionID, &details.CreatedAt, &details.UpdatedAt,
	)
	if err != nil {
		return CreateResult{}, fmt.Errorf("创建出款单: %w", err)
	}
	if err = databaseTransaction.Commit(ctx); err != nil {
		return CreateResult{}, fmt.Errorf("提交创建出款事务: %w", err)
	}
	return CreateResult{Payout: details, Created: true}, nil
}

// Get 返回指定商户的出款单。
func (store *Store) Get(ctx context.Context, merchantID, payoutID string) (Details, error) {
	details, err := queryDetails(ctx, store.db, merchantID, payoutID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Details{}, ErrPayoutNotFound
	}
	if err != nil {
		return Details{}, fmt.Errorf("查询出款单: %w", err)
	}
	return details, nil
}

type rowQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func queryDetails(ctx context.Context, querier rowQuerier, merchantID, payoutID string) (Details, error) {
	var details Details
	err := querier.QueryRow(ctx, `
		SELECT payout.id::TEXT, payout.merchant_reference, payout.asset_id,
		       asset.network, asset.contract_address, asset.symbol, asset.decimals,
		       payout.destination_address, payout.amount::TEXT, payout.status,
		       payout.freeze_transaction_id::TEXT, payout.created_at, payout.updated_at
		FROM payouts AS payout
		JOIN assets AS asset ON asset.id = payout.asset_id
		WHERE payout.merchant_id = $1 AND payout.id = $2
	`, merchantID, payoutID).Scan(
		&details.ID, &details.MerchantReference, &details.AssetID,
		&details.Network, &details.ContractAddress, &details.Symbol, &details.Decimals,
		&details.DestinationAddress, &details.Amount, &details.Status,
		&details.FreezeTransactionID, &details.CreatedAt, &details.UpdatedAt,
	)
	return details, err
}

func findExisting(
	ctx context.Context,
	databaseTransaction pgx.Tx,
	request Request,
	requestHash string,
) (Details, bool, error) {
	rows, err := databaseTransaction.Query(ctx, `
		SELECT id::TEXT, request_hash
		FROM payouts
		WHERE merchant_id = $1
		  AND (idempotency_key = $2 OR merchant_reference = $3)
		FOR UPDATE
	`, request.MerchantID, request.IdempotencyKey, request.MerchantReference)
	if err != nil {
		return Details{}, false, fmt.Errorf("查询幂等出款单: %w", err)
	}
	defer rows.Close()
	var existingID string
	for rows.Next() {
		var id, hash string
		if err := rows.Scan(&id, &hash); err != nil {
			return Details{}, false, fmt.Errorf("读取幂等出款单: %w", err)
		}
		if hash != requestHash || (existingID != "" && existingID != id) {
			return Details{}, false, ErrPayoutConflict
		}
		existingID = id
	}
	if err := rows.Err(); err != nil {
		return Details{}, false, fmt.Errorf("遍历幂等出款单: %w", err)
	}
	rows.Close()
	if existingID == "" {
		return Details{}, false, nil
	}
	details, err := queryDetails(ctx, databaseTransaction, request.MerchantID, existingID)
	if err != nil {
		return Details{}, false, fmt.Errorf("读取幂等出款单详情: %w", err)
	}
	return details, true, nil
}

func accountBalance(ctx context.Context, transaction pgx.Tx, accountID string) (*big.Int, error) {
	var balanceText string
	err := transaction.QueryRow(ctx, `
		SELECT COALESCE(SUM(
		    CASE
		        WHEN journal.id IS NULL THEN 0
		        WHEN entry.side = account.normal_side THEN entry.amount
		        ELSE -entry.amount
		    END
		), 0)::TEXT
		FROM ledger_accounts AS account
		LEFT JOIN journal_entries AS entry ON entry.account_id = account.id
		LEFT JOIN journal_transactions AS journal
		  ON journal.id = entry.transaction_id AND journal.status = 'posted'
		WHERE account.id = $1
		GROUP BY account.normal_side
	`, accountID).Scan(&balanceText)
	if err != nil {
		return nil, fmt.Errorf("计算出款可用余额: %w", err)
	}
	balance, valid := new(big.Int).SetString(balanceText, 10)
	if !valid || balance.Sign() < 0 {
		return nil, errors.New("出款可用余额无效")
	}
	return balance, nil
}

func normalizeRequest(request Request) Request {
	request.ID = strings.TrimSpace(request.ID)
	request.MerchantID = strings.TrimSpace(request.MerchantID)
	request.AssetID = strings.TrimSpace(request.AssetID)
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	request.MerchantReference = strings.TrimSpace(request.MerchantReference)
	request.DestinationAddress = strings.TrimSpace(request.DestinationAddress)
	request.Amount = strings.TrimSpace(request.Amount)
	return request
}

func validateRequest(request Request) (int64, error) {
	if !identity.ValidUUID(request.ID) || !identity.ValidUUID(request.MerchantID) || request.AssetID == "" ||
		request.IdempotencyKey == "" || len(request.IdempotencyKey) > 128 || request.MerchantReference == "" ||
		len(request.MerchantReference) > 128 || request.DestinationAddress == "" || len(request.DestinationAddress) > 128 ||
		request.Amount == "" || len(request.Amount) > 19 || request.Amount[0] == '0' {
		return 0, ErrInvalidRequest
	}
	amount, err := strconv.ParseInt(request.Amount, 10, 64)
	if err != nil || amount <= 0 {
		return 0, ErrInvalidRequest
	}
	return amount, nil
}

func requestFingerprint(request Request) (string, error) {
	payload := struct {
		MerchantID         string
		AssetID            string
		MerchantReference  string
		DestinationAddress string
		Amount             string
	}{
		MerchantID: request.MerchantID, AssetID: request.AssetID,
		MerchantReference:  request.MerchantReference,
		DestinationAddress: request.DestinationAddress, Amount: request.Amount,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("生成出款请求摘要: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}
