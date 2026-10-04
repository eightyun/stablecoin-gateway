package screening

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/identity"
)

const (
	defaultMaxResponseBytes int64 = 64 << 10
	maximumFutureSkew             = 30 * time.Second
)

var (
	ErrInvalidProviderConfig    = errors.New("地址筛查 Provider 配置无效")
	ErrInvalidProviderRequest   = errors.New("地址筛查 Provider 请求无效")
	ErrInvalidProviderResponse  = errors.New("地址筛查 Provider 响应无效")
	ErrProviderResponseTooLarge = errors.New("地址筛查 Provider 响应超过大小限制")
)

// HTTPConfig 定义内部筛查适配服务连接和结果时效边界。
type HTTPConfig struct {
	BaseURL           string
	BearerToken       string
	ProviderName      string
	RequestTimeout    time.Duration
	MaxResponseBytes  int64
	MaxResultValidity time.Duration
}

// HTTPProvider 通过固定 HTTPS 契约调用部署方的供应商适配服务。
type HTTPProvider struct {
	endpoint          string
	bearerToken       string
	providerName      string
	maxResponseBytes  int64
	maxResultValidity time.Duration
	httpClient        *http.Client
}

var _ Provider = (*HTTPProvider)(nil)

// NewHTTPProvider 创建拒绝重定向且最低使用 TLS 1.2 的 Provider 客户端。
func NewHTTPProvider(config HTTPConfig, httpClient *http.Client) (*HTTPProvider, error) {
	baseURL, err := url.Parse(strings.TrimSpace(config.BaseURL))
	token := strings.TrimSpace(config.BearerToken)
	providerName := strings.TrimSpace(config.ProviderName)
	if err != nil || baseURL.Scheme != "https" || baseURL.Host == "" || baseURL.User != nil ||
		baseURL.RawQuery != "" || baseURL.Fragment != "" || token == "" || len(token) > 4096 ||
		providerName == "" || len(providerName) > 128 || config.RequestTimeout <= 0 ||
		config.MaxResponseBytes < 0 || config.MaxResultValidity <= time.Minute ||
		config.MaxResultValidity > 30*24*time.Hour {
		return nil, ErrInvalidProviderConfig
	}
	for _, character := range token {
		if character <= 0x20 || character == 0x7f {
			return nil, ErrInvalidProviderConfig
		}
	}
	maxResponseBytes := config.MaxResponseBytes
	if maxResponseBytes == 0 {
		maxResponseBytes = defaultMaxResponseBytes
	}
	if maxResponseBytes < 1024 || maxResponseBytes > 1<<20 {
		return nil, ErrInvalidProviderConfig
	}
	baseURL.Path = strings.TrimRight(baseURL.Path, "/") + "/v1/address-screenings"
	if httpClient == nil {
		transport, ok := http.DefaultTransport.(*http.Transport)
		if !ok {
			return nil, ErrInvalidProviderConfig
		}
		configuredTransport := transport.Clone()
		configuredTransport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		httpClient = &http.Client{Timeout: config.RequestTimeout, Transport: configuredTransport}
	}
	configuredClient := *httpClient
	configuredClient.Timeout = config.RequestTimeout
	configuredClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &HTTPProvider{
		endpoint: baseURL.String(), bearerToken: token, providerName: providerName,
		maxResponseBytes: maxResponseBytes, maxResultValidity: config.MaxResultValidity,
		httpClient: &configuredClient,
	}, nil
}

