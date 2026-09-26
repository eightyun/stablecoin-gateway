package tron

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math/big"
	"strings"
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
