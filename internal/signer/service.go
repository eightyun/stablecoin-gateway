package signer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/chain/tron"
	"github.com/eightyun/stablecoin-gateway/internal/identity"
)

var (
	ErrInvalidConfig  = errors.New("测试网 signer 配置无效")
	ErrInvalidRequest = errors.New("测试网 signer 请求无效")
	ErrPolicyDenied   = errors.New("测试网 signer 策略拒绝")
)

// ServiceConfig 定义 signer 自身不可由调用方覆盖的资金安全边界。
type ServiceConfig struct {
	Network                string
	OwnerAddress           string
	AllowedContracts       []string
	MaxAmount              string
	MaxFeeLimit            int64
	MaxTransactionLifetime time.Duration
}

// Service 只签署符合固定网络、资产、金额和交易语义的测试网转账。
type Service struct {
	builder                tron.TransferBuilder
	key                    DigestSigner
	store                  *FileStore
	network                string
	ownerAddress           string
	allowedContracts       map[string]struct{}
	maxAmount              *big.Int
	maxFeeLimit            int64
	maxTransactionLifetime time.Duration
	now                    func() time.Time
}

var _ tron.TransferSigner = (*Service)(nil)

// NewService 创建测试网签名服务。文件密钥后端明确拒绝主网。
func NewService(config ServiceConfig, builder tron.TransferBuilder, key DigestSigner, store *FileStore) (*Service, error) {
	if config.Network != "tron-nile" && config.Network != "tron-shasta" || builder == nil || key == nil || store == nil ||
		config.MaxFeeLimit <= 0 || config.MaxTransactionLifetime <= 15*time.Second {
		return nil, ErrInvalidConfig
	}
	owner, err := tron.NormalizeAddressBase58(config.OwnerAddress)
	if err != nil || key.Address() != owner {
		return nil, ErrInvalidConfig
	}
	maxAmount, ok := new(big.Int).SetString(strings.TrimSpace(config.MaxAmount), 10)
	if !ok || maxAmount.Sign() <= 0 || maxAmount.BitLen() > 256 || maxAmount.Text(10) != config.MaxAmount {
		return nil, ErrInvalidConfig
	}
	allowed := make(map[string]struct{}, len(config.AllowedContracts))
	for _, value := range config.AllowedContracts {
		contract, normalizeErr := tron.NormalizeAddressBase58(value)
		if normalizeErr != nil {
			return nil, ErrInvalidConfig
		}
		allowed[contract] = struct{}{}
	}
	if len(allowed) == 0 {
		return nil, ErrInvalidConfig
	}
	return &Service{
		builder: builder, key: key, store: store, network: config.Network,
		ownerAddress: owner, allowedContracts: allowed, maxAmount: maxAmount,
		maxFeeLimit: config.MaxFeeLimit, maxTransactionLifetime: config.MaxTransactionLifetime,
		now: time.Now,
	}, nil
}

// SignTransfer 验证业务语义、执行持久化幂等，再构造并签署交易。
func (service *Service) SignTransfer(ctx context.Context, request tron.TransferSignRequest) (tron.SignedTransaction, error) {
	request.RequestID = strings.ToLower(strings.TrimSpace(request.RequestID))
	request.Network = strings.TrimSpace(request.Network)
	request.Amount = strings.TrimSpace(request.Amount)
	if !identity.ValidUUID(request.RequestID) || request.Network != service.network {
		return tron.SignedTransaction{}, ErrInvalidRequest
	}
	contract, err := tron.NormalizeAddressBase58(request.ContractAddress)
	if err != nil {
		return tron.SignedTransaction{}, ErrInvalidRequest
	}
	destination, err := tron.NormalizeAddressBase58(request.DestinationAddress)
	if err != nil {
		return tron.SignedTransaction{}, ErrInvalidRequest
	}
	amount, ok := new(big.Int).SetString(request.Amount, 10)
	if !ok || amount.Sign() <= 0 || amount.BitLen() > 256 || amount.Text(10) != request.Amount {
		return tron.SignedTransaction{}, ErrInvalidRequest
	}
	if _, ok := service.allowedContracts[contract]; !ok || amount.Cmp(service.maxAmount) > 0 {
		return tron.SignedTransaction{}, ErrPolicyDenied
	}
	request.ContractAddress = contract
	request.DestinationAddress = destination
	fingerprintPayload, _ := json.Marshal(request)
	fingerprint := sha256.Sum256(fingerprintPayload)
	return service.store.Resolve(ctx, request.RequestID, hex.EncodeToString(fingerprint[:]), func() (tron.SignedTransaction, error) {
		return service.buildAndSign(ctx, request)
	})
}

func (service *Service) buildAndSign(ctx context.Context, request tron.TransferSignRequest) (tron.SignedTransaction, error) {
	unsigned, err := service.builder.BuildTransfer(ctx, request)
	if err != nil {
		return tron.SignedTransaction{}, err
	}
	digest := sha256.Sum256(unsigned.RawData)
	if hex.EncodeToString(digest[:]) != unsigned.ID {
		return tron.SignedTransaction{}, ErrInvalidRequest
	}
	signature, err := service.key.SignDigest(digest[:])
	if err != nil {
		return tron.SignedTransaction{}, err
	}
	if err := verifyDigestSignature(digest[:], signature, service.ownerAddress); err != nil {
		return tron.SignedTransaction{}, err
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(unsigned.Payload, &envelope) != nil || envelope["signature"] != nil {
		return tron.SignedTransaction{}, ErrInvalidRequest
	}
	encodedSignature, _ := json.Marshal([]string{hex.EncodeToString(signature)})
	envelope["signature"] = encodedSignature
	payload, err := json.Marshal(envelope)
	if err != nil {
		return tron.SignedTransaction{}, err
	}
	transaction := tron.SignedTransaction{ID: unsigned.ID, Payload: payload}
	if err := tron.ValidateSignedTransferTransaction(transaction, tron.TransferTransactionExpectation{
		OwnerAddress: service.ownerAddress, ContractAddress: request.ContractAddress,
		DestinationAddress: request.DestinationAddress, Amount: request.Amount,
		Now: service.now().UTC(), MaxFeeLimit: service.maxFeeLimit,
		MaxLifetime: service.maxTransactionLifetime, MinRemainingLifetime: 15 * time.Second,
		MaxFutureSkew: 30 * time.Second,
	}); err != nil {
		return tron.SignedTransaction{}, ErrInvalidRequest
	}
	return transaction, nil
}
