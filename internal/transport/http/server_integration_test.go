//go:build integration

package http

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/database"
	"github.com/eightyun/stablecoin-gateway/internal/deposit"
	"github.com/eightyun/stablecoin-gateway/internal/identity"
	"github.com/eightyun/stablecoin-gateway/internal/merchantauth"
)

func TestMerchantDepositAPIEndToEnd(t *testing.T) {
	databaseURL := os.Getenv("GATEWAY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("未设置 GATEWAY_TEST_DATABASE_URL")
	}
	ctx := context.Background()
	pool, err := database.Open(ctx, database.DefaultConfig(databaseURL, "merchant-api-integration-test"))
	if err != nil {
		t.Fatalf("连接测试数据库: %v", err)
	}
	t.Cleanup(pool.Close)

	merchantID := mustUUID(t)
	assetID := "asset-" + mustToken(t, 16)
	addressID := mustUUID(t)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("开始测试数据事务: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `
		INSERT INTO assets (id, network, contract_address, symbol, decimals, status)
		VALUES ($1, $2, $3, 'USDT', 6, 'active')
	`, assetID, "tron-"+mustToken(t, 16), "41"+mustToken(t, 20)); err != nil {
		t.Fatalf("创建测试资产: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO merchants (id, name, status) VALUES ($1, 'api test merchant', 'active')
	`, merchantID); err != nil {
		t.Fatalf("创建测试商户: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO ledger_accounts (id, owner_type, owner_id, asset_id, code, normal_side, status)
		VALUES
			($1, 'platform', 'gateway', $3, 'custody', 'D', 'active'),
			($2, 'merchant', $4, $3, 'available', 'C', 'active')
	`, mustUUID(t), mustUUID(t), assetID, merchantID); err != nil {
		t.Fatalf("创建测试账本科目: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO deposit_addresses (id, merchant_id, asset_id, address, status)
		VALUES ($1, $2, $3, $4, 'active')
	`, addressID, merchantID, assetID, "41"+mustToken(t, 20)); err != nil {
		t.Fatalf("创建测试充值地址: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("提交测试数据事务: %v", err)
	}

	keyring, err := merchantauth.NewKeyring(map[string][]byte{"v1": bytes.Repeat([]byte{9}, 32)}, "v1")
	if err != nil {
		t.Fatalf("NewKeyring() error = %v", err)
	}
	authStore, err := merchantauth.NewStore(pool)
	if err != nil {
		t.Fatalf("merchantauth.NewStore() error = %v", err)
	}
	credentials, err := authStore.ProvisionKey(ctx, keyring, merchantID, "integration", nil)
	if err != nil {
		t.Fatalf("ProvisionKey() error = %v", err)
	}
	authenticator, err := merchantauth.NewAuthenticator(authStore, keyring, 5*time.Minute)
	if err != nil {
		t.Fatalf("NewAuthenticator() error = %v", err)
	}
	depositStore, err := deposit.NewStore(pool)
	if err != nil {
		t.Fatalf("deposit.NewStore() error = %v", err)
	}
	handler, err := NewHandler(authenticator, depositStore, 1<<20)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}

	body := []byte(`{"merchant_reference":"order-e2e","asset_id":"` + assetID + `","amount":"1000000","expires_in_seconds":900}`)
	response := serveSigned(t, handler, credentials, http.MethodPost, "/v1/deposits", body, "idem-e2e")
	if response.Code != http.StatusCreated {
		t.Fatalf("首次创建 response=%d body=%q", response.Code, response.Body.String())
	}
	var created struct {
		Data deposit.IntentDetails `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil || created.Data.ID == "" || created.Data.DepositAddress == "" {
		t.Fatalf("解析创建响应: %+v, %v", created, err)
	}

	response = serveSigned(t, handler, credentials, http.MethodPost, "/v1/deposits", body, "idem-e2e")
	if response.Code != http.StatusOK {
		t.Fatalf("幂等重试 response=%d body=%q", response.Code, response.Body.String())
	}
	response = serveSigned(t, handler, credentials, http.MethodGet, "/v1/deposits/"+created.Data.ID, nil, "")
	if response.Code != http.StatusOK {
		t.Fatalf("查询充值 response=%d body=%q", response.Code, response.Body.String())
	}
	response = serveSigned(t, handler, credentials, http.MethodGet, "/v1/balances", nil, "")
	if response.Code != http.StatusOK {
		t.Fatalf("查询余额 response=%d body=%q", response.Code, response.Body.String())
	}
}

func serveSigned(
	t *testing.T,
	handler http.Handler,
	credentials merchantauth.Credentials,
	method, target string,
	body []byte,
	idempotencyKey string,
) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, bytes.NewReader(body))
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if idempotencyKey != "" {
		request.Header.Set(idempotencyHeader, idempotencyKey)
	}
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := mustToken(t, 18)
	request.Header.Set(merchantauth.HeaderKey, credentials.KeyID)
	request.Header.Set(merchantauth.HeaderTimestamp, timestamp)
	request.Header.Set(merchantauth.HeaderNonce, nonce)
	request.Header.Set(merchantauth.HeaderSignature, merchantauth.SignRequest(
		method, request.URL.RequestURI(), timestamp, nonce, body, []byte(credentials.Secret),
	))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func mustUUID(t *testing.T) string {
	t.Helper()
	value, err := identity.NewUUID()
	if err != nil {
		t.Fatalf("生成 UUID: %v", err)
	}
	return value
}

func mustToken(t *testing.T, size int) string {
	t.Helper()
	value, err := identity.NewToken(size)
	if err != nil {
		t.Fatalf("生成随机令牌: %v", err)
	}
	return value
}
