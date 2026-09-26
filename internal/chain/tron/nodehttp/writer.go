package nodehttp

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
	defaultWriterMaxResponseBytes int64 = 64 << 10
)

var (
	ErrInvalidWriterConfig = errors.New("TRON 写节点配置无效")
	ErrBroadcastRejected   = errors.New("TRON 节点拒绝广播")
)

// WriterConfig 是 TRON FullNode 写接口配置。
type WriterConfig struct {
	BaseURL          string
	APIKey           string
	MaxResponseBytes int64
}

// Writer 只向 FullNode 广播已经签名的原始交易，不构造或签署交易。
type Writer struct {
	endpoint         string
	apiKey           string
	maxResponseBytes int64
	httpClient       *http.Client
}

var _ tron.Broadcaster = (*Writer)(nil)

// NewWriter 创建 TRON FullNode 广播客户端。
func NewWriter(config WriterConfig, httpClient *http.Client) (*Writer, error) {
	baseURL, err := url.Parse(strings.TrimSpace(config.BaseURL))
	if err != nil || (baseURL.Scheme != "http" && baseURL.Scheme != "https") || baseURL.Host == "" ||
		baseURL.User != nil || baseURL.RawQuery != "" || baseURL.Fragment != "" || config.MaxResponseBytes < 0 {
		return nil, ErrInvalidWriterConfig
	}
	maxResponseBytes := config.MaxResponseBytes
	if maxResponseBytes == 0 {
		maxResponseBytes = defaultWriterMaxResponseBytes
	}
	if maxResponseBytes < 1024 || maxResponseBytes > 1<<20 {
		return nil, ErrInvalidWriterConfig
	}
	baseURL.Path = strings.TrimRight(baseURL.Path, "/") + "/wallet/broadcasttransaction"
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	configuredHTTPClient := *httpClient
	configuredHTTPClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &Writer{
		endpoint: baseURL.String(), apiKey: strings.TrimSpace(config.APIKey),
		maxResponseBytes: maxResponseBytes, httpClient: &configuredHTTPClient,
	}, nil
}

// Broadcast 将完全相同的签名交易提交给 FullNode。返回 nil 仅表示节点此次调用未报告错误。
func (writer *Writer) Broadcast(ctx context.Context, transaction tron.SignedTransaction) error {
	if err := tron.ValidateSignedTransaction(transaction); err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, writer.endpoint, bytes.NewReader(transaction.Payload))
	if err != nil {
		return fmt.Errorf("创建 TRON 广播请求: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if writer.apiKey != "" {
		request.Header.Set("TRON-PRO-API-KEY", writer.apiKey)
	}
	response, err := writer.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("调用 TRON 广播接口: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, writer.maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("读取 TRON 广播响应: %w", err)
	}
	if int64(len(responseBody)) > writer.maxResponseBytes {
		return ErrResponseTooLarge
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("TRON 广播接口返回 HTTP %d: %w", response.StatusCode, ErrBroadcastRejected)
	}
	var result struct {
		Accepted      *bool  `json:"result"`
		TransactionID string `json:"txid"`
		Code          string `json:"code"`
	}
	decoder := json.NewDecoder(bytes.NewReader(responseBody))
	if err := decoder.Decode(&result); err != nil || result.Accepted == nil {
		return ErrInvalidResponse
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrInvalidResponse
	}
	if !*result.Accepted {
		code := strings.TrimSpace(result.Code)
		if code == "" {
			return ErrBroadcastRejected
		}
		return fmt.Errorf("TRON 广播错误码 %s: %w", code, ErrBroadcastRejected)
	}
	if result.TransactionID != transaction.ID {
		return ErrInvalidResponse
	}
	return nil
}
