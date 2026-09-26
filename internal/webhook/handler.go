package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/outbox"
	"github.com/eightyun/stablecoin-gateway/internal/secretbox"
)

var ErrInvalidHandlerConfig = errors.New("Webhook Handler 配置无效")

// Sender 执行一次已签名的 HTTP 投递。
type Sender interface {
	Send(ctx context.Context, request DeliveryRequest) DeliveryResult
}

// DeliveryRequest 是 Sender 所需的投递数据。
type DeliveryRequest struct {
	EventID string
	Topic   string
	URL     string
	Body    []byte
	Secret  []byte
}

// DeliveryResult 是一次 HTTP 投递结果。
type DeliveryResult struct {
	RequestTimestamp int64
	ResponseStatus   *int
	Duration         time.Duration
	Err              error
}

// Handler 将 Outbox 事件投递到商户的所有活动端点。
type Handler struct {
	store   DeliveryStore
	keyring *secretbox.Keyring
	sender  Sender
}

// NewHandler 创建 Webhook Handler。
func NewHandler(store DeliveryStore, keyring *secretbox.Keyring, sender Sender) (*Handler, error) {
	if store == nil || keyring == nil || sender == nil {
		return nil, ErrInvalidHandlerConfig
	}
	return &Handler{store: store, keyring: keyring, sender: sender}, nil
}

// Handle 实现 Outbox Handler。
func (handler *Handler) Handle(ctx context.Context, event outbox.Event) error {
	var routing struct {
		MerchantID string `json:"merchant_id"`
	}
	if !json.Valid(event.Payload) || json.Unmarshal(event.Payload, &routing) != nil || routing.MerchantID == "" {
		return errors.New("Webhook 事件缺少有效商户信息")
	}
	envelope, err := json.Marshal(struct {
		ID        string          `json:"id"`
		Type      string          `json:"type"`
		CreatedAt time.Time       `json:"created_at"`
		Data      json.RawMessage `json:"data"`
	}{ID: event.ID, Type: event.Topic, CreatedAt: event.CreatedAt.UTC(), Data: event.Payload})
	if err != nil {
		return fmt.Errorf("编码 Webhook 事件信封: %w", err)
	}
	endpoints, err := handler.store.PendingEndpoints(ctx, event.ID, routing.MerchantID)
	if err != nil {
		return err
	}
	var deliveryErrors []error
	for _, endpoint := range endpoints {
		secret, decryptErr := handler.keyring.Decrypt(endpointAAD(endpoint.ID), endpoint.Encrypted)
		if decryptErr != nil {
			deliveryErrors = append(deliveryErrors, fmt.Errorf("解密 Webhook 端点 %s: %w", endpoint.ID, decryptErr))
			continue
		}
		result := handler.sender.Send(ctx, DeliveryRequest{
			EventID: event.ID, Topic: event.Topic, URL: endpoint.URL, Body: envelope, Secret: secret,
		})
		clear(secret)
		errorText := ""
		if result.Err != nil {
			errorText = result.Err.Error()
		}
		recordErr := handler.store.RecordAttempt(ctx, Attempt{
			EventID: event.ID, EndpointID: endpoint.ID, Attempt: event.Attempts,
			RequestTimestamp: result.RequestTimestamp, ResponseStatus: result.ResponseStatus,
			Error: errorText, Duration: result.Duration,
		})
		if result.Err != nil || recordErr != nil {
			deliveryErrors = append(deliveryErrors, errors.Join(result.Err, recordErr))
		}
	}
	return errors.Join(deliveryErrors...)
}
