package signer

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"

	"github.com/eightyun/stablecoin-gateway/internal/chain/tron"
)

// KMSAcceptanceResult 保存不包含密钥、地址或签名原文的验收证据。
type KMSAcceptanceResult struct {
	AddressFingerprint string `json:"address_fingerprint"`
	AddressMatched     bool   `json:"address_matched"`
	SignatureVerified  bool   `json:"signature_verified"`
}

// CheckAcceptance 绑定预期地址，并按显式选择签署随机域分离挑战。
// 挑战不能由调用方提供，避免验收接口被用作任意交易签名入口。
func (key *AWSKMSKey) CheckAcceptance(ctx context.Context, expectedAddress string, sign bool) (KMSAcceptanceResult, error) {
	if err := ctx.Err(); err != nil {
		return KMSAcceptanceResult{}, err
	}
	if key == nil || key.client == nil {
		return KMSAcceptanceResult{}, ErrInvalidKMSConfig
	}
	address, err := tron.NormalizeAddressBase58(expectedAddress)
	if err != nil || address != key.address {
		return KMSAcceptanceResult{}, ErrPolicyDenied
	}
	fingerprint := sha256.Sum256([]byte(address))
	result := KMSAcceptanceResult{
		AddressFingerprint: hex.EncodeToString(fingerprint[:]), AddressMatched: true,
	}
	if !sign {
		return result, nil
	}
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return KMSAcceptanceResult{}, err
	}
	challenge := append([]byte("stablecoin-gateway/kms-acceptance/v1\x00"), nonce...)
	digest := sha256.Sum256(challenge)
	signature, err := key.SignDigest(ctx, digest[:])
	if err != nil {
		return KMSAcceptanceResult{}, err
	}
	defer zero(signature)
	if err := verifyDigestSignature(digest[:], signature, address); err != nil {
		return KMSAcceptanceResult{}, ErrInvalidKMSSignature
	}
	result.SignatureVerified = true
	return result, nil
}
