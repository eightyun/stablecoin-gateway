// Package signhttp 通过 HTTPS 调用隔离的 TRON 交易签名服务。
package signhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/chain/tron"
)

const (
	defaultMaxResponseBytes  int64 = 2 << 20
	maxSignedPayloadBytes          = 1 << 20
	minimumRemainingLifetime       = 15 * time.Second
	maximumFutureSkew              = 30 * time.Second
)

var (
	ErrInvalidConfig    = errors.New("远程签名器配置无效")
	ErrInvalidRequest   = errors.New("远程签名请求无效")
	ErrInvalidResponse  = errors.New("远程签名响应无效")
	ErrResponseTooLarge = errors.New("远程签名响应超过大小限制")
)

// Config 是远程签名服务连接配置。
type Config struct {
	BaseURL                string
	BearerToken            string
	ExpectedOwnerAddress   string
	MaxFeeLimit            int64
	MaxTransactionLifetime time.Duration
	MaxResponseBytes       int64
}

// Client 只传递语义化转账请求，不持有私钥。
type Client struct {
	endpoint               string
	bearerToken            string
	maxResponseBytes       int64
	expectedOwnerAddress   string
	maxFeeLimit            int64
	maxTransactionLifetime time.Duration
	httpClient             *http.Client
}

var _ tron.TransferSigner = (*Client)(nil)

// New 创建远程签名客户端。自定义 HTTP Client 可用于注入 mTLS Transport。
func New(config Config, httpClient *http.Client) (*Client, error) {
	baseURL, err := url.Parse(strings.TrimSpace(config.BaseURL))
	token := strings.TrimSpace(config.BearerToken)
	ownerAddress := strings.TrimSpace(config.ExpectedOwnerAddress)
	_, ownerErr := tron.NormalizeAddressHex(ownerAddress)
	if err != nil || baseURL.Scheme != "https" || baseURL.Host == "" || baseURL.User != nil ||
		baseURL.RawQuery != "" || baseURL.Fragment != "" || token == "" || len(token) > 4096 ||
		ownerErr != nil || config.MaxFeeLimit <= 0 || config.MaxTransactionLifetime <= minimumRemainingLifetime ||
		config.MaxResponseBytes < 0 {
		return nil, ErrInvalidConfig
	}
	for _, character := range token {
		if character <= 0x20 || character == 0x7f {
			return nil, ErrInvalidConfig
		}
	}
	maxResponseBytes := config.MaxResponseBytes
	if maxResponseBytes == 0 {
		maxResponseBytes = defaultMaxResponseBytes
	}
	if maxResponseBytes < 1024 || maxResponseBytes > 16<<20 {
		return nil, ErrInvalidConfig
	}
	baseURL.Path = strings.TrimRight(baseURL.Path, "/") + "/v1/tron/transfers:sign"
	if httpClient == nil {
		httpClient = &http.Client{
			Timeout: 15 * time.Second,
		}
	}
	configuredHTTPClient := *httpClient
	configuredHTTPClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &Client{
		endpoint: baseURL.String(), bearerToken: token,
		expectedOwnerAddress: ownerAddress, maxFeeLimit: config.MaxFeeLimit,
		maxTransactionLifetime: config.MaxTransactionLifetime,
		maxResponseBytes:       maxResponseBytes, httpClient: &configuredHTTPClient,
	}, nil
}

