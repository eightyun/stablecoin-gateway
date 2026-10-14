package signer

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/chain/tron"
	"github.com/eightyun/stablecoin-gateway/internal/identity"
)

const defaultMaxRequestBodyBytes int64 = 64 << 10

// HTTPConfig 定义 signer HTTP 边界。
type HTTPConfig struct {
	BearerToken         string
	MaxRequestBodyBytes int64
	RequestTimeout      time.Duration
	Observer            HTTPObserver
}

// HTTPResult 保存可观测请求结果，不包含资金明细或签名原文。
type HTTPResult struct {
	Operation     string
	Result        string
	RequestID     string
	TransactionID string
	Duration      time.Duration
}

// HTTPObserver 接收每次 HTTP 请求的固定类别与脱敏结果。
type HTTPObserver interface {
	Completed(HTTPResult)
}

type transferSigner interface {
	SignTransfer(context.Context, tron.TransferSignRequest) (tron.SignedTransaction, error)
}

type sweepSigner interface {
	SignSweep(context.Context, tron.SweepSignRequest) (tron.SignedTransaction, error)
}

type httpHandler struct {
	signer              transferSigner
	sweepSigner         sweepSigner
	authorization       string
	maxRequestBodyBytes int64
	observer            HTTPObserver
}

// NewHTTPHandler 创建受鉴权保护的转账与归集签名 HTTP Handler。
func NewHTTPHandler(config HTTPConfig, service transferSigner) (http.Handler, error) {
	token := strings.TrimSpace(config.BearerToken)
	if service == nil || token == "" || len(token) > 4096 || config.MaxRequestBodyBytes < 0 || config.RequestTimeout < 0 {
		return nil, ErrInvalidConfig
	}
	for _, character := range token {
		if character <= 0x20 || character == 0x7f {
			return nil, ErrInvalidConfig
		}
	}
	maxBytes := config.MaxRequestBodyBytes
	if maxBytes == 0 {
		maxBytes = defaultMaxRequestBodyBytes
	}
	if maxBytes < 1024 || maxBytes > 1<<20 {
		return nil, ErrInvalidConfig
	}
	sweepService, _ := service.(sweepSigner)
	var handler http.Handler = &httpHandler{
		signer: service, authorization: "Bearer " + token, maxRequestBodyBytes: maxBytes,
		sweepSigner: sweepService, observer: config.Observer,
	}
	if config.RequestTimeout > 0 {
		handler = http.TimeoutHandler(handler, config.RequestTimeout, `{"error":"signing_timeout"}`)
	}
	return handler, nil
}

