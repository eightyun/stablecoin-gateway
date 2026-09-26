package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	stdhttp "net/http"
	"strings"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/deposit"
	"github.com/eightyun/stablecoin-gateway/internal/identity"
	"github.com/eightyun/stablecoin-gateway/internal/merchantauth"
	"github.com/eightyun/stablecoin-gateway/internal/payout"
)

var ErrInvalidHandlerConfig = errors.New("HTTP Handler 配置无效")

const idempotencyHeader = "Idempotency-Key"

// DepositService 定义商户 HTTP API 需要的充值能力。
type DepositService interface {
	CreateIntentFromPool(context.Context, deposit.PoolIntentRequest) (deposit.PoolIntentResult, error)
	GetIntent(context.Context, string, string) (deposit.IntentDetails, error)
	ListBalances(context.Context, string) ([]deposit.Balance, error)
}

// PayoutService 定义商户 HTTP API 需要的出款能力。
type PayoutService interface {
	Create(context.Context, payout.Request) (payout.CreateResult, error)
	Get(context.Context, string, string) (payout.Details, error)
}

// NewHandler 创建 HTTP 路由。
func NewHandler(
	authenticator merchantauth.RequestAuthenticator,
	deposits DepositService,
	payouts PayoutService,
	maxRequestBodyBytes int64,
) (stdhttp.Handler, error) {
	if authenticator == nil || deposits == nil || payouts == nil || maxRequestBodyBytes <= 0 {
		return nil, ErrInvalidHandlerConfig
	}
	handler := &merchantHandler{
		authenticator: authenticator, deposits: deposits, payouts: payouts,
		maxRequestBodyBytes: maxRequestBodyBytes,
	}
	mux := stdhttp.NewServeMux()
	mux.HandleFunc("GET /healthz", health)
	mux.HandleFunc("POST /v1/deposits", handler.createDeposit)
	mux.HandleFunc("GET /v1/deposits/{id}", handler.getDeposit)
	mux.HandleFunc("GET /v1/balances", handler.listBalances)
	mux.HandleFunc("POST /v1/payouts", handler.createPayout)
	mux.HandleFunc("GET /v1/payouts/{id}", handler.getPayout)
	return mux, nil
}

type merchantHandler struct {
	authenticator       merchantauth.RequestAuthenticator
	deposits            DepositService
	payouts             PayoutService
	maxRequestBodyBytes int64
}

type createDepositRequest struct {
	MerchantReference string `json:"merchant_reference"`
	AssetID           string `json:"asset_id"`
	Amount            string `json:"amount"`
	ExpiresInSeconds  int64  `json:"expires_in_seconds"`
}

type createPayoutRequest struct {
	MerchantReference  string `json:"merchant_reference"`
	AssetID            string `json:"asset_id"`
	DestinationAddress string `json:"destination_address"`
	Amount             string `json:"amount"`
}

