package deposit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var (
	ErrInvalidPoolIntent = errors.New("充值意图请求无效")
	ErrAddressPoolEmpty  = errors.New("没有可分配的充值地址")
	ErrIntentNotFound    = errors.New("充值意图不存在")
)

// PoolIntentRequest 是从地址池创建充值意图的请求。
type PoolIntentRequest struct {
	ID                string
	MerchantID        string
	AssetID           string
	IdempotencyKey    string
	MerchantReference string
	ExpectedAmount    string
	ExpiresIn         time.Duration
}

// IntentDetails 是商户可查询的充值意图信息。
type IntentDetails struct {
	ID                string     `json:"id"`
	MerchantReference string     `json:"merchant_reference"`
	AssetID           string     `json:"asset_id"`
	Network           string     `json:"network"`
	ContractAddress   string     `json:"contract_address"`
	Symbol            string     `json:"symbol"`
	Decimals          int16      `json:"decimals"`
	DepositAddress    string     `json:"deposit_address"`
	ExpectedAmount    string     `json:"expected_amount"`
	ReceivedAmount    string     `json:"received_amount"`
	Status            string     `json:"status"`
	ExpiresAt         time.Time  `json:"expires_at"`
	CreatedAt         time.Time  `json:"created_at"`
	CreditedAt        *time.Time `json:"credited_at,omitempty"`
}

// PoolIntentResult 表示地址池创建结果。
type PoolIntentResult struct {
	Intent  IntentDetails
	Created bool
}

// Balance 是商户一个资产的可用余额。
type Balance struct {
	AssetID         string `json:"asset_id"`
	Network         string `json:"network"`
	ContractAddress string `json:"contract_address"`
	Symbol          string `json:"symbol"`
	Decimals        int16  `json:"decimals"`
	Available       string `json:"available"`
	Frozen          string `json:"frozen"`
}

// CreateIntentFromPool 幂等创建充值意图，并原子领取一个未使用地址。
func (store *Store) CreateIntentFromPool(
	ctx context.Context,
	request PoolIntentRequest,
) (result PoolIntentResult, err error) {
	if err := validatePoolIntent(request); err != nil {
		return PoolIntentResult{}, err
	}
	requestHash, err := poolIntentFingerprint(request)
	if err != nil {
		return PoolIntentResult{}, err
	}
	transaction, err := store.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return PoolIntentResult{}, fmt.Errorf("开始创建充值意图事务: %w", err)
	}
	defer func() {
		if rollbackErr := transaction.Rollback(context.Background()); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			err = errors.Join(err, fmt.Errorf("回滚创建充值意图事务: %w", rollbackErr))
		}
	}()

	if existing, found, err := findPoolIntent(ctx, transaction, request, requestHash); err != nil || found {
		if err != nil {
			return PoolIntentResult{}, err
		}
		if err = transaction.Commit(ctx); err != nil {
			return PoolIntentResult{}, fmt.Errorf("提交充值意图幂等查询: %w", err)
		}
		return PoolIntentResult{Intent: existing, Created: false}, nil
	}

	var addressID string
	var details IntentDetails
	err = transaction.QueryRow(ctx, `
		SELECT address.id::TEXT, address.address, asset.network, asset.contract_address,
		       asset.symbol, asset.decimals
		FROM deposit_addresses AS address
		JOIN merchants AS merchant ON merchant.id = address.merchant_id
		JOIN assets AS asset ON asset.id = address.asset_id
		JOIN ledger_accounts AS custody
		  ON custody.owner_type = 'platform'
		 AND custody.owner_id = $3
		 AND custody.asset_id = asset.id
		 AND custody.code = $4
		 AND custody.normal_side = 'D'
		 AND custody.status = 'active'
		JOIN ledger_accounts AS available
		  ON available.owner_type = 'merchant'
		 AND available.owner_id = merchant.id::TEXT
		 AND available.asset_id = asset.id
		 AND available.code = $5
		 AND available.normal_side = 'C'
		 AND available.status = 'active'
		LEFT JOIN deposit_intents AS intent ON intent.deposit_address_id = address.id
		WHERE address.merchant_id = $1
		  AND address.asset_id = $2
		  AND address.status = 'active'
		  AND merchant.status = 'active'
		  AND asset.status = 'active'
		  AND intent.id IS NULL
		ORDER BY address.created_at, address.id
		FOR UPDATE OF address SKIP LOCKED
		LIMIT 1
	`, request.MerchantID, request.AssetID, platformLedgerOwnerID,
		custodyAccountCode, availableAccountCode).Scan(
		&addressID, &details.DepositAddress, &details.Network, &details.ContractAddress,
		&details.Symbol, &details.Decimals,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return PoolIntentResult{}, ErrAddressPoolEmpty
	}
	if err != nil {
		return PoolIntentResult{}, fmt.Errorf("领取充值地址: %w", err)
	}

	command := transaction.QueryRow(ctx, `
		INSERT INTO deposit_intents (
			id, merchant_id, asset_id, deposit_address_id, idempotency_key,
			merchant_reference, request_hash, expected_amount, expires_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8,
			clock_timestamp() + ($9 * INTERVAL '1 second')
		)
		ON CONFLICT DO NOTHING
		RETURNING id::TEXT, merchant_reference, asset_id, expected_amount::TEXT,
		          received_amount::TEXT, status, expires_at, created_at, credited_at
	`, request.ID, request.MerchantID, request.AssetID, addressID, request.IdempotencyKey,
		request.MerchantReference, requestHash, request.ExpectedAmount, int64(request.ExpiresIn/time.Second))
	err = command.Scan(
		&details.ID, &details.MerchantReference, &details.AssetID, &details.ExpectedAmount,
		&details.ReceivedAmount, &details.Status, &details.ExpiresAt, &details.CreatedAt, &details.CreditedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		existing, found, findErr := findPoolIntent(ctx, transaction, request, requestHash)
		if findErr != nil {
			return PoolIntentResult{}, findErr
		}
		if !found {
			return PoolIntentResult{}, errors.New("充值意图创建条件在并发处理中发生变化")
		}
		if err = transaction.Commit(ctx); err != nil {
			return PoolIntentResult{}, fmt.Errorf("提交充值意图幂等结果: %w", err)
		}
		return PoolIntentResult{Intent: existing, Created: false}, nil
	}
	if err != nil {
		return PoolIntentResult{}, fmt.Errorf("创建地址池充值意图: %w", err)
	}
	if err = transaction.Commit(ctx); err != nil {
		return PoolIntentResult{}, fmt.Errorf("提交地址池充值意图: %w", err)
	}
	return PoolIntentResult{Intent: details, Created: true}, nil
}

