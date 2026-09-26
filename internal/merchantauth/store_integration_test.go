//go:build integration

package merchantauth

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/database"
	"github.com/eightyun/stablecoin-gateway/internal/identity"
)

func TestStoreProvisionsEncryptedKeyAndPreventsNonceReplay(t *testing.T) {
	databaseURL := os.Getenv("GATEWAY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("未设置 GATEWAY_TEST_DATABASE_URL")
	}
	ctx := context.Background()
	pool, err := database.Open(ctx, database.DefaultConfig(databaseURL, "merchantauth-integration-test"))
	if err != nil {
		t.Fatalf("连接测试数据库: %v", err)
	}
	t.Cleanup(pool.Close)
	merchantID, err := identity.NewUUID()
	if err != nil {
		t.Fatalf("生成商户 ID: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO merchants (id, name, status) VALUES ($1, 'auth test merchant', 'active')
	`, merchantID); err != nil {
		t.Fatalf("创建测试商户: %v", err)
	}

	keyring, err := NewKeyring(map[string][]byte{"v1": make([]byte, 32)}, "v1")
	if err != nil {
		t.Fatalf("NewKeyring() error = %v", err)
	}
	store, err := NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	credentials, err := store.ProvisionKey(ctx, keyring, merchantID, "integration", nil)
	if err != nil {
		t.Fatalf("ProvisionKey() error = %v", err)
	}
	record, err := store.FindActiveKey(ctx, credentials.KeyID)
	if err != nil {
		t.Fatalf("FindActiveKey() error = %v", err)
	}
	secret, err := keyring.Decrypt(record.KeyID, record.Encrypted)
	if err != nil || string(secret) != credentials.Secret || record.MerchantID != merchantID {
		t.Fatalf("解密凭证 secret=%q merchant=%q error=%v", secret, record.MerchantID, err)
	}
	var plaintextMatches int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM merchant_api_keys WHERE id = $1 AND secret_ciphertext = $2
	`, record.ID, []byte(credentials.Secret)).Scan(&plaintextMatches); err != nil || plaintextMatches != 0 {
		t.Fatalf("明文密钥持久化检查 count=%d error=%v", plaintextMatches, err)
	}
	if err := store.ConsumeNonce(ctx, record.ID, "nonce_1234567890", time.Minute); err != nil {
		t.Fatalf("首次 ConsumeNonce() error = %v", err)
	}
	if err := store.ConsumeNonce(ctx, record.ID, "nonce_1234567890", time.Minute); !errors.Is(err, ErrNonceAlreadyUsed) {
		t.Fatalf("重复 ConsumeNonce() error = %v", err)
	}
}
