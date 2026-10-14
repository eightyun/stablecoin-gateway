package signer

import (
	"context"
	"encoding/asn1"
	"errors"
	"math/big"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
)

type kmsClientStub struct {
	publicKeyOutput *kms.GetPublicKeyOutput
	signOutput      *kms.SignOutput
	signError       error
	signInput       *kms.SignInput
}

func (client *kmsClientStub) GetPublicKey(
	context.Context,
	*kms.GetPublicKeyInput,
	...func(*kms.Options),
) (*kms.GetPublicKeyOutput, error) {
	return client.publicKeyOutput, nil
}

func (client *kmsClientStub) Sign(
	_ context.Context,
	input *kms.SignInput,
	_ ...func(*kms.Options),
) (*kms.SignOutput, error) {
	client.signInput = input
	return client.signOutput, client.signError
}

func TestAWSKMSKeyLoadsAndConvertsSignature(t *testing.T) {
	privateKey := testKMSPrivateKey(t)
	defer privateKey.Zero()
	publicKey := marshalKMSPublicKey(t, privateKey.PubKey().SerializeUncompressed())
	digest := make([]byte, 32)
	digest[31] = 42
	derSignature := ecdsa.Sign(privateKey, digest).Serialize()
	client := &kmsClientStub{
		publicKeyOutput: &kms.GetPublicKeyOutput{
			PublicKey: publicKey, KeySpec: types.KeySpecEccSecgP256k1,
			KeyUsage:          types.KeyUsageTypeSignVerify,
			SigningAlgorithms: []types.SigningAlgorithmSpec{types.SigningAlgorithmSpecEcdsaSha256},
		},
		signOutput: &kms.SignOutput{
			Signature: derSignature, SigningAlgorithm: types.SigningAlgorithmSpecEcdsaSha256,
		},
	}
	key, err := NewAWSKMSKey(context.Background(), client, "alias/gateway-tron")
	if err != nil {
		t.Fatalf("NewAWSKMSKey() error = %v", err)
	}
	if key.Address() != testOwner {
		t.Fatalf("Address() = %s", key.Address())
	}
	signature, err := key.SignDigest(context.Background(), digest)
	if err != nil {
		t.Fatalf("SignDigest() error = %v", err)
	}
	if len(signature) != 65 || signature[64] > 1 {
		t.Fatalf("TRON signature = %x", signature)
	}
	if err := verifyDigestSignature(digest, signature, testOwner); err != nil {
		t.Fatalf("签名恢复校验 error = %v", err)
	}
	if client.signInput == nil || client.signInput.MessageType != types.MessageTypeDigest ||
		client.signInput.SigningAlgorithm != types.SigningAlgorithmSpecEcdsaSha256 {
		t.Fatalf("KMS Sign input = %+v", client.signInput)
	}
}

func TestAWSKMSKeyRejectsInvalidMetadataAndSignature(t *testing.T) {
	privateKey := testKMSPrivateKey(t)
	defer privateKey.Zero()
	publicKey := marshalKMSPublicKey(t, privateKey.PubKey().SerializeUncompressed())
	client := &kmsClientStub{publicKeyOutput: &kms.GetPublicKeyOutput{
		PublicKey: publicKey, KeySpec: types.KeySpecEccSecgP256k1,
		KeyUsage:          types.KeyUsageTypeEncryptDecrypt,
		SigningAlgorithms: []types.SigningAlgorithmSpec{types.SigningAlgorithmSpecEcdsaSha256},
	}}
	if _, err := NewAWSKMSKey(context.Background(), client, "key-id"); !errors.Is(err, ErrInvalidKMSKey) {
		t.Fatalf("错误用途 NewAWSKMSKey() error = %v", err)
	}
	client.publicKeyOutput.KeyUsage = types.KeyUsageTypeSignVerify
	client.signOutput = &kms.SignOutput{
		Signature: []byte{0x30, 0x00}, SigningAlgorithm: types.SigningAlgorithmSpecEcdsaSha256,
	}
	key, err := NewAWSKMSKey(context.Background(), client, "key-id")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := key.SignDigest(context.Background(), make([]byte, 32)); !errors.Is(err, ErrInvalidKMSSignature) {
		t.Fatalf("错误 DER SignDigest() error = %v", err)
	}
}

func testKMSPrivateKey(t *testing.T) *secp256k1.PrivateKey {
	t.Helper()
	value := make([]byte, 32)
	value[31] = 1
	return secp256k1.PrivKeyFromBytes(value)
}

func marshalKMSPublicKey(t *testing.T, publicKey []byte) []byte {
	t.Helper()
	parameters, err := asn1.Marshal(oidSecp256k1)
	if err != nil {
		t.Fatal(err)
	}
	information := subjectPublicKeyInfo{PublicKey: asn1.BitString{Bytes: publicKey, BitLength: len(publicKey) * 8}}
	information.Algorithm.Algorithm = oidPublicKeyECDSA
	information.Algorithm.Parameters = asn1.RawValue{FullBytes: parameters}
	encoded, err := asn1.Marshal(information)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestDERToTRONSignatureNormalizesHighS(t *testing.T) {
	privateKey := testKMSPrivateKey(t)
	defer privateKey.Zero()
	digest := make([]byte, 32)
	digest[0] = 7
	lowDER := ecdsa.Sign(privateKey, digest).Serialize()
	var parsed ecdsaSignature
	if _, err := asn1.Unmarshal(lowDER, &parsed); err != nil {
		t.Fatal(err)
	}
	parsed.S = new(big.Int).Sub(secp256k1.Params().N, parsed.S)
	highDER, err := asn1.Marshal(parsed)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := derToTRONSignature(highDER, digest, testOwner)
	if err != nil {
		t.Fatal(err)
	}
	s := new(big.Int).SetBytes(signature[32:64])
	halfOrder := new(big.Int).Rsh(new(big.Int).Set(secp256k1.Params().N), 1)
	if s.Cmp(halfOrder) > 0 {
		t.Fatalf("签名未规范化为 low-S: %x", signature)
	}
}

func TestAWSKMSReadinessRejectsChangedKeyAndDoesNotSign(t *testing.T) {
	privateKey := testKMSPrivateKey(t)
	defer privateKey.Zero()
	client := &kmsClientStub{publicKeyOutput: &kms.GetPublicKeyOutput{
		PublicKey: marshalKMSPublicKey(t, privateKey.PubKey().SerializeUncompressed()),
		KeySpec:   types.KeySpecEccSecgP256k1, KeyUsage: types.KeyUsageTypeSignVerify,
		SigningAlgorithms: []types.SigningAlgorithmSpec{types.SigningAlgorithmSpecEcdsaSha256},
	}}
	key, err := NewAWSKMSKey(context.Background(), client, "alias/gateway")
	if err != nil {
		t.Fatal(err)
	}
	if err := key.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	other, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	defer other.Zero()
	client.publicKeyOutput.PublicKey = marshalKMSPublicKey(t, other.PubKey().SerializeUncompressed())
	if err := key.Ping(context.Background()); !errors.Is(err, ErrInvalidKMSKey) {
		t.Fatalf("变更公钥 Ping() = %v", err)
	}
	client.publicKeyOutput.KeyUsage = types.KeyUsageTypeEncryptDecrypt
	if err := key.Ping(context.Background()); !errors.Is(err, ErrInvalidKMSKey) {
		t.Fatalf("无效用途 Ping() = %v", err)
	}
	if client.signInput != nil {
		t.Fatal("就绪探针不应请求签名")
	}
}
