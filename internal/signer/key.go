// Package signer 提供与业务进程隔离的交易签名能力。
package signer

import (
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/eightyun/stablecoin-gateway/internal/chain/tron"
)

var (
	ErrInvalidKeyFile = errors.New("测试网 signer 私钥文件无效")
	ErrInvalidDigest  = errors.New("签名摘要无效")
)

// DigestSigner 对已经校验的 32 字节交易摘要签名。
type DigestSigner interface {
	Address() string
	SignDigest([]byte) ([]byte, error)
}

// FileKey 是仅供测试网独立 signer 使用的软件密钥。
type FileKey struct {
	privateKey *secp256k1.PrivateKey
	address    string
}

// LoadFileKey 从权限严格为 0600 的普通文件加载 32 字节十六进制私钥。
func LoadFileKey(path string) (*FileKey, error) {
	path = strings.TrimSpace(path)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return nil, ErrInvalidKeyFile
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, ErrInvalidKeyFile
	}
	defer file.Close()
	encoded, err := io.ReadAll(io.LimitReader(file, 67))
	if err != nil {
		return nil, ErrInvalidKeyFile
	}
	encoded = []byte(strings.TrimSpace(string(encoded)))
	if len(encoded) != 64 {
		zero(encoded)
		return nil, ErrInvalidKeyFile
	}
	privateKeyBytes := make([]byte, 32)
	if _, err := hex.Decode(privateKeyBytes, encoded); err != nil {
		zero(encoded)
		zero(privateKeyBytes)
		return nil, ErrInvalidKeyFile
	}
	zero(encoded)
	var scalar secp256k1.ModNScalar
	if overflow := scalar.SetByteSlice(privateKeyBytes); overflow || scalar.IsZero() {
		zero(privateKeyBytes)
		scalar.Zero()
		return nil, ErrInvalidKeyFile
	}
	zero(privateKeyBytes)
	privateKey := secp256k1.NewPrivateKey(&scalar)
	scalar.Zero()
	address, err := tron.AddressFromPublicKey(privateKey.PubKey().SerializeUncompressed())
	if err != nil {
		privateKey.Zero()
		return nil, ErrInvalidKeyFile
	}
	return &FileKey{privateKey: privateKey, address: address}, nil
}

// Address 返回私钥对应的 TRON Base58Check 地址。
func (key *FileKey) Address() string {
	return key.address
}

// SignDigest 返回 TRON 规范的 r || s || v，其中 v 为 0 或 1。
func (key *FileKey) SignDigest(digest []byte) ([]byte, error) {
	if key == nil || key.privateKey == nil || len(digest) != 32 {
		return nil, ErrInvalidDigest
	}
	compact := ecdsa.SignCompact(key.privateKey, digest, false)
	recoveryID := compact[0] - 27
	if recoveryID > 1 {
		return nil, fmt.Errorf("恢复标识 %d 不受 TRON 规范支持: %w", recoveryID, ErrInvalidDigest)
	}
	signature := make([]byte, 65)
	copy(signature[:64], compact[1:])
	signature[64] = recoveryID
	return signature, nil
}

func verifyDigestSignature(digest []byte, signature []byte, expectedAddress string) error {
	if len(digest) != 32 || len(signature) != 65 || signature[64] > 1 {
		return ErrInvalidDigest
	}
	compact := make([]byte, 65)
	compact[0] = 27 + signature[64]
	copy(compact[1:], signature[:64])
	publicKey, _, err := ecdsa.RecoverCompact(compact, digest)
	if err != nil {
		return ErrInvalidDigest
	}
	address, err := tron.AddressFromPublicKey(publicKey.SerializeUncompressed())
	if err != nil || len(address) != len(expectedAddress) ||
		subtle.ConstantTimeCompare([]byte(address), []byte(expectedAddress)) != 1 {
		return ErrInvalidDigest
	}
	return nil
}

// MatchesAddress 以固定时间比较配置地址与密钥地址。
func (key *FileKey) MatchesAddress(address string) bool {
	normalized, err := tron.NormalizeAddressBase58(address)
	if err != nil || len(normalized) != len(key.address) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(normalized), []byte(key.address)) == 1
}

// Close 尽力清除进程内私钥标量。
func (key *FileKey) Close() {
	if key != nil && key.privateKey != nil {
		key.privateKey.Zero()
		key.privateKey = nil
	}
}

func zero(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
