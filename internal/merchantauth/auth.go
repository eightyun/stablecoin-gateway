package merchantauth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	HeaderKey       = "X-Gateway-Key"
	HeaderTimestamp = "X-Gateway-Timestamp"
	HeaderNonce     = "X-Gateway-Nonce"
	HeaderSignature = "X-Gateway-Signature"
)

var (
	ErrUnauthorized  = errors.New("商户请求鉴权失败")
	ErrInvalidConfig = errors.New("商户鉴权配置无效")
	noncePattern     = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)
	keyIDPattern     = regexp.MustCompile(`^gk_[A-Za-z0-9_-]{24}$`)
)

// Principal 是通过鉴权的商户身份。
type Principal struct {
	MerchantID string
	APIKeyID   string
}

// RequestAuthenticator 定义 HTTP 层使用的鉴权接口。
type RequestAuthenticator interface {
	Authenticate(ctx context.Context, request *http.Request, body []byte) (Principal, error)
}

// Authenticator 验证 HMAC 签名、时间窗和 nonce。
type Authenticator struct {
	store   KeyStore
	keyring *Keyring
	maxSkew time.Duration
}

// NewAuthenticator 创建商户请求鉴权器。
func NewAuthenticator(store KeyStore, keyring *Keyring, maxSkew time.Duration) (*Authenticator, error) {
	if store == nil || keyring == nil || maxSkew <= 0 || maxSkew > time.Hour {
		return nil, ErrInvalidConfig
	}
	return &Authenticator{store: store, keyring: keyring, maxSkew: maxSkew}, nil
}

// Authenticate 验证请求，并在签名正确后消费 nonce。
func (authenticator *Authenticator) Authenticate(
	ctx context.Context,
	request *http.Request,
	body []byte,
) (Principal, error) {
	keyID := strings.TrimSpace(request.Header.Get(HeaderKey))
	timestampText := strings.TrimSpace(request.Header.Get(HeaderTimestamp))
	nonce := strings.TrimSpace(request.Header.Get(HeaderNonce))
	signatureText := strings.TrimSpace(request.Header.Get(HeaderSignature))
	if !keyIDPattern.MatchString(keyID) || !noncePattern.MatchString(nonce) || len(signatureText) != sha256.Size*2 {
		return Principal{}, ErrUnauthorized
	}
	timestamp, err := strconv.ParseInt(timestampText, 10, 64)
	if err != nil {
		return Principal{}, ErrUnauthorized
	}
	requestTime := time.Unix(timestamp, 0)
	now := time.Now()
	if requestTime.Before(now.Add(-authenticator.maxSkew)) || requestTime.After(now.Add(authenticator.maxSkew)) {
		return Principal{}, ErrUnauthorized
	}
	providedSignature, err := hex.DecodeString(signatureText)
	if err != nil || len(providedSignature) != sha256.Size {
		return Principal{}, ErrUnauthorized
	}
	record, err := authenticator.store.FindActiveKey(ctx, keyID)
	if errors.Is(err, ErrAPIKeyNotFound) {
		return Principal{}, ErrUnauthorized
	}
	if err != nil {
		return Principal{}, err
	}
	secret, err := authenticator.keyring.Decrypt(record.KeyID, record.Encrypted)
	if err != nil {
		return Principal{}, fmt.Errorf("解密 API Secret: %w", err)
	}
	expectedSignature := signatureBytes(request.Method, request.URL.RequestURI(), timestampText, nonce, body, secret)
	if !hmac.Equal(providedSignature, expectedSignature) {
		return Principal{}, ErrUnauthorized
	}
	if err := authenticator.store.ConsumeNonce(ctx, record.ID, nonce, authenticator.maxSkew*2); err != nil {
		if errors.Is(err, ErrNonceAlreadyUsed) {
			return Principal{}, ErrUnauthorized
		}
		return Principal{}, err
	}
	return Principal{MerchantID: record.MerchantID, APIKeyID: record.ID}, nil
}

// SignRequest 计算客户端需要发送的小写十六进制签名。
func SignRequest(method, requestURI, timestamp, nonce string, body, secret []byte) string {
	return hex.EncodeToString(signatureBytes(method, requestURI, timestamp, nonce, body, secret))
}

func signatureBytes(method, requestURI, timestamp, nonce string, body, secret []byte) []byte {
	bodyHash := sha256.Sum256(body)
	canonical := strings.Join([]string{
		strings.ToUpper(method), requestURI, timestamp, nonce, hex.EncodeToString(bodyHash[:]),
	}, "\n")
	digest := hmac.New(sha256.New, secret)
	_, _ = digest.Write([]byte(canonical))
	return digest.Sum(nil)
}
