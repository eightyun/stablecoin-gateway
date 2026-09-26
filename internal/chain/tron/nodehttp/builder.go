package nodehttp

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/chain/tron"
)

var (
	ErrInvalidBuilderConfig = errors.New("TRON 交易构造器配置无效")
	ErrBuildRejected        = errors.New("TRON FullNode 拒绝构造交易")
)

// BuilderConfig 约束 FullNode 构造的 TRC20 转账。
type BuilderConfig struct {
	BaseURL                string
	Network                string
	APIKey                 string
	OwnerAddress           string
	FeeLimit               int64
	MaxTransactionLifetime time.Duration
	MaxResponseBytes       int64
}

// Builder 通过 FullNode 构造交易，并在交给密钥前验证完整语义。
type Builder struct {
	client                 *Client
	network                string
	ownerAddress           string
	feeLimit               int64
	maxTransactionLifetime time.Duration
}

var _ tron.TransferBuilder = (*Builder)(nil)

// NewBuilder 创建受约束的 TRC20 交易构造器。
func NewBuilder(config BuilderConfig, client *http.Client) (*Builder, error) {
	if config.Network != "tron-nile" && config.Network != "tron-shasta" && config.Network != "tron-mainnet" {
		return nil, ErrInvalidBuilderConfig
	}
	ownerAddress, err := tron.NormalizeAddressBase58(config.OwnerAddress)
	if err != nil || config.FeeLimit <= 0 || config.MaxTransactionLifetime <= 15*time.Second {
		return nil, ErrInvalidBuilderConfig
	}
	nodeClient, err := New(Config{
		BaseURL: config.BaseURL, Network: config.Network,
		APIKey: config.APIKey, MaxResponseBytes: config.MaxResponseBytes,
	}, client)
	if err != nil {
		return nil, ErrInvalidBuilderConfig
	}
	return &Builder{
		client: nodeClient, network: config.Network, ownerAddress: ownerAddress,
		feeLimit: config.FeeLimit, maxTransactionLifetime: config.MaxTransactionLifetime,
	}, nil
}

// BuildTransfer 从 FullNode 构造并验证一笔未签名 TRC20 transfer。
func (builder *Builder) BuildTransfer(ctx context.Context, request tron.TransferSignRequest) (tron.UnsignedTransaction, error) {
	request.Network = strings.TrimSpace(request.Network)
	request.ContractAddress = strings.TrimSpace(request.ContractAddress)
	request.DestinationAddress = strings.TrimSpace(request.DestinationAddress)
	request.Amount = strings.TrimSpace(request.Amount)
	if request.Network != builder.network {
		return tron.UnsignedTransaction{}, ErrBuildRejected
	}
	contractAddress, err := tron.NormalizeAddressBase58(request.ContractAddress)
	if err != nil {
		return tron.UnsignedTransaction{}, ErrBuildRejected
	}
	destinationAddress, err := tron.NormalizeAddressBase58(request.DestinationAddress)
	if err != nil {
		return tron.UnsignedTransaction{}, ErrBuildRejected
	}
	amount, ok := new(big.Int).SetString(request.Amount, 10)
	if !ok || amount.Sign() <= 0 || amount.BitLen() > 256 || amount.Text(10) != request.Amount {
		return tron.UnsignedTransaction{}, ErrBuildRejected
	}
	destinationHex, _ := tron.NormalizeAddressHex(destinationAddress)
	parameter := make([]byte, 64)
	destinationBytes, _ := hex.DecodeString(destinationHex[2:])
	copy(parameter[12:32], destinationBytes)
	amount.FillBytes(parameter[32:])
	payload := struct {
		OwnerAddress    string `json:"owner_address"`
		ContractAddress string `json:"contract_address"`
		Function        string `json:"function_selector"`
		Parameter       string `json:"parameter"`
		FeeLimit        int64  `json:"fee_limit"`
		CallValue       int64  `json:"call_value"`
		Visible         bool   `json:"visible"`
	}{
		OwnerAddress: builder.ownerAddress, ContractAddress: contractAddress,
		Function: "transfer(address,uint256)", Parameter: hex.EncodeToString(parameter),
		FeeLimit: builder.feeLimit, Visible: true,
	}
	var response struct {
		Result struct {
			Accepted *bool `json:"result"`
		} `json:"result"`
		Transaction json.RawMessage `json:"transaction"`
	}
	if err := builder.client.post(ctx, "/wallet/triggersmartcontract", payload, &response); err != nil {
		return tron.UnsignedTransaction{}, err
	}
	if response.Result.Accepted == nil || !*response.Result.Accepted || len(response.Transaction) == 0 {
		return tron.UnsignedTransaction{}, ErrBuildRejected
	}
	var transactionFields map[string]json.RawMessage
	if json.Unmarshal(response.Transaction, &transactionFields) != nil || transactionFields["signature"] != nil {
		return tron.UnsignedTransaction{}, ErrInvalidResponse
	}
	var identity struct {
		ID         string `json:"txID"`
		RawDataHex string `json:"raw_data_hex"`
	}
	if json.Unmarshal(response.Transaction, &identity) != nil {
		return tron.UnsignedTransaction{}, ErrInvalidResponse
	}
	rawData, err := hex.DecodeString(identity.RawDataHex)
	if err != nil || len(rawData) == 0 {
		return tron.UnsignedTransaction{}, ErrInvalidResponse
	}
	dummySignature, _ := json.Marshal([]string{strings.Repeat("00", 65)})
	transactionFields["signature"] = dummySignature
	validationPayload, err := json.Marshal(transactionFields)
	if err != nil {
		return tron.UnsignedTransaction{}, ErrInvalidResponse
	}
	now := time.Now().UTC()
	if err := tron.ValidateSignedTransferTransaction(tron.SignedTransaction{
		ID: identity.ID, Payload: validationPayload,
	}, tron.TransferTransactionExpectation{
		OwnerAddress: builder.ownerAddress, ContractAddress: contractAddress,
		DestinationAddress: destinationAddress, Amount: request.Amount, Now: now,
		MaxFeeLimit: builder.feeLimit, MaxLifetime: builder.maxTransactionLifetime,
		MinRemainingLifetime: 15 * time.Second, MaxFutureSkew: 30 * time.Second,
	}); err != nil {
		return tron.UnsignedTransaction{}, fmt.Errorf("FullNode 构造交易不符合请求: %w", ErrInvalidResponse)
	}
	return tron.UnsignedTransaction{
		ID: identity.ID, Payload: append([]byte(nil), response.Transaction...), RawData: rawData,
	}, nil
}
