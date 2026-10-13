package nodehttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
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

// WriterConfig 是 TRON FullNode 写接口配置。BaseURL 可按优先级提供逗号分隔的同网络端点。
type WriterConfig struct {
	BaseURL          string
	APIKey           string
	MaxResponseBytes int64
	Observer         Observer
}

// Writer 只向 FullNode 广播已经签名的原始交易，不构造或签署交易。
type Writer struct {
	endpoints        []string
	apiKey           string
	maxResponseBytes int64
	httpClient       *http.Client
	observer         Observer
	preferred        atomic.Uint32
	selectionMu      sync.Mutex
}

var _ tron.Broadcaster = (*Writer)(nil)

// NewWriter 创建 TRON FullNode 广播客户端。
func NewWriter(config WriterConfig, httpClient *http.Client) (*Writer, error) {
	baseURLs, err := parseBaseURLs(config.BaseURL)
	if err != nil || config.MaxResponseBytes < 0 {
		return nil, ErrInvalidWriterConfig
	}
	maxResponseBytes := config.MaxResponseBytes
	if maxResponseBytes == 0 {
		maxResponseBytes = defaultWriterMaxResponseBytes
	}
	if maxResponseBytes < 1024 || maxResponseBytes > 1<<20 {
		return nil, ErrInvalidWriterConfig
	}
	endpoints := make([]string, 0, len(baseURLs))
	for _, baseURL := range baseURLs {
		endpoint := *baseURL
		endpoint.Path += "/wallet/broadcasttransaction"
		endpoint.RawPath = ""
		endpoints = append(endpoints, endpoint.String())
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	configuredHTTPClient := *httpClient
	configuredHTTPClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	observer := observerOrNoop(config.Observer)
	observer.Configured(OperationBroadcast, len(endpoints))
	return &Writer{
		endpoints: endpoints, apiKey: strings.TrimSpace(config.APIKey),
		maxResponseBytes: maxResponseBytes, httpClient: &configuredHTTPClient, observer: observer,
	}, nil
}

// Broadcast 将完全相同的签名交易提交给 FullNode。返回 nil 仅表示节点此次调用未报告错误。
func (writer *Writer) Broadcast(ctx context.Context, transaction tron.SignedTransaction) error {
	if err := tron.ValidateSignedTransaction(transaction); err != nil {
		return err
	}
	start := int(writer.preferred.Load()) % len(writer.endpoints)
	failures := make([]error, 0, len(writer.endpoints))
	for offset := range writer.endpoints {
		index := (start + offset) % len(writer.endpoints)
		retryable, reason, err := writer.broadcastToEndpoint(ctx, writer.endpoints[index], transaction)
		if err == nil {
			writer.selectPreferred(index)
			return nil
		}
		if reason != "" {
			writer.observer.Failed(OperationBroadcast, index, reason)
		}
		failures = append(failures, fmt.Errorf("TRON 广播端点 %d: %w", index+1, err))
		if !retryable || ctx.Err() != nil {
			return failures[len(failures)-1]
		}
	}
	writer.observer.Exhausted(OperationBroadcast)
	return fmt.Errorf("所有 TRON 广播端点均不可用: %w", errors.Join(failures...))
}

func (writer *Writer) selectPreferred(index int) {
	writer.selectionMu.Lock()
	defer writer.selectionMu.Unlock()
	previous := int(writer.preferred.Load()) % len(writer.endpoints)
	if previous == index {
		return
	}
	writer.preferred.Store(uint32(index))
	writer.observer.Switched(OperationBroadcast, previous, index)
}

func (writer *Writer) broadcastToEndpoint(
	ctx context.Context,
	endpoint string,
	transaction tron.SignedTransaction,
) (bool, string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(transaction.Payload))
	if err != nil {
		return false, FailureResponseInvalid, fmt.Errorf("创建 TRON 广播请求: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if writer.apiKey != "" {
		request.Header.Set("TRON-PRO-API-KEY", writer.apiKey)
	}
	response, err := writer.httpClient.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return false, "", fmt.Errorf("调用 TRON 广播接口: %w", err)
		}
		return true, FailureTransport, fmt.Errorf("调用 TRON 广播接口: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, writer.maxResponseBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return false, "", fmt.Errorf("读取 TRON 广播响应: %w", err)
		}
		return true, FailureResponseRead, fmt.Errorf("读取 TRON 广播响应: %w", err)
	}
	if int64(len(responseBody)) > writer.maxResponseBytes {
		return true, FailureResponseTooLarge, ErrResponseTooLarge
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		retryable := retryableHTTPStatus(response.StatusCode)
		reason := FailureRequestRejected
		if retryable {
			reason = FailureHTTPRetryable
		}
		return retryable, reason,
			fmt.Errorf("TRON 广播接口返回 HTTP %d: %w", response.StatusCode, ErrBroadcastRejected)
	}
	var result struct {
		Accepted      *bool  `json:"result"`
		TransactionID string `json:"txid"`
		Code          string `json:"code"`
	}
	decoder := json.NewDecoder(bytes.NewReader(responseBody))
	if err := decoder.Decode(&result); err != nil || result.Accepted == nil {
		return true, FailureResponseInvalid, ErrInvalidResponse
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return true, FailureResponseInvalid, ErrInvalidResponse
	}
	if !*result.Accepted {
		code := strings.TrimSpace(result.Code)
		if code == "" {
			return false, FailureRequestRejected, ErrBroadcastRejected
		}
		return false, FailureRequestRejected, fmt.Errorf("TRON 广播错误码 %s: %w", code, ErrBroadcastRejected)
	}
	if result.TransactionID != transaction.ID {
		return true, FailureResponseInvalid, ErrInvalidResponse
	}
	return false, "", nil
}
