package tron

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math/big"
	"strings"

	"golang.org/x/crypto/sha3"
)

var ErrInvalidAddress = errors.New("TRON 地址无效")

const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

// NormalizeAddress 校验 TRON Base58Check 地址并返回去除首尾空白后的规范值。
func NormalizeAddress(value string) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) != 34 {
		return "", ErrInvalidAddress
	}
	decoded, ok := decodeBase58(value)
	if !ok || len(decoded) != 25 || decoded[0] != 0x41 {
		return "", ErrInvalidAddress
	}
	first := sha256.Sum256(decoded[:21])
	second := sha256.Sum256(first[:])
	for index := 0; index < 4; index++ {
		if decoded[21+index] != second[index] {
			return "", ErrInvalidAddress
		}
	}
	return value, nil
}

// AddressFromPublicKey 从 65 字节未压缩 secp256k1 公钥生成 TRON Base58Check 地址。
func AddressFromPublicKey(publicKey []byte) (string, error) {
	if len(publicKey) != 65 || publicKey[0] != 0x04 {
		return "", ErrInvalidAddress
	}
	hasher := sha3.NewLegacyKeccak256()
	_, _ = hasher.Write(publicKey[1:])
	digest := hasher.Sum(nil)
	payload := make([]byte, 21)
	payload[0] = 0x41
	copy(payload[1:], digest[len(digest)-20:])
	return encodeBase58Check(payload), nil
}

// NormalizeAddressHex 将 Base58Check 或 41 前缀十六进制地址规范化为小写十六进制。
func NormalizeAddressHex(value string) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) == 34 {
		if _, err := NormalizeAddress(value); err != nil {
			return "", err
		}
		decoded, ok := decodeBase58(value)
		if !ok || len(decoded) != 25 {
			return "", ErrInvalidAddress
		}
		return hex.EncodeToString(decoded[:21]), nil
	}
	value = strings.ToLower(value)
	if len(value) != 42 || !strings.HasPrefix(value, "41") {
		return "", ErrInvalidAddress
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 21 {
		return "", ErrInvalidAddress
	}
	return value, nil
}

// NormalizeAddressBase58 将 Base58Check 或 41 前缀十六进制地址规范化为 Base58Check。
func NormalizeAddressBase58(value string) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) == 34 {
		return NormalizeAddress(value)
	}
	hexAddress, err := NormalizeAddressHex(value)
	if err != nil {
		return "", err
	}
	payload, err := hex.DecodeString(hexAddress)
	if err != nil {
		return "", ErrInvalidAddress
	}
	return encodeBase58Check(payload), nil
}

func decodeBase58(value string) ([]byte, bool) {
	number := new(big.Int)
	base := big.NewInt(58)
	for _, character := range value {
		index := strings.IndexRune(base58Alphabet, character)
		if index < 0 {
			return nil, false
		}
		number.Mul(number, base)
		number.Add(number, big.NewInt(int64(index)))
	}
	decoded := number.Bytes()
	leadingZeros := 0
	for leadingZeros < len(value) && value[leadingZeros] == '1' {
		leadingZeros++
	}
	result := make([]byte, leadingZeros+len(decoded))
	copy(result[leadingZeros:], decoded)
	return result, true
}

func encodeBase58Check(payload []byte) string {
	first := sha256.Sum256(payload)
	second := sha256.Sum256(first[:])
	checked := make([]byte, 0, len(payload)+4)
	checked = append(checked, payload...)
	checked = append(checked, second[:4]...)

	number := new(big.Int).SetBytes(checked)
	base := big.NewInt(58)
	remainder := new(big.Int)
	encoded := make([]byte, 0, 40)
	for number.Sign() > 0 {
		number.QuoRem(number, base, remainder)
		encoded = append(encoded, base58Alphabet[remainder.Int64()])
	}
	for _, value := range checked {
		if value != 0 {
			break
		}
		encoded = append(encoded, base58Alphabet[0])
	}
	for left, right := 0, len(encoded)-1; left < right; left, right = left+1, right-1 {
		encoded[left], encoded[right] = encoded[right], encoded[left]
	}
	return string(encoded)
}