// Screen 请求筛查并严格验证供应商时间边界和规范决策。
func (provider *HTTPProvider) Screen(ctx context.Context, request Request) (Result, error) {
	request = normalizeRequest(request)
	if err := validateProviderRequest(request); err != nil {
		return Result{}, err
	}
	body, err := json.Marshal(struct {
		RequestID          string `json:"request_id"`
		PayoutID           string `json:"payout_id,omitempty"`
		DepositScreeningID string `json:"deposit_screening_id,omitempty"`
		Direction          string `json:"direction"`
		Network            string `json:"network"`
		AssetID            string `json:"asset_id"`
		ContractAddress    string `json:"contract_address"`
		SourceAddress      string `json:"source_address,omitempty"`
		DestinationAddress string `json:"destination_address"`
		Amount             string `json:"amount"`
	}{
		RequestID: request.RequestID, PayoutID: request.PayoutID,
		DepositScreeningID: request.DepositScreeningID, Direction: request.Direction,
		Network: request.Network, AssetID: request.AssetID, ContractAddress: request.ContractAddress,
		SourceAddress: request.SourceAddress, DestinationAddress: request.DestinationAddress,
		Amount: request.Amount,
	})
	if err != nil {
		return Result{}, fmt.Errorf("编码地址筛查请求: %w", err)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.endpoint, bytes.NewReader(body))
	if err != nil {
		return Result{}, fmt.Errorf("创建地址筛查请求: %w", err)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+provider.bearerToken)
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json")
	httpRequest.Header.Set("Idempotency-Key", request.RequestID)
	response, err := provider.httpClient.Do(httpRequest)
	if err != nil {
		return Result{}, fmt.Errorf("调用地址筛查 Provider: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, provider.maxResponseBytes+1))
	if err != nil {
		return Result{}, fmt.Errorf("读取地址筛查响应: %w", err)
	}
	if int64(len(responseBody)) > provider.maxResponseBytes {
		return Result{}, ErrProviderResponseTooLarge
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Result{}, fmt.Errorf("地址筛查 Provider 返回 HTTP %d: %w", response.StatusCode, ErrInvalidProviderResponse)
	}
	return provider.parseResponse(responseBody, time.Now().UTC())
}

func (provider *HTTPProvider) parseResponse(body []byte, now time.Time) (Result, error) {
	var response struct {
		Decision          string    `json:"decision"`
		ReasonCodes       []string  `json:"reason_codes"`
		ProviderReference string    `json:"provider_reference"`
		CheckedAt         time.Time `json:"checked_at"`
		ValidUntil        time.Time `json:"valid_until"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil || ensureEOF(decoder) != nil {
		return Result{}, ErrInvalidProviderResponse
	}
	hash := sha256.Sum256(body)
	result := Result{
		Provider: provider.providerName, Decision: strings.TrimSpace(response.Decision),
		ReasonCodes: response.ReasonCodes, ProviderReference: strings.TrimSpace(response.ProviderReference),
		ResponseHash: hex.EncodeToString(hash[:]), CheckedAt: response.CheckedAt.UTC(),
		ValidUntil: response.ValidUntil.UTC(),
	}
	if validateResult(result) != nil || result.CheckedAt.After(now.Add(maximumFutureSkew)) ||
		result.CheckedAt.Before(now.Add(-provider.maxResultValidity)) || !result.ValidUntil.After(now) ||
		result.ValidUntil.After(result.CheckedAt.Add(provider.maxResultValidity)) {
		return Result{}, ErrInvalidProviderResponse
	}
	return result, nil
}

func normalizeRequest(request Request) Request {
	request.RequestID = strings.TrimSpace(request.RequestID)
	request.Direction = strings.TrimSpace(request.Direction)
	request.PayoutID = strings.TrimSpace(request.PayoutID)
	request.DepositScreeningID = strings.TrimSpace(request.DepositScreeningID)
	request.Network = strings.TrimSpace(request.Network)
	request.AssetID = strings.TrimSpace(request.AssetID)
	request.ContractAddress = strings.TrimSpace(request.ContractAddress)
	request.SourceAddress = strings.TrimSpace(request.SourceAddress)
	request.DestinationAddress = strings.TrimSpace(request.DestinationAddress)
	request.Amount = strings.TrimSpace(request.Amount)
	return request
}

func validateProviderRequest(request Request) error {
	if request.RequestID == "" || len(request.RequestID) > 128 ||
		request.Network == "" || len(request.Network) > 128 || request.AssetID == "" || len(request.AssetID) > 256 ||
		request.ContractAddress == "" || len(request.ContractAddress) > 128 ||
		request.DestinationAddress == "" || len(request.DestinationAddress) > 128 ||
		request.Amount == "" || len(request.Amount) > 78 || request.Amount[0] == '0' {
		return ErrInvalidProviderRequest
	}
	switch request.Direction {
	case DirectionOutbound:
		if !identity.ValidUUID(request.PayoutID) || request.DepositScreeningID != "" || request.SourceAddress != "" {
			return ErrInvalidProviderRequest
		}
	case DirectionInbound:
		if request.PayoutID != "" || !identity.ValidUUID(request.DepositScreeningID) ||
			request.SourceAddress == "" || len(request.SourceAddress) > 128 {
			return ErrInvalidProviderRequest
		}
	default:
		return ErrInvalidProviderRequest
	}
	for _, digit := range request.Amount {
		if digit < '0' || digit > '9' {
			return ErrInvalidProviderRequest
		}
	}
	return nil
}

func ensureEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrInvalidProviderResponse
	}
	return nil
}