// SignTransfer 请求隔离服务构造并签署一笔 TRC20 转账。
func (client *Client) SignTransfer(
	ctx context.Context,
	request tron.TransferSignRequest,
) (tron.SignedTransaction, error) {
	request = normalizeRequest(request)
	if err := validateRequest(request); err != nil {
		return tron.SignedTransaction{}, err
	}
	body, err := json.Marshal(struct {
		RequestID          string `json:"request_id"`
		Network            string `json:"network"`
		ContractAddress    string `json:"contract_address"`
		DestinationAddress string `json:"destination_address"`
		Amount             string `json:"amount"`
	}{
		RequestID: request.RequestID, Network: request.Network,
		ContractAddress: request.ContractAddress, DestinationAddress: request.DestinationAddress,
		Amount: request.Amount,
	})
	if err != nil {
		return tron.SignedTransaction{}, fmt.Errorf("编码远程签名请求: %w", err)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, client.endpoint, bytes.NewReader(body))
	if err != nil {
		return tron.SignedTransaction{}, fmt.Errorf("创建远程签名请求: %w", err)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+client.bearerToken)
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json")
	httpRequest.Header.Set("Idempotency-Key", request.RequestID)
	response, err := client.httpClient.Do(httpRequest)
	if err != nil {
		return tron.SignedTransaction{}, fmt.Errorf("调用远程签名服务: %w", err)
	}
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, client.maxResponseBytes+1)
	responseBody, err := io.ReadAll(limited)
	if err != nil {
		return tron.SignedTransaction{}, fmt.Errorf("读取远程签名响应: %w", err)
	}
	if int64(len(responseBody)) > client.maxResponseBytes {
		return tron.SignedTransaction{}, ErrResponseTooLarge
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return tron.SignedTransaction{}, fmt.Errorf("远程签名服务返回 HTTP %d: %w", response.StatusCode, ErrInvalidResponse)
	}
	transaction, err := parseResponse(responseBody)
	if err != nil {
		return tron.SignedTransaction{}, err
	}
	if err := tron.ValidateSignedTransferTransaction(transaction, tron.TransferTransactionExpectation{
		OwnerAddress: client.expectedOwnerAddress, ContractAddress: request.ContractAddress,
		DestinationAddress: request.DestinationAddress, Amount: request.Amount, Now: time.Now().UTC(),
		MaxFeeLimit: client.maxFeeLimit, MaxLifetime: client.maxTransactionLifetime,
		MinRemainingLifetime: minimumRemainingLifetime, MaxFutureSkew: maximumFutureSkew,
	}); err != nil {
		return tron.SignedTransaction{}, ErrInvalidResponse
	}
	return transaction, nil
}

func parseResponse(body []byte) (tron.SignedTransaction, error) {
	var response struct {
		TransactionID     string          `json:"transaction_id"`
		SignedTransaction json.RawMessage `json:"signed_transaction"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil {
		return tron.SignedTransaction{}, ErrInvalidResponse
	}
	if err := ensureJSONEOF(decoder); err != nil ||
		len(response.SignedTransaction) == 0 || len(response.SignedTransaction) > maxSignedPayloadBytes {
		return tron.SignedTransaction{}, ErrInvalidResponse
	}
	transaction := tron.SignedTransaction{
		ID: response.TransactionID, Payload: bytes.Clone(response.SignedTransaction),
	}
	if err := tron.ValidateSignedTransaction(transaction); err != nil {
		return tron.SignedTransaction{}, ErrInvalidResponse
	}
	return transaction, nil
}

func normalizeRequest(request tron.TransferSignRequest) tron.TransferSignRequest {
	request.RequestID = strings.TrimSpace(request.RequestID)
	request.Network = strings.TrimSpace(request.Network)
	request.ContractAddress = strings.TrimSpace(request.ContractAddress)
	request.DestinationAddress = strings.TrimSpace(request.DestinationAddress)
	request.Amount = strings.TrimSpace(request.Amount)
	return request
}

func validateRequest(request tron.TransferSignRequest) error {
	if request.RequestID == "" || len(request.RequestID) > 128 ||
		(request.Network != "tron-nile" && request.Network != "tron-shasta" && request.Network != "tron-mainnet") ||
		request.ContractAddress == "" || len(request.ContractAddress) > 128 || request.Amount == "" ||
		len(request.Amount) > 19 || request.Amount[0] == '0' {
		return ErrInvalidRequest
	}
	if _, err := tron.NormalizeAddress(request.DestinationAddress); err != nil {
		return ErrInvalidRequest
	}
	if _, err := tron.NormalizeAddressHex(request.ContractAddress); err != nil {
		return ErrInvalidRequest
	}
	for _, digit := range request.Amount {
		if digit < '0' || digit > '9' {
			return ErrInvalidRequest
		}
	}
	return nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrInvalidResponse
	}
	return nil
}
