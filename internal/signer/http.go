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

	"github.com/eightyun/stablecoin-gateway/internal/chain/tron"
)

const defaultMaxRequestBodyBytes int64 = 64 << 10

// HTTPConfig 定义 signer HTTP 边界。
type HTTPConfig struct {
	BearerToken         string
	MaxRequestBodyBytes int64
}

type transferSigner interface {
	SignTransfer(context.Context, tron.TransferSignRequest) (tron.SignedTransaction, error)
}

type httpHandler struct {
	signer              transferSigner
	authorization       string
	maxRequestBodyBytes int64
}

// NewHTTPHandler 创建仅暴露单一签名端点的 HTTP Handler。
func NewHTTPHandler(config HTTPConfig, service transferSigner) (http.Handler, error) {
	token := strings.TrimSpace(config.BearerToken)
	if service == nil || token == "" || len(token) > 4096 || config.MaxRequestBodyBytes < 0 {
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
	return &httpHandler{
		signer: service, authorization: "Bearer " + token, maxRequestBodyBytes: maxBytes,
	}, nil
}

func (handler *httpHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	if request.URL.Path != "/v1/tron/transfers:sign" || request.URL.RawQuery != "" {
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
	if mediaType := strings.TrimSpace(strings.Split(request.Header.Get("Content-Type"), ";")[0]); mediaType != "application/json" {
		writeError(response, http.StatusUnsupportedMediaType, "unsupported_media_type")
		return
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, handler.maxRequestBodyBytes+1))
	if err != nil || int64(len(body)) > handler.maxRequestBodyBytes {
		writeError(response, http.StatusRequestEntityTooLarge, "request_too_large")
		return
	}
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
	if err != nil {
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
		return
	}
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
