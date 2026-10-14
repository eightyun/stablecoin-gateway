package main

import (
	"bytes"
	"context"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
)

const acceptanceAddress = "TMVQGm1qAQYVdetCeGRRkTWYYrLXuHK2HC"

func TestRunUsesRealSDKAndRedactsAcceptanceOutput(t *testing.T) {
	for _, performSign := range []bool{false, true} {
		t.Run(map[bool]string{false: "公钥", true: "签名"}[performSign], func(t *testing.T) {
			privateKeyBytes := make([]byte, 32)
			privateKeyBytes[31] = 1
			privateKey := secp256k1.PrivKeyFromBytes(privateKeyBytes)
			defer privateKey.Zero()
			publicKey := encodedPublicKey(t, privateKey.PubKey().SerializeUncompressed())
			var gets, signs atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("Content-Type", "application/x-amz-json-1.1")
				switch request.Header.Get("X-Amz-Target") {
				case "TrentService.GetPublicKey":
					gets.Add(1)
					_ = json.NewEncoder(writer).Encode(map[string]any{
						"PublicKey": base64.StdEncoding.EncodeToString(publicKey), "KeySpec": "ECC_SECG_P256K1",
						"KeyUsage": "SIGN_VERIFY", "SigningAlgorithms": []string{"ECDSA_SHA_256"},
					})
				case "TrentService.Sign":
					signs.Add(1)
					var input struct {
						KeyID            string `json:"KeyId"`
						Message          []byte `json:"Message"`
						MessageType      string `json:"MessageType"`
						SigningAlgorithm string `json:"SigningAlgorithm"`
					}
					if err := json.NewDecoder(request.Body).Decode(&input); err != nil || len(input.Message) != 32 || input.MessageType != "DIGEST" || input.SigningAlgorithm != "ECDSA_SHA_256" {
						t.Errorf("KMS Sign 协议不符合要求")
						writer.WriteHeader(http.StatusBadRequest)
						return
					}
					_ = json.NewEncoder(writer).Encode(map[string]any{
						"Signature":        base64.StdEncoding.EncodeToString(ecdsa.Sign(privateKey, input.Message).Serialize()),
						"SigningAlgorithm": "ECDSA_SHA_256",
					})
				default:
					t.Errorf("意外 AWS 请求")
					writer.WriteHeader(http.StatusBadRequest)
				}
			}))
			defer server.Close()
			setAWSTestEnv(t, server.URL)
			args := []string{"--region", "ap-southeast-1", "--key-id", "alias/private-key-id", "--expected-address", acceptanceAddress}
			if performSign {
				args = append(args, "--sign")
			}
			var output bytes.Buffer
			if err := run(context.Background(), args, &output); err != nil {
				t.Fatal(err)
			}
			var result struct {
				Status            string    `json:"status"`
				Mode              string    `json:"mode"`
				AddressMatched    bool      `json:"address_matched"`
				SignatureVerified bool      `json:"signature_verified"`
				CheckedAt         time.Time `json:"checked_at"`
			}
			if err := json.Unmarshal(output.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.Status != "ok" || !result.AddressMatched || result.SignatureVerified != performSign || result.CheckedAt.IsZero() || gets.Load() != 1 {
				t.Fatalf("验收输出 = %s gets=%d", output.String(), gets.Load())
			}
			if performSign && (result.Mode != "signature" || signs.Load() != 1) || !performSign && (result.Mode != "public_key" || signs.Load() != 0) {
				t.Fatal("签名选择与输出或调用次数不匹配")
			}
			for _, secret := range []string{acceptanceAddress, "private-key-id", "test-access-key", "test-secret-key", base64.StdEncoding.EncodeToString(publicKey)} {
				if strings.Contains(output.String(), secret) {
					t.Fatal("验收输出泄漏配置或密钥原文")
				}
			}
		})
	}
}

