// Package deposit 管理充值地址、充值意图和链事件匹配。
package deposit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrDatabaseRequired   = errors.New("数据库连接不能为空")
	ErrInvalidAddress     = errors.New("充值地址无效")
	ErrInvalidIntent      = errors.New("充值意图无效")
	ErrAddressConflict    = errors.New("充值地址已被不同商户或资产使用")
	ErrIntentConflict     = errors.New("充值意图幂等键或业务引用冲突")
	ErrAddressUnavailable = errors.New("充值地址不存在、已停用或归属不匹配")
	ErrInvalidLimit       = errors.New("处理数量限制无效")
)

// Store 使用 PostgreSQL 保存充值事实。
type Store struct {
	db *pgxpool.Pool
}

// Address 是平台已登记的商户充值地址，不包含私钥。
type Address struct {
	ID         string
	MerchantID string
	AssetID    string
	Address    string
}

// Intent 是一次性地址对应的充值意图，金额使用资产最小单位。
type Intent struct {
	ID                string
	MerchantID        string
	AssetID           string
	DepositAddressID  string
	IdempotencyKey    string
	MerchantReference string
	ExpectedAmount    string
	ExpiresAt         time.Time
}

// CreateResult 表示幂等创建结果。
type CreateResult struct {
	ID      string
	Created bool
}

// MatchResult 表示一次链事件匹配结果。
type MatchResult struct {
	Processed      bool
	TransactionID  string
	LogIndex       uint32
	IntentID       string
	MatchStatus    string
	Reason         string
	IntentStatus   string
	ReceivedAmount string
}

// NewStore 创建充值仓储。
func NewStore(db *pgxpool.Pool) (*Store, error) {
	if db == nil {
		return nil, ErrDatabaseRequired
	}
	return &Store{db: db}, nil
}

// RegisterAddress 幂等登记充值地址。地址一旦属于某商户和资产就不能转移。
func (store *Store) RegisterAddress(ctx context.Context, address Address) (CreateResult, error) {
	normalized, err := normalizeAddress(address.Address)
	if err != nil || strings.TrimSpace(address.ID) == "" || strings.TrimSpace(address.MerchantID) == "" || strings.TrimSpace(address.AssetID) == "" {
		return CreateResult{}, ErrInvalidAddress
	}
	address.Address = normalized
	var id string
	err = store.db.QueryRow(ctx, `
		INSERT INTO deposit_addresses (id, merchant_id, asset_id, address, status)
		SELECT $1, merchant.id, asset.id, $4, 'active'
		FROM merchants AS merchant
		JOIN assets AS asset ON asset.id = $3
		WHERE merchant.id = $2
		  AND merchant.status = 'active'
		  AND asset.status = 'active'
		ON CONFLICT DO NOTHING
		RETURNING id
	`, address.ID, address.MerchantID, address.AssetID, address.Address).Scan(&id)
	if err == nil {
		return CreateResult{ID: id, Created: true}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return CreateResult{}, fmt.Errorf("登记充值地址: %w", err)
	}
	return store.existingAddress(ctx, address)
}

// CreateIntent 幂等创建充值意图。每个地址只能使用一次。
func (store *Store) CreateIntent(ctx context.Context, intent Intent) (CreateResult, error) {
	if err := validateIntent(intent); err != nil {
		return CreateResult{}, err
	}
	requestHash, err := intentFingerprint(intent)
	if err != nil {
		return CreateResult{}, err
	}
	if existing, found, err := store.findExistingIntent(ctx, intent, requestHash); err != nil || found {
		return existing, err
	}
	if !intent.ExpiresAt.After(time.Now()) {
		return CreateResult{}, fmt.Errorf("过期时间必须晚于当前时间: %w", ErrInvalidIntent)
	}

	var id string
	err = store.db.QueryRow(ctx, `
		INSERT INTO deposit_intents (
			id, merchant_id, asset_id, deposit_address_id, idempotency_key,
			merchant_reference, request_hash, expected_amount, expires_at
		)
		SELECT $1, $2, $3, address.id, $5, $6, $7, $8, $9
		FROM deposit_addresses AS address
		JOIN merchants AS merchant ON merchant.id = address.merchant_id
		JOIN assets AS asset ON asset.id = address.asset_id
		WHERE address.id = $4
		  AND address.merchant_id = $2
		  AND address.asset_id = $3
		  AND address.status = 'active'
		  AND merchant.status = 'active'
		  AND asset.status = 'active'
		ON CONFLICT DO NOTHING
		RETURNING id
	`, intent.ID, intent.MerchantID, intent.AssetID, intent.DepositAddressID,
		intent.IdempotencyKey, intent.MerchantReference, requestHash,
		intent.ExpectedAmount, intent.ExpiresAt.UTC()).Scan(&id)
	if err == nil {
		return CreateResult{ID: id, Created: true}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return CreateResult{}, fmt.Errorf("创建充值意图: %w", err)
	}
	if existing, found, err := store.findExistingIntent(ctx, intent, requestHash); err != nil || found {
		return existing, err
	}
	return CreateResult{}, ErrAddressUnavailable
}

