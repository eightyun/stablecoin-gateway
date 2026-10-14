package signer

import (
	"context"
	"encoding/asn1"
	"errors"
	"math/big"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/eightyun/stablecoin-gateway/internal/chain/tron"
)

var (
	ErrInvalidKMSConfig    = errors.New("AWS KMS signer 配置无效")
	ErrInvalidKMSKey       = errors.New("AWS KMS 密钥无效")
	ErrInvalidKMSSignature = errors.New("AWS KMS 签名无效")
)

var (
	oidPublicKeyECDSA = asn1.ObjectIdentifier{1, 2, 840, 10045, 2, 1}
	oidSecp256k1      = asn1.ObjectIdentifier{1, 3, 132, 0, 10}
)

type kmsClient interface {
	GetPublicKey(context.Context, *kms.GetPublicKeyInput, ...func(*kms.Options)) (*kms.GetPublicKeyOutput, error)
	Sign(context.Context, *kms.SignInput, ...func(*kms.Options)) (*kms.SignOutput, error)
}

// AWSKMSKey 使用 AWS KMS 中不可导出的 secp256k1 密钥签署 TRON 交易摘要。
type AWSKMSKey struct {
	client  kmsClient
	keyID   string
	address string
}

// NewAWSKMSKey 读取并校验 KMS 公钥、用途和算法，然后绑定对应 TRON 地址。
func NewAWSKMSKey(ctx context.Context, client kmsClient, keyID string) (*AWSKMSKey, error) {
	keyID = strings.TrimSpace(keyID)
	if client == nil || keyID == "" || len(keyID) > 2048 {
		return nil, ErrInvalidKMSConfig
	}
	output, err := client.GetPublicKey(ctx, &kms.GetPublicKeyInput{KeyId: &keyID})
	if err != nil {
		return nil, err
	}
	if output == nil || output.KeySpec != types.KeySpecEccSecgP256k1 ||
		output.KeyUsage != types.KeyUsageTypeSignVerify ||
		!containsSigningAlgorithm(output.SigningAlgorithms, types.SigningAlgorithmSpecEcdsaSha256) {
		return nil, ErrInvalidKMSKey
	}
	publicKey, err := parseSecp256k1PublicKey(output.PublicKey)
	if err != nil {
		return nil, err
	}
	address, err := tron.AddressFromPublicKey(publicKey.SerializeUncompressed())
	if err != nil {
		return nil, ErrInvalidKMSKey
	}
	return &AWSKMSKey{client: client, keyID: keyID, address: address}, nil
}

// Address 返回 KMS 公钥对应的 TRON Base58Check 地址。
func (key *AWSKMSKey) Address() string {
	if key == nil {
		return ""
	}
	return key.address
}

// SignDigest 请求 KMS 签署已计算的 SHA-256 摘要并返回 TRON r || s || v 签名。
func (key *AWSKMSKey) SignDigest(ctx context.Context, digest []byte) ([]byte, error) {
	if key == nil || key.client == nil || len(digest) != 32 {
		return nil, ErrInvalidDigest
	}
	output, err := key.client.Sign(ctx, &kms.SignInput{
		KeyId: &key.keyID, Message: digest, MessageType: types.MessageTypeDigest,
		SigningAlgorithm: types.SigningAlgorithmSpecEcdsaSha256,
	})
	if err != nil {
		return nil, err
	}
	if output == nil || output.SigningAlgorithm != types.SigningAlgorithmSpecEcdsaSha256 {
		return nil, ErrInvalidKMSSignature
	}
	return derToTRONSignature(output.Signature, digest, key.address)
}

func containsSigningAlgorithm(values []types.SigningAlgorithmSpec, expected types.SigningAlgorithmSpec) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

type subjectPublicKeyInfo struct {
	Algorithm struct {
		Algorithm  asn1.ObjectIdentifier
		Parameters asn1.RawValue
	}
	PublicKey asn1.BitString
}

func parseSecp256k1PublicKey(encoded []byte) (*secp256k1.PublicKey, error) {
	var information subjectPublicKeyInfo
	rest, err := asn1.Unmarshal(encoded, &information)
	if err != nil || len(rest) != 0 || !information.Algorithm.Algorithm.Equal(oidPublicKeyECDSA) ||
		information.PublicKey.BitLength != len(information.PublicKey.Bytes)*8 {
		return nil, ErrInvalidKMSKey
	}
	var curve asn1.ObjectIdentifier
	curveRest, err := asn1.Unmarshal(information.Algorithm.Parameters.FullBytes, &curve)
	if err != nil || len(curveRest) != 0 || !curve.Equal(oidSecp256k1) {
		return nil, ErrInvalidKMSKey
	}
	publicKey, err := secp256k1.ParsePubKey(information.PublicKey.Bytes)
	if err != nil {
		return nil, ErrInvalidKMSKey
	}
	return publicKey, nil
}

type ecdsaSignature struct {
	R *big.Int
	S *big.Int
}

func derToTRONSignature(encoded []byte, digest []byte, expectedAddress string) ([]byte, error) {
	if len(digest) != 32 {
		return nil, ErrInvalidKMSSignature
	}
	var parsed ecdsaSignature
	rest, err := asn1.Unmarshal(encoded, &parsed)
	order := secp256k1.Params().N
	if err != nil || len(rest) != 0 || parsed.R == nil || parsed.S == nil ||
		parsed.R.Sign() <= 0 || parsed.S.Sign() <= 0 || parsed.R.Cmp(order) >= 0 || parsed.S.Cmp(order) >= 0 {
		return nil, ErrInvalidKMSSignature
	}
	halfOrder := new(big.Int).Rsh(new(big.Int).Set(order), 1)
	if parsed.S.Cmp(halfOrder) > 0 {
		parsed.S.Sub(order, parsed.S)
	}
	signature := make([]byte, 65)
	parsed.R.FillBytes(signature[:32])
	parsed.S.FillBytes(signature[32:64])
	for recoveryID := byte(0); recoveryID <= 1; recoveryID++ {
		signature[64] = recoveryID
		if verifyDigestSignature(digest, signature, expectedAddress) == nil {
			return signature, nil
		}
	}
	zero(signature)
	return nil, ErrInvalidKMSSignature
}
