package signer

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
)

type acceptanceKMSStub struct {
	kmsClientStub
	privateKey *secp256k1.PrivateKey
	digests    [][]byte
}

func (client *acceptanceKMSStub) Sign(ctx context.Context, input *kms.SignInput, _ ...func(*kms.Options)) (*kms.SignOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	client.digests = append(client.digests, bytes.Clone(input.Message))
	return &kms.SignOutput{
		Signature:        ecdsa.Sign(client.privateKey, input.Message).Serialize(),
		SigningAlgorithm: types.SigningAlgorithmSpecEcdsaSha256,
	}, nil
}

func acceptanceKey(t *testing.T) (*AWSKMSKey, *acceptanceKMSStub) {
	t.Helper()
	privateKey := testKMSPrivateKey(t)
	t.Cleanup(privateKey.Zero)
	client := &acceptanceKMSStub{privateKey: privateKey, kmsClientStub: kmsClientStub{
		publicKeyOutput: &kms.GetPublicKeyOutput{
			PublicKey: marshalKMSPublicKey(t, privateKey.PubKey().SerializeUncompressed()),
			KeySpec:   types.KeySpecEccSecgP256k1, KeyUsage: types.KeyUsageTypeSignVerify,
			SigningAlgorithms: []types.SigningAlgorithmSpec{types.SigningAlgorithmSpecEcdsaSha256},
		},
	}}
	key, err := NewAWSKMSKey(context.Background(), client, "acceptance-key")
	if err != nil {
		t.Fatal(err)
	}
	return key, client
}

func TestKMSAcceptancePublicKeyCheckDoesNotSign(t *testing.T) {
	key, client := acceptanceKey(t)
	result, err := key.CheckAcceptance(context.Background(), testOwner, false)
	if err != nil || !result.AddressMatched || result.SignatureVerified || len(result.AddressFingerprint) != 64 || len(client.digests) != 0 {
		t.Fatalf("只读验收 = %+v error=%v calls=%d", result, err, len(client.digests))
	}
}

func TestKMSAcceptanceSignsFreshChallenges(t *testing.T) {
	key, client := acceptanceKey(t)
	for range 2 {
		result, err := key.CheckAcceptance(context.Background(), testOwner, true)
		if err != nil || !result.SignatureVerified {
			t.Fatalf("签名验收 = %+v error=%v", result, err)
		}
	}
	if len(client.digests) != 2 || len(client.digests[0]) != 32 || bytes.Equal(client.digests[0], client.digests[1]) {
		t.Fatal("两次验收应签署不同的随机挑战摘要")
	}
}

func TestKMSAcceptanceRejectsAddressMismatchBeforeSigning(t *testing.T) {
	key, client := acceptanceKey(t)
	for _, address := range []string{testDestination, "invalid-address"} {
		if _, err := key.CheckAcceptance(context.Background(), address, true); !errors.Is(err, ErrPolicyDenied) {
			t.Fatalf("错误地址验收 error=%v", err)
		}
	}
	if len(client.digests) != 0 {
		t.Fatal("地址不匹配时不应调用 Sign")
	}
}

func TestKMSAcceptanceRejectsWrongSignatureAndCancellation(t *testing.T) {
	key, client := acceptanceKey(t)
	other, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	defer other.Zero()
	client.privateKey = other
	if _, err := key.CheckAcceptance(context.Background(), testOwner, true); !errors.Is(err, ErrInvalidKMSSignature) {
		t.Fatalf("错误签名 error=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := key.CheckAcceptance(ctx, testOwner, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消验收 error=%v", err)
	}
	if len(client.digests) != 1 {
		t.Fatal("取消后不应追加签名调用")
	}
}