func (store *Store) existingAddress(ctx context.Context, address Address) (CreateResult, error) {
	rows, err := store.db.Query(ctx, `
		SELECT id, merchant_id, asset_id, address
		FROM deposit_addresses
		WHERE id = $1 OR (asset_id = $2 AND address = $3)
	`, address.ID, address.AssetID, address.Address)
	if err != nil {
		return CreateResult{}, fmt.Errorf("查询已有充值地址: %w", err)
	}
	defer rows.Close()
	var existingID string
	count := 0
	for rows.Next() {
		var id, merchantID, assetID, chainAddress string
		if err := rows.Scan(&id, &merchantID, &assetID, &chainAddress); err != nil {
			return CreateResult{}, fmt.Errorf("读取已有充值地址: %w", err)
		}
		if merchantID != address.MerchantID || assetID != address.AssetID || chainAddress != address.Address {
			return CreateResult{}, ErrAddressConflict
		}
		existingID = id
		count++
	}
	if err := rows.Err(); err != nil {
		return CreateResult{}, fmt.Errorf("遍历已有充值地址: %w", err)
	}
	if count == 0 {
		return CreateResult{}, ErrAddressUnavailable
	}
	if count != 1 {
		return CreateResult{}, ErrAddressConflict
	}
	return CreateResult{ID: existingID, Created: false}, nil
}

func (store *Store) findExistingIntent(ctx context.Context, intent Intent, requestHash string) (CreateResult, bool, error) {
	rows, err := store.db.Query(ctx, `
		SELECT id, request_hash
		FROM deposit_intents
		WHERE id = $1
		   OR (merchant_id = $2 AND idempotency_key = $3)
		   OR (merchant_id = $2 AND merchant_reference = $4)
		   OR deposit_address_id = $5
	`, intent.ID, intent.MerchantID, intent.IdempotencyKey, intent.MerchantReference, intent.DepositAddressID)
	if err != nil {
		return CreateResult{}, false, fmt.Errorf("查询已有充值意图: %w", err)
	}
	defer rows.Close()
	ids := make(map[string]struct{})
	for rows.Next() {
		var id, existingHash string
		if err := rows.Scan(&id, &existingHash); err != nil {
			return CreateResult{}, false, fmt.Errorf("读取已有充值意图: %w", err)
		}
		if existingHash != requestHash {
			return CreateResult{}, false, ErrIntentConflict
		}
		ids[id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return CreateResult{}, false, fmt.Errorf("遍历已有充值意图: %w", err)
	}
	if len(ids) == 0 {
		return CreateResult{}, false, nil
	}
	if len(ids) != 1 {
		return CreateResult{}, false, ErrIntentConflict
	}
	for id := range ids {
		return CreateResult{ID: id, Created: false}, true, nil
	}
	panic("不可达")
}

func validateIntent(intent Intent) error {
	if strings.TrimSpace(intent.ID) == "" || strings.TrimSpace(intent.MerchantID) == "" ||
		strings.TrimSpace(intent.AssetID) == "" || strings.TrimSpace(intent.DepositAddressID) == "" ||
		strings.TrimSpace(intent.IdempotencyKey) == "" || strings.TrimSpace(intent.MerchantReference) == "" ||
		intent.ExpiresAt.IsZero() || !positiveAmount(intent.ExpectedAmount) {
		return ErrInvalidIntent
	}
	return nil
}

func positiveAmount(value string) bool {
	if value == "" || len(value) > 78 || value[0] == '0' {
		return false
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	amount, ok := new(big.Int).SetString(value, 10)
	return ok && amount.Sign() > 0 && amount.BitLen() <= 256
}

func normalizeAddress(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) != 42 || !strings.HasPrefix(value, "41") {
		return "", ErrInvalidAddress
	}
	if _, err := hex.DecodeString(value); err != nil {
		return "", ErrInvalidAddress
	}
	return value, nil
}

func intentFingerprint(intent Intent) (string, error) {
	payload := struct {
		MerchantID        string
		AssetID           string
		DepositAddressID  string
		MerchantReference string
		ExpectedAmount    string
		ExpiresAt         time.Time
	}{
		MerchantID: intent.MerchantID, AssetID: intent.AssetID,
		DepositAddressID: intent.DepositAddressID, MerchantReference: intent.MerchantReference,
		ExpectedAmount: intent.ExpectedAmount, ExpiresAt: intent.ExpiresAt.UTC(),
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("生成充值意图摘要: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}