// GetIntent 返回指定商户的充值意图。
func (store *Store) GetIntent(ctx context.Context, merchantID, intentID string) (IntentDetails, error) {
	details, err := queryIntent(ctx, store.db, merchantID, intentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return IntentDetails{}, ErrIntentNotFound
	}
	if err != nil {
		return IntentDetails{}, fmt.Errorf("查询充值意图: %w", err)
	}
	return details, nil
}

// ListBalances 返回商户所有账本科目的可用余额。
func (store *Store) ListBalances(ctx context.Context, merchantID string) ([]Balance, error) {
	rows, err := store.db.Query(ctx, `
		SELECT asset.id, asset.network, asset.contract_address, asset.symbol, asset.decimals,
		       COALESCE(SUM(CASE WHEN account.code = $2 THEN
		           CASE
		               WHEN journal.id IS NULL THEN 0
		               WHEN entry.side = account.normal_side THEN entry.amount
		               ELSE -entry.amount
		           END
		       ELSE 0 END), 0)::TEXT AS available,
		       COALESCE(SUM(CASE WHEN account.code = $3 THEN
		           CASE
		               WHEN journal.id IS NULL THEN 0
		               WHEN entry.side = account.normal_side THEN entry.amount
		               ELSE -entry.amount
		           END
		       ELSE 0 END), 0)::TEXT AS frozen
		FROM ledger_accounts AS account
		JOIN assets AS asset ON asset.id = account.asset_id
		LEFT JOIN journal_entries AS entry ON entry.account_id = account.id
		LEFT JOIN journal_transactions AS journal
		  ON journal.id = entry.transaction_id AND journal.status = 'posted'
		WHERE account.owner_type = 'merchant'
		  AND account.owner_id = $1
		  AND account.code IN ($2, $3)
		  AND account.status IN ('active', 'locked')
		GROUP BY asset.id, asset.network, asset.contract_address, asset.symbol, asset.decimals
		ORDER BY asset.id
	`, merchantID, availableAccountCode, frozenAccountCode)
	if err != nil {
		return nil, fmt.Errorf("查询商户余额: %w", err)
	}
	defer rows.Close()
	balances := make([]Balance, 0)
	for rows.Next() {
		var balance Balance
		if err := rows.Scan(
			&balance.AssetID, &balance.Network, &balance.ContractAddress, &balance.Symbol,
			&balance.Decimals, &balance.Available, &balance.Frozen,
		); err != nil {
			return nil, fmt.Errorf("读取商户余额: %w", err)
		}
		balances = append(balances, balance)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历商户余额: %w", err)
	}
	return balances, nil
}

type intentQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func queryIntent(ctx context.Context, querier intentQuerier, merchantID, intentID string) (IntentDetails, error) {
	var details IntentDetails
	err := querier.QueryRow(ctx, `
		SELECT intent.id::TEXT, intent.merchant_reference, intent.asset_id,
		       asset.network, asset.contract_address, asset.symbol, asset.decimals,
		       address.address, intent.expected_amount::TEXT, intent.received_amount::TEXT,
		       intent.status, intent.expires_at, intent.created_at, intent.credited_at
		FROM deposit_intents AS intent
		JOIN deposit_addresses AS address ON address.id = intent.deposit_address_id
		JOIN assets AS asset ON asset.id = intent.asset_id
		WHERE intent.merchant_id = $1 AND intent.id = $2
	`, merchantID, intentID).Scan(
		&details.ID, &details.MerchantReference, &details.AssetID,
		&details.Network, &details.ContractAddress, &details.Symbol, &details.Decimals,
		&details.DepositAddress, &details.ExpectedAmount, &details.ReceivedAmount,
		&details.Status, &details.ExpiresAt, &details.CreatedAt, &details.CreditedAt,
	)
	return details, err
}

func findPoolIntent(
	ctx context.Context,
	transaction pgx.Tx,
	request PoolIntentRequest,
	requestHash string,
) (IntentDetails, bool, error) {
	rows, err := transaction.Query(ctx, `
		SELECT intent.id::TEXT, intent.request_hash
		FROM deposit_intents AS intent
		WHERE intent.merchant_id = $1
		  AND (intent.idempotency_key = $2 OR intent.merchant_reference = $3)
		FOR UPDATE
	`, request.MerchantID, request.IdempotencyKey, request.MerchantReference)
	if err != nil {
		return IntentDetails{}, false, fmt.Errorf("查询幂等充值意图: %w", err)
	}
	defer rows.Close()
	var existingID string
	count := 0
	for rows.Next() {
		var id, hash string
		if err := rows.Scan(&id, &hash); err != nil {
			return IntentDetails{}, false, fmt.Errorf("读取幂等充值意图: %w", err)
		}
		if hash != requestHash || (existingID != "" && existingID != id) {
			return IntentDetails{}, false, ErrIntentConflict
		}
		existingID = id
		count++
	}
	if err := rows.Err(); err != nil {
		return IntentDetails{}, false, fmt.Errorf("遍历幂等充值意图: %w", err)
	}
	rows.Close()
	if count == 0 {
		return IntentDetails{}, false, nil
	}
	details, err := queryIntent(ctx, transaction, request.MerchantID, existingID)
	if err != nil {
		return IntentDetails{}, false, fmt.Errorf("读取幂等充值意图详情: %w", err)
	}
	return details, true, nil
}

func validatePoolIntent(request PoolIntentRequest) error {
	if strings.TrimSpace(request.ID) == "" || strings.TrimSpace(request.MerchantID) == "" ||
		strings.TrimSpace(request.AssetID) == "" || strings.TrimSpace(request.IdempotencyKey) == "" ||
		strings.TrimSpace(request.MerchantReference) == "" || len(request.IdempotencyKey) > 128 ||
		len(request.MerchantReference) > 128 || !positiveAmount(request.ExpectedAmount) ||
		request.ExpiresIn < time.Minute || request.ExpiresIn > 24*time.Hour || request.ExpiresIn%time.Second != 0 {
		return ErrInvalidPoolIntent
	}
	return nil
}

func poolIntentFingerprint(request PoolIntentRequest) (string, error) {
	payload := struct {
		MerchantID        string
		AssetID           string
		MerchantReference string
		ExpectedAmount    string
		ExpiresInSeconds  int64
	}{
		MerchantID: request.MerchantID, AssetID: request.AssetID,
		MerchantReference: request.MerchantReference, ExpectedAmount: request.ExpectedAmount,
		ExpiresInSeconds: int64(request.ExpiresIn / time.Second),
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("生成地址池充值意图摘要: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}
