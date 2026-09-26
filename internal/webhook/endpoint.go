// Package webhook 提供商户 Webhook 端点管理和可靠投递能力。
package webhook

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/eightyun/stablecoin-gateway/internal/identity"
	"github.com/eightyun/stablecoin-gateway/internal/secretbox"
	"github.com/jackc/pgx/v5"
)

var (
	ErrDatabaseRequired    = errors.New("数据库连接不能为空")
	ErrInvalidEndpoint     = errors.New("Webhook 端点无效")
	ErrMerchantUnavailable = errors.New("商户不存在或不可用")
)

// Credentials 是仅在创建端点时返回一次的签名凭证。
type Credentials struct {
	EndpointID string `json:"endpoint_id"`
	Secret     string `json:"secret"`
}

// ProvisionEndpoint 创建商户 Webhook 端点并加密保存签名 Secret。
func (store *Store) ProvisionEndpoint(
	ctx context.Context,
	keyring *secretbox.Keyring,
	merchantID, name, rawURL string,
) (Credentials, error) {
	merchantID = strings.TrimSpace(merchantID)
	name = strings.TrimSpace(name)
	endpointURL, err := normalizeEndpointURL(rawURL)
	if keyring == nil || !identity.ValidUUID(merchantID) || name == "" || len(name) > 128 || err != nil {
		return Credentials{}, ErrInvalidEndpoint
	}
	endpointID, err := identity.NewUUID()
	if err != nil {
		return Credentials{}, err
	}
	secretToken, err := identity.NewToken(32)
	if err != nil {
		return Credentials{}, err
	}
	credentials := Credentials{EndpointID: endpointID, Secret: "whsec_" + secretToken}
	encrypted, err := keyring.Encrypt(endpointAAD(endpointID), []byte(credentials.Secret))
	if err != nil {
		return Credentials{}, err
	}
	var createdID string
	err = store.db.QueryRow(ctx, `
		INSERT INTO merchant_webhook_endpoints (
			id, merchant_id, name, url, secret_ciphertext, secret_nonce,
			encryption_key_version, status
		)
		SELECT $1, merchant.id, $3, $4, $5, $6, $7, 'active'
		FROM merchants AS merchant
		WHERE merchant.id = $2 AND merchant.status = 'active'
		RETURNING id::TEXT
	`, endpointID, merchantID, name, endpointURL, encrypted.Ciphertext,
		encrypted.Nonce, encrypted.Version).Scan(&createdID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Credentials{}, ErrMerchantUnavailable
	}
	if err != nil {
		return Credentials{}, fmt.Errorf("创建 Webhook 端点: %w", err)
	}
	return credentials, nil
}

func normalizeEndpointURL(raw string) (string, error) {
	if len(raw) > 2048 {
		return "", ErrInvalidEndpoint
	}
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return "", ErrInvalidEndpoint
	}
	if strings.TrimSpace(parsed.Hostname()) == "" {
		return "", ErrInvalidEndpoint
	}
	return parsed.String(), nil
}

func endpointAAD(endpointID string) string {
	return "webhook-endpoint:" + endpointID
}
