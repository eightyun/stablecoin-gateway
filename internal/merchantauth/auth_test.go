package merchantauth

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

type keyStoreStub struct {
	record   KeyRecord
	findErr  error
	nonceErr error
	nonces   int
}

func (stub *keyStoreStub) FindActiveKey(context.Context, string) (KeyRecord, error) {
	return stub.record, stub.findErr
}

func (stub *keyStoreStub) ConsumeNonce(context.Context, string, string, time.Duration) error {
	stub.nonces++
	return stub.nonceErr
}

func TestAuthenticatorVerifiesSignatureAndConsumesNonce(t *testing.T) {
	keyring, encrypted, secret := authenticationFixture(t)
	store := &keyStoreStub{record: KeyRecord{
		ID: "key-uuid", MerchantID: "merchant-uuid", KeyID: "gk_123456789012345678901234", Encrypted: encrypted,
	}}
	authenticator, err := NewAuthenticator(store, keyring, 5*time.Minute)
	if err != nil {
		t.Fatalf("NewAuthenticator() error = %v", err)
	}
	body := []byte(`{"amount":"100"}`)
	request := httptest.NewRequest("POST", "/v1/deposits?mode=exact", bytes.NewReader(body))
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := "nonce_1234567890abcdef"
	request.Header.Set(HeaderKey, "gk_123456789012345678901234")
	request.Header.Set(HeaderTimestamp, timestamp)
	request.Header.Set(HeaderNonce, nonce)
	request.Header.Set(HeaderSignature, SignRequest(request.Method, request.URL.RequestURI(), timestamp, nonce, body, secret))
	principal, err := authenticator.Authenticate(context.Background(), request, body)
	if err != nil || principal.MerchantID != "merchant-uuid" || store.nonces != 1 {
		t.Fatalf("Authenticate() = %+v, %v, nonces=%d", principal, err, store.nonces)
	}
}

func TestAuthenticatorRejectsInvalidAndReplayedRequests(t *testing.T) {
	keyring, encrypted, secret := authenticationFixture(t)
	tests := []struct {
		name      string
		timestamp string
		sign      func(*keyStoreStub, string, string, []byte) string
		nonceErr  error
	}{
		{name: "过期", timestamp: strconv.FormatInt(time.Now().Add(-time.Hour).Unix(), 10)},
		{name: "错误签名", sign: func(*keyStoreStub, string, string, []byte) string { return string(bytes.Repeat([]byte{'0'}, 64)) }},
		{name: "重放", nonceErr: ErrNonceAlreadyUsed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &keyStoreStub{record: KeyRecord{
				ID: "key-uuid", MerchantID: "merchant-uuid", KeyID: "gk_123456789012345678901234", Encrypted: encrypted,
			}, nonceErr: test.nonceErr}
			authenticator, err := NewAuthenticator(store, keyring, 5*time.Minute)
			if err != nil {
				t.Fatalf("NewAuthenticator() error = %v", err)
			}
			body := []byte(`{}`)
			request := httptest.NewRequest("POST", "/v1/deposits", bytes.NewReader(body))
			timestamp := test.timestamp
			if timestamp == "" {
				timestamp = strconv.FormatInt(time.Now().Unix(), 10)
			}
			nonce := "nonce_1234567890abcdef"
			signature := SignRequest(request.Method, request.URL.RequestURI(), timestamp, nonce, body, secret)
			if test.sign != nil {
				signature = test.sign(store, timestamp, nonce, body)
			}
			request.Header.Set(HeaderKey, "gk_123456789012345678901234")
			request.Header.Set(HeaderTimestamp, timestamp)
			request.Header.Set(HeaderNonce, nonce)
			request.Header.Set(HeaderSignature, signature)
			if _, err := authenticator.Authenticate(context.Background(), request, body); !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("Authenticate() error = %v", err)
			}
		})
	}
}

func authenticationFixture(t *testing.T) (*Keyring, EncryptedSecret, []byte) {
	t.Helper()
	keyring, err := NewKeyring(map[string][]byte{"v1": bytes.Repeat([]byte{7}, 32)}, "v1")
	if err != nil {
		t.Fatalf("NewKeyring() error = %v", err)
	}
	secret := []byte("gs_a-secure-api-secret")
	encrypted, err := keyring.Encrypt("gk_123456789012345678901234", secret)
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}
	return keyring, encrypted, secret
}