func TestRunRedactsCloudErrorsAndDoesNotOutputSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/x-amz-json-1.1")
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = writer.Write([]byte(`{"__type":"AccessDeniedException","message":"secret ARN and credential detail"}`))
	}))
	defer server.Close()
	setAWSTestEnv(t, server.URL)
	var output bytes.Buffer
	err := run(context.Background(), []string{"--region", "ap-southeast-1", "--key-id", "private-id", "--expected-address", acceptanceAddress, "--sign"}, &output)
	if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "private-id") || output.Len() != 0 {
		t.Fatalf("失败输出 = %s error=%v", output.String(), err)
	}
}

func TestRunRejectsCanceledContextBeforeAWSCall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var output bytes.Buffer
	err := run(ctx, []string{"--region", "ap-southeast-1", "--key-id", "key", "--expected-address", acceptanceAddress}, &output)
	if !errors.Is(err, context.Canceled) || output.Len() != 0 {
		t.Fatalf("取消验收 error=%v output=%s", err, output.String())
	}
}

func TestRunDeadlineStopsCloudRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		select {
		case <-request.Context().Done():
		case <-time.After(time.Second):
		}
	}))
	defer server.Close()
	setAWSTestEnv(t, server.URL)
	var output bytes.Buffer
	err := run(context.Background(), []string{"--region", "ap-southeast-1", "--key-id", "key", "--expected-address", acceptanceAddress, "--timeout", "100ms"}, &output)
	if !errors.Is(err, context.DeadlineExceeded) || output.Len() != 0 {
		t.Fatalf("超时验收 error=%v output=%s", err, output.String())
	}
}

func TestParseOptionsValidatesInputsBeforeAWSCalls(t *testing.T) {
	for _, name := range []string{"GATEWAY_KMS_SIGNER_AWS_REGION", "GATEWAY_KMS_SIGNER_AWS_KEY_ID", "GATEWAY_KMS_SIGNER_OWNER_ADDRESS"} {
		t.Setenv(name, "")
	}
	for _, args := range [][]string{
		nil,
		{"--region", "ap-southeast-1", "--key-id", "key", "--expected-address", "invalid"},
		{"--region", "ap-southeast-1", "--key-id", "key", "--expected-address", acceptanceAddress, "--timeout", "0s"},
		{"--region", "ap-southeast-1", "--key-id", "key", "--expected-address", acceptanceAddress, "--timeout", "2m"},
		{"--region", "ap-southeast-1", "--key-id", "key\nsecret", "--expected-address", acceptanceAddress},
		{"--digest", "arbitrary-input"},
	} {
		if _, err := parseOptions(args); err == nil {
			t.Fatalf("应拒绝非法配置")
		}
	}
}

func setAWSTestEnv(t *testing.T, endpoint string) {
	t.Helper()
	for name, value := range map[string]string{
		"AWS_ENDPOINT_URL_KMS": endpoint, "AWS_ENDPOINT_URL": "", "AWS_PROFILE": "", "AWS_SESSION_TOKEN": "",
		"AWS_ACCESS_KEY_ID": "test-access-key", "AWS_SECRET_ACCESS_KEY": "test-secret-key", "AWS_EC2_METADATA_DISABLED": "true",
		"AWS_CONFIG_FILE": t.TempDir() + "/missing-config", "AWS_SHARED_CREDENTIALS_FILE": t.TempDir() + "/missing-credentials", "AWS_MAX_ATTEMPTS": "1",
	} {
		t.Setenv(name, value)
	}
}

func encodedPublicKey(t *testing.T, publicKey []byte) []byte {
	t.Helper()
	parameters, err := asn1.Marshal(asn1.ObjectIdentifier{1, 3, 132, 0, 10})
	if err != nil {
		t.Fatal(err)
	}
	information := struct {
		Algorithm struct {
			Algorithm  asn1.ObjectIdentifier
			Parameters asn1.RawValue
		}
		PublicKey asn1.BitString
	}{PublicKey: asn1.BitString{Bytes: publicKey, BitLength: len(publicKey) * 8}}
	information.Algorithm.Algorithm = asn1.ObjectIdentifier{1, 2, 840, 10045, 2, 1}
	information.Algorithm.Parameters = asn1.RawValue{FullBytes: parameters}
	encoded, err := asn1.Marshal(information)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
