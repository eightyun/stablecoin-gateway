package merchantauth

import (
	"bytes"
	"errors"
	"testing"
)

func TestKeyringEncryptsAndRotatesSecrets(t *testing.T) {
	keyring, err := NewKeyring(map[string][]byte{
		"v1": bytes.Repeat([]byte{1}, 32),
		"v2": bytes.Repeat([]byte{2}, 32),
	}, "v2")
	if err != nil {
		t.Fatalf("NewKeyring() error = %v", err)
	}
	secret := []byte("gs_a-secure-api-secret")
	encrypted, err := keyring.Encrypt("gk_public", secret)
	if err != nil || encrypted.Version != "v2" || bytes.Contains(encrypted.Ciphertext, secret) {
		t.Fatalf("Encrypt() = %+v, %v", encrypted, err)
	}
	decrypted, err := keyring.Decrypt("gk_public", encrypted)
	if err != nil || !bytes.Equal(decrypted, secret) {
		t.Fatalf("Decrypt() = %q, %v", decrypted, err)
	}
	if _, err := keyring.Decrypt("gk_other", encrypted); !errors.Is(err, ErrInvalidSecret) {
		t.Fatalf("篡改 Key ID 后 Decrypt() error = %v", err)
	}
}

func TestNewKeyringRejectsInvalidConfiguration(t *testing.T) {
	if _, err := NewKeyring(map[string][]byte{"v1": {1}}, "v1"); !errors.Is(err, ErrInvalidKeyring) {
		t.Fatalf("NewKeyring() error = %v", err)
	}
}
