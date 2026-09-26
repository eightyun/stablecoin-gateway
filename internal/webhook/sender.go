package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"time"
)

const (
	HeaderEventID        = "X-Gateway-Event-ID"
	HeaderEventType      = "X-Gateway-Event-Type"
	HeaderEventTimestamp = "X-Gateway-Event-Timestamp"
	HeaderSignature      = "X-Gateway-Signature"
)

var ErrInvalidSenderConfig = errors.New("Webhook Sender 配置无效")

var protectedNetworkPrefixes = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001:db8::/32"),
}

// HTTPSenderConfig 控制 HTTP 超时、响应限制和目标网络范围。
type HTTPSenderConfig struct {
	Timeout              time.Duration
	MaxResponseBodyBytes int64
	AllowPrivateNetworks bool
}

// HTTPSender 执行带 SSRF 防护的 Webhook 请求。
type HTTPSender struct {
	client               *http.Client
	maxResponseBodyBytes int64
}

// NewHTTPSender 创建 Webhook HTTP Sender。
func NewHTTPSender(config HTTPSenderConfig) (*HTTPSender, error) {
	if config.Timeout <= 0 || config.MaxResponseBodyBytes <= 0 || config.MaxResponseBodyBytes > 1<<20 {
		return nil, ErrInvalidSenderConfig
	}
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           protectedDialContext(config.AllowPrivateNetworks),
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: config.Timeout,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}
	return &HTTPSender{
		client: &http.Client{
			Transport: transport,
			Timeout:   config.Timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		maxResponseBodyBytes: config.MaxResponseBodyBytes,
	}, nil
}

// Send 发送一次 Webhook；只有 2xx 响应视为成功。
func (sender *HTTPSender) Send(ctx context.Context, delivery DeliveryRequest) DeliveryResult {
	startedAt := time.Now()
	timestamp := startedAt.Unix()
	result := DeliveryResult{RequestTimestamp: timestamp}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, delivery.URL, bytes.NewReader(delivery.Body))
	if err != nil {
		result.Err = fmt.Errorf("创建 Webhook 请求: %w", err)
		result.Duration = time.Since(startedAt)
		return result
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "stablecoin-gateway-webhook/1")
	request.Header.Set(HeaderEventID, delivery.EventID)
	request.Header.Set(HeaderEventType, delivery.Topic)
	request.Header.Set(HeaderEventTimestamp, strconv.FormatInt(timestamp, 10))
	request.Header.Set(HeaderSignature, sign(timestamp, delivery.EventID, delivery.Body, delivery.Secret))
	response, err := sender.client.Do(request)
	if err != nil {
		var urlError *url.Error
		if errors.As(err, &urlError) {
			err = urlError.Err
		}
		result.Err = fmt.Errorf("发送 Webhook 请求: %w", err)
		result.Duration = time.Since(startedAt)
		return result
	}
	defer response.Body.Close()
	status := response.StatusCode
	result.ResponseStatus = &status
	_, readErr := io.Copy(io.Discard, io.LimitReader(response.Body, sender.maxResponseBodyBytes+1))
	result.Duration = time.Since(startedAt)
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		if readErr != nil {
			result.Err = fmt.Errorf("Webhook 返回 HTTP %d 且响应读取失败: %w", status, readErr)
		} else {
			result.Err = fmt.Errorf("Webhook 返回 HTTP %d", status)
		}
	}
	return result
}

func sign(timestamp int64, eventID string, body, secret []byte) string {
	digest := hmac.New(sha256.New, secret)
	_, _ = fmt.Fprintf(digest, "%d.%s.", timestamp, eventID)
	_, _ = digest.Write(body)
	return "v1=" + hex.EncodeToString(digest.Sum(nil))
}

func protectedDialContext(allowPrivate bool) func(context.Context, string, string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("解析 Webhook 目标地址: %w", err)
		}
		addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, fmt.Errorf("解析 Webhook 目标主机: %w", err)
		}
		if len(addresses) == 0 {
			return nil, errors.New("Webhook 目标主机没有可用地址")
		}
		for _, address := range addresses {
			if blockedAddress(address, allowPrivate) {
				return nil, errors.New("Webhook 目标解析到受保护网络")
			}
		}
		var lastErr error
		for _, resolved := range addresses {
			connection, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(resolved.String(), port))
			if dialErr == nil {
				return connection, nil
			}
			lastErr = dialErr
		}
		return nil, fmt.Errorf("连接 Webhook 目标: %w", lastErr)
	}
}

func blockedAddress(address netip.Addr, allowPrivate bool) bool {
	address = address.Unmap()
	if !address.IsGlobalUnicast() || address.IsLoopback() || address.IsLinkLocalUnicast() ||
		address.IsLinkLocalMulticast() || address.IsUnspecified() {
		return true
	}
	if allowPrivate {
		return false
	}
	if address.IsPrivate() {
		return true
	}
	for _, prefix := range protectedNetworkPrefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}
