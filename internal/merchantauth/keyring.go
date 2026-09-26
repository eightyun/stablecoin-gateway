package merchantauth

import "github.com/eightyun/stablecoin-gateway/internal/secretbox"

var (
	ErrInvalidKeyring = secretbox.ErrInvalidKeyring
	ErrUnknownVersion = secretbox.ErrUnknownVersion
	ErrInvalidSecret  = secretbox.ErrInvalidSecret
)

// EncryptedSecret 是加密后的 API Secret。
type EncryptedSecret = secretbox.Encrypted

// Keyring 是商户鉴权使用的密钥环别名。
type Keyring = secretbox.Keyring

// NewKeyring 创建 API Secret 密钥环。
func NewKeyring(keys map[string][]byte, activeVersion string) (*Keyring, error) {
	return secretbox.NewKeyring(keys, activeVersion)
}