func (handler *httpHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	started := time.Now()
	event := HTTPResult{Operation: "unknown"}
	switch request.URL.Path {
	case "/v1/tron/transfers:sign":
		event.Operation = "transfer"
	case "/v1/tron/sweeps:sign":
		event.Operation = "sweep"
	}
	recorded := &statusWriter{ResponseWriter: response, status: http.StatusOK}
	response = recorded
	defer func() {
		if handler.observer == nil {
			return
		}
		event.Duration = time.Since(started)
		event.Result = httpResult(recorded.status)
		if errors.Is(request.Context().Err(), context.DeadlineExceeded) {
			event.Result = "timeout"
		} else if errors.Is(request.Context().Err(), context.Canceled) {
			event.Result = "canceled"
		}
		handler.observer.Completed(event)
	}()
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	if (request.URL.Path != "/v1/tron/transfers:sign" && request.URL.Path != "/v1/tron/sweeps:sign") ||
		request.URL.RawQuery != "" {
		writeError(response, http.StatusNotFound, "not_found")
		return
	}
	if request.Method != http.MethodPost {
		response.Header().Set("Allow", http.MethodPost)
		writeError(response, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	providedAuthorization := request.Header.Get("Authorization")
	if len(providedAuthorization) != len(handler.authorization) ||
		subtle.ConstantTimeCompare([]byte(providedAuthorization), []byte(handler.authorization)) != 1 {
		writeError(response, http.StatusUnauthorized, "unauthorized")
		return
	}
	if requestID := request.Header.Get("Idempotency-Key"); identity.ValidUUID(requestID) {
		event.RequestID = strings.ToLower(requestID)
	}
	if mediaType := strings.TrimSpace(strings.Split(request.Header.Get("Content-Type"), ";")[0]); mediaType != "application/json" {
		writeError(response, http.StatusUnsupportedMediaType, "unsupported_media_type")
		return
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, handler.maxRequestBodyBytes+1))
	if err != nil || int64(len(body)) > handler.maxRequestBodyBytes {
		writeError(response, http.StatusRequestEntityTooLarge, "request_too_large")
		return
	}
	if request.URL.Path == "/v1/tron/sweeps:sign" {
		handler.handleSweep(response, request, body, &event)
		return
	}
	handler.handleTransfer(response, request, body, &event)
}

func (handler *httpHandler) handleTransfer(response http.ResponseWriter, request *http.Request, body []byte, event *HTTPResult) {
	var input struct {
		RequestID          string `json:"request_id"`
		Network            string `json:"network"`
		ContractAddress    string `json:"contract_address"`
		DestinationAddress string `json:"destination_address"`
		Amount             string `json:"amount"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || ensureEOF(decoder) != nil ||
		request.Header.Get("Idempotency-Key") != input.RequestID {
		writeError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	transaction, err := handler.signer.SignTransfer(request.Context(), tron.TransferSignRequest{
		RequestID: input.RequestID, Network: input.Network, ContractAddress: input.ContractAddress,
		DestinationAddress: input.DestinationAddress, Amount: input.Amount,
	})
	if writeSigningError(response, err) {
		return
	}
	if validRequestHash(transaction.ID) {
		event.TransactionID = transaction.ID
	}
	writeTransaction(response, transaction)
}

func (handler *httpHandler) handleSweep(response http.ResponseWriter, request *http.Request, body []byte, event *HTTPResult) {
	if handler.sweepSigner == nil {
		writeError(response, http.StatusNotFound, "not_found")
		return
	}
	var input struct {
		RequestID          string `json:"request_id"`
		Network            string `json:"network"`
		SourceAddress      string `json:"source_address"`
		ContractAddress    string `json:"contract_address"`
		DestinationAddress string `json:"destination_address"`
		Amount             string `json:"amount"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || ensureEOF(decoder) != nil ||
		request.Header.Get("Idempotency-Key") != input.RequestID {
		writeError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	transaction, err := handler.sweepSigner.SignSweep(request.Context(), tron.SweepSignRequest{
		RequestID: input.RequestID, Network: input.Network, SourceAddress: input.SourceAddress,
		ContractAddress: input.ContractAddress, DestinationAddress: input.DestinationAddress,
		Amount: input.Amount,
	})
	if writeSigningError(response, err) {
		return
	}
	if validRequestHash(transaction.ID) {
		event.TransactionID = transaction.ID
	}
	writeTransaction(response, transaction)
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (writer *statusWriter) WriteHeader(status int) {
	writer.status = status
	writer.ResponseWriter.WriteHeader(status)
}

func httpResult(status int) string {
	switch status {
	case http.StatusOK:
		return "success"
	case http.StatusUnauthorized:
		return "unauthorized"
	case http.StatusConflict:
		return "conflict"
	case http.StatusUnprocessableEntity:
		return "policy_denied"
	case http.StatusInternalServerError:
		return "failed"
	default:
		return "invalid_request"
	}
}

func writeSigningError(response http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, ErrInvalidRequest):
		writeError(response, http.StatusBadRequest, "invalid_request")
	case errors.Is(err, ErrPolicyDenied):
		writeError(response, http.StatusUnprocessableEntity, "policy_denied")
	case errors.Is(err, ErrIdempotencyConflict):
		writeError(response, http.StatusConflict, "idempotency_conflict")
	default:
		writeError(response, http.StatusInternalServerError, "signing_failed")
	}
	return true
}

func writeTransaction(response http.ResponseWriter, transaction tron.SignedTransaction) {
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(struct {
		TransactionID     string          `json:"transaction_id"`
		SignedTransaction json.RawMessage `json:"signed_transaction"`
	}{
		TransactionID: transaction.ID, SignedTransaction: json.RawMessage(transaction.Payload),
	})
}

func writeError(response http.ResponseWriter, status int, code string) {
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(struct {
		Error string `json:"error"`
	}{Error: code})
}