func (handler *merchantHandler) createDeposit(writer stdhttp.ResponseWriter, request *stdhttp.Request) {
	body, principal, ok := handler.authenticate(writer, request)
	if !ok {
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		writeError(writer, stdhttp.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type 必须是 application/json")
		return
	}
	var input createDepositRequest
	if err := decodeJSON(body, &input); err != nil {
		writeError(writer, stdhttp.StatusBadRequest, "invalid_request", "请求 JSON 无效")
		return
	}
	idempotencyKey := strings.TrimSpace(request.Header.Get(idempotencyHeader))
	if input.ExpiresInSeconds < 60 || input.ExpiresInSeconds > 86_400 {
		writeError(writer, stdhttp.StatusBadRequest, "invalid_deposit", "充值订单参数无效")
		return
	}
	intentID, err := identity.NewUUID()
	if err != nil {
		writeInternalError(writer, err)
		return
	}
	result, err := handler.deposits.CreateIntentFromPool(request.Context(), deposit.PoolIntentRequest{
		ID: intentID, MerchantID: principal.MerchantID, AssetID: strings.TrimSpace(input.AssetID),
		IdempotencyKey: idempotencyKey, MerchantReference: strings.TrimSpace(input.MerchantReference),
		ExpectedAmount: input.Amount, ExpiresIn: time.Duration(input.ExpiresInSeconds) * time.Second,
	})
	if err != nil {
		writeDepositError(writer, err)
		return
	}
	status := stdhttp.StatusOK
	if result.Created {
		status = stdhttp.StatusCreated
	}
	writeJSON(writer, status, map[string]any{"data": result.Intent})
}

func (handler *merchantHandler) getDeposit(writer stdhttp.ResponseWriter, request *stdhttp.Request) {
	body, principal, ok := handler.authenticate(writer, request)
	if !ok {
		return
	}
	if len(body) != 0 {
		writeError(writer, stdhttp.StatusBadRequest, "invalid_request", "GET 请求不能包含请求体")
		return
	}
	intentID := strings.TrimSpace(request.PathValue("id"))
	if !identity.ValidUUID(intentID) {
		writeError(writer, stdhttp.StatusBadRequest, "invalid_deposit_id", "充值订单 ID 无效")
		return
	}
	result, err := handler.deposits.GetIntent(request.Context(), principal.MerchantID, intentID)
	if err != nil {
		writeDepositError(writer, err)
		return
	}
	writeJSON(writer, stdhttp.StatusOK, map[string]any{"data": result})
}

func (handler *merchantHandler) listBalances(writer stdhttp.ResponseWriter, request *stdhttp.Request) {
	body, principal, ok := handler.authenticate(writer, request)
	if !ok {
		return
	}
	if len(body) != 0 {
		writeError(writer, stdhttp.StatusBadRequest, "invalid_request", "GET 请求不能包含请求体")
		return
	}
	balances, err := handler.deposits.ListBalances(request.Context(), principal.MerchantID)
	if err != nil {
		writeInternalError(writer, err)
		return
	}
	writeJSON(writer, stdhttp.StatusOK, map[string]any{"data": balances})
}

func (handler *merchantHandler) createPayout(writer stdhttp.ResponseWriter, request *stdhttp.Request) {
	body, principal, ok := handler.authenticate(writer, request)
	if !ok {
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		writeError(writer, stdhttp.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type 必须是 application/json")
		return
	}
	var input createPayoutRequest
	if err := decodeJSON(body, &input); err != nil {
		writeError(writer, stdhttp.StatusBadRequest, "invalid_request", "请求 JSON 无效")
		return
	}
	payoutID, err := identity.NewUUID()
	if err != nil {
		writeInternalError(writer, err)
		return
	}
	result, err := handler.payouts.Create(request.Context(), payout.Request{
		ID: payoutID, MerchantID: principal.MerchantID, AssetID: strings.TrimSpace(input.AssetID),
		IdempotencyKey:     strings.TrimSpace(request.Header.Get(idempotencyHeader)),
		MerchantReference:  strings.TrimSpace(input.MerchantReference),
		DestinationAddress: strings.TrimSpace(input.DestinationAddress), Amount: strings.TrimSpace(input.Amount),
	})
	if err != nil {
		writePayoutError(writer, err)
		return
	}
	status := stdhttp.StatusOK
	if result.Created {
		status = stdhttp.StatusCreated
	}
	writeJSON(writer, status, map[string]any{"data": result.Payout})
}

func (handler *merchantHandler) getPayout(writer stdhttp.ResponseWriter, request *stdhttp.Request) {
	body, principal, ok := handler.authenticate(writer, request)
	if !ok {
		return
	}
	if len(body) != 0 {
		writeError(writer, stdhttp.StatusBadRequest, "invalid_request", "GET 请求不能包含请求体")
		return
	}
	payoutID := strings.TrimSpace(request.PathValue("id"))
	if !identity.ValidUUID(payoutID) {
		writeError(writer, stdhttp.StatusBadRequest, "invalid_payout_id", "出款单 ID 无效")
		return
	}
	result, err := handler.payouts.Get(request.Context(), principal.MerchantID, payoutID)
	if err != nil {
		writePayoutError(writer, err)
		return
	}
	writeJSON(writer, stdhttp.StatusOK, map[string]any{"data": result})
}

func (handler *merchantHandler) authenticate(
	writer stdhttp.ResponseWriter,
	request *stdhttp.Request,
) ([]byte, merchantauth.Principal, bool) {
	body, err := io.ReadAll(io.LimitReader(request.Body, handler.maxRequestBodyBytes+1))
	if err != nil {
		writeError(writer, stdhttp.StatusBadRequest, "invalid_request", "无法读取请求体")
		return nil, merchantauth.Principal{}, false
	}
	if int64(len(body)) > handler.maxRequestBodyBytes {
		writeError(writer, stdhttp.StatusRequestEntityTooLarge, "request_too_large", "请求体过大")
		return nil, merchantauth.Principal{}, false
	}
	principal, err := handler.authenticator.Authenticate(request.Context(), request, body)
	if errors.Is(err, merchantauth.ErrUnauthorized) {
		writer.Header().Set("WWW-Authenticate", "HMAC")
		writeError(writer, stdhttp.StatusUnauthorized, "unauthorized", "请求鉴权失败")
		return nil, merchantauth.Principal{}, false
	}
	if err != nil {
		writeInternalError(writer, err)
		return nil, merchantauth.Principal{}, false
	}
	return body, principal, true
}

func health(writer stdhttp.ResponseWriter, _ *stdhttp.Request) {
	writeJSON(writer, stdhttp.StatusOK, map[string]string{"status": "ok"})
}

func decodeJSON(body []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("JSON 包含尾随内容")
	}
	return nil
}

func writeDepositError(writer stdhttp.ResponseWriter, err error) {
	switch {
	case errors.Is(err, deposit.ErrInvalidPoolIntent):
		writeError(writer, stdhttp.StatusBadRequest, "invalid_deposit", "充值订单参数无效")
	case errors.Is(err, deposit.ErrIntentConflict):
		writeError(writer, stdhttp.StatusConflict, "idempotency_conflict", "幂等键已被不同请求使用")
	case errors.Is(err, deposit.ErrAddressPoolEmpty):
		writeError(writer, stdhttp.StatusConflict, "address_pool_empty", "暂无可用充值地址")
	case errors.Is(err, deposit.ErrIntentNotFound):
		writeError(writer, stdhttp.StatusNotFound, "deposit_not_found", "充值订单不存在")
	default:
		writeInternalError(writer, err)
	}
}

func writePayoutError(writer stdhttp.ResponseWriter, err error) {
	switch {
	case errors.Is(err, payout.ErrInvalidRequest), errors.Is(err, payout.ErrAssetUnavailable),
		errors.Is(err, payout.ErrUnsupportedNetwork):
		writeError(writer, stdhttp.StatusBadRequest, "invalid_payout", "出款参数无效")
	case errors.Is(err, payout.ErrPayoutConflict):
		writeError(writer, stdhttp.StatusConflict, "idempotency_conflict", "幂等键已被不同请求使用")
	case errors.Is(err, payout.ErrInsufficientBalance):
		writeError(writer, stdhttp.StatusUnprocessableEntity, "insufficient_balance", "可用余额不足")
	case errors.Is(err, payout.ErrMerchantUnavailable), errors.Is(err, payout.ErrLedgerUnavailable):
		writeError(writer, stdhttp.StatusConflict, "payout_unavailable", "当前无法创建出款")
	case errors.Is(err, payout.ErrPayoutNotFound):
		writeError(writer, stdhttp.StatusNotFound, "payout_not_found", "出款单不存在")
	default:
		writeInternalError(writer, err)
	}
}

func writeInternalError(writer stdhttp.ResponseWriter, err error) {
	slog.Error("商户 API 请求失败", "error", err)
	writeError(writer, stdhttp.StatusInternalServerError, "internal_error", "服务暂时不可用")
}

func writeError(writer stdhttp.ResponseWriter, status int, code, message string) {
	writeJSON(writer, status, map[string]any{
		"error": map[string]string{"code": code, "message": message},
	})
}

func writeJSON(writer stdhttp.ResponseWriter, status int, value any) {
	encoded, err := json.Marshal(value)
	if err != nil {
		slog.Error("编码 HTTP 响应失败", "error", err)
		stdhttp.Error(writer, "internal server error", stdhttp.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	if _, err := writer.Write(append(encoded, '\n')); err != nil {
		slog.Error("写入 HTTP 响应失败", "error", fmt.Errorf("写入响应: %w", err))
	}
}
