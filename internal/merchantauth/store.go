package merchantauth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrDatabaseRequired    = errors.New("数据库连接不能为空")
	ErrAPIKeyNotFound      = errors.New("API Key 不存在或不可用")
	ErrNonceAlreadyUsed    = errors.New("请求 nonce 已使用")
	ErrMerchantUnavailable = errors.New("商户不存在或不可用")
)

// KeyRecord 是签名验证需要的 API Key 数据。
type KeyRecord struct {
	ID         string
	MerchantID string
	KeyID      string
	Encrypted  EncryptedSecret
}

// KeyStore 定义鉴权所需的持久化操作。
type KeyStore interface {
	FindActiveKey(ctx context.Context, keyID string) (KeyRecord, error)
	ConsumeNonce(ctx context.Context, apiKeyID, nonce string, ttl time.Duration) error
}

// Store 使用 PostgreSQL 保存 API Key 和请求 nonce。
type Store struct {
	db *pgxpool.Pool
}

// NewStore 创建商户鉴权 Store。
func NewStore(db *pgxpool.Pool) (*Store, error) {
	if db == nil {
		return nil, ErrDatabaseRequired
	}
	return &Store{db: db}, nil
}

// FindActiveKey 查找有效且所属商户正常的 API Key。
func (store *Store) FindActiveKey(ctx context.Context, keyID string) (KeyRecord, error) {
	var record KeyRecord
	err := store.db.QueryRow(ctx, `
		SELECT key.id::TEXT, key.merchant_id::TEXT, key.key_id,
		       key.secret_ciphertext, key.secret_nonce, key.encryption_key_version
		FROM merchant_api_keys AS key
		JOIN merchants AS merchant ON merchant.id = key.merchant_id
		WHERE key.key_id = $1
		  AND key.status = 'active'
		  AND (key.expires_at IS NULL OR key.expires_at > clock_timestamp())
		  AND merchant.status = 'active'
	`, strings.TrimSpace(keyID)).Scan(
		&record.ID, &record.MerchantID, &record.KeyID,
		&record.Encrypted.Ciphertext, &record.Encrypted.Nonce, &record.Encrypted.Version,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return KeyRecord{}, ErrAPIKeyNotFound
	}
	if err != nil {
		return KeyRecord{}, fmt.Errorf("查询 API Key: %w", err)
	}
	return record, nil
}

// ConsumeNonce 原子记录 nonce；同一 API Key 的 nonce 只能成功一次。
func (store *Store) ConsumeNonce(ctx context.Context, apiKeyID, nonce string, ttl time.Duration) error {
	var insertedID string
	err := store.db.QueryRow(ctx, `
		WITH cleanup AS (
			DELETE FROM merchant_api_nonces
			WHERE api_key_id = $1 AND expires_at <= clock_timestamp()
		)
		INSERT INTO merchant_api_nonces (api_key_id, nonce, expires_at)
		VALUES ($1, $2, clock_timestamp() + ($3 * INTERVAL '1 millisecond'))
		ON CONFLICT DO NOTHING
		RETURNING api_key_id::TEXT
	`, apiKeyID, nonce, ttl.Milliseconds()).Scan(&insertedID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNonceAlreadyUsed
	}
	if err != nil {
		return fmt.Errorf("记录 API 请求 nonce: %w", err)
	}
	return nil
}
