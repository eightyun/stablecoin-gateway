package merchantauth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/identity"
	"github.com/jackc/pgx/v5"
)

// Credentials 是仅在创建 API Key 时返回一次的凭证。
type Credentials struct {
	KeyID  string `json:"key_id"`
	Secret string `json:"secret"`
}

// ProvisionKey 为现有活跃商户创建新的 HMAC API Key。
func (store *Store) ProvisionKey(
	ctx context.Context,
	keyring *Keyring,
	merchantID, name string,
	expiresAt *time.Time,
) (Credentials, error) {
	if keyring == nil || !identity.ValidUUID(strings.TrimSpace(merchantID)) || strings.TrimSpace(name) == "" || len(name) > 128 ||
		(expiresAt != nil && !expiresAt.After(time.Now())) {
		return Credentials{}, ErrInvalidSecret
	}
	id, err := identity.NewUUID()
	if err != nil {
		return Credentials{}, err
	}
	publicToken, err := identity.NewToken(18)
	if err != nil {
		return Credentials{}, err
	}
	secretToken, err := identity.NewToken(32)
	if err != nil {
		return Credentials{}, err
	}
	credentials := Credentials{KeyID: "gk_" + publicToken, Secret: "gs_" + secretToken}
	encrypted, err := keyring.Encrypt(credentials.KeyID, []byte(credentials.Secret))
	if err != nil {
		return Credentials{}, err
	}
	var createdID string
	err = store.db.QueryRow(ctx, `
		INSERT INTO merchant_api_keys (
			id, merchant_id, key_id, name, secret_ciphertext, secret_nonce,
			encryption_key_version, status, expires_at
		)
		SELECT $1, merchant.id, $3, $4, $5, $6, $7, 'active', $8
		FROM merchants AS merchant
		WHERE merchant.id = $2 AND merchant.status = 'active'
		RETURNING id::TEXT
	`, id, merchantID, credentials.KeyID, strings.TrimSpace(name), encrypted.Ciphertext,
		encrypted.Nonce, encrypted.Version, expiresAt).Scan(&createdID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Credentials{}, ErrMerchantUnavailable
	}
	if err != nil {
		return Credentials{}, fmt.Errorf("创建商户 API Key: %w", err)
	}
	return credentials, nil
}
