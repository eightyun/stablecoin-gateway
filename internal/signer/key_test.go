package signer

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileKeyLoadsAndSigns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte(strings.Repeat("0", 63)+"1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	key, err := LoadFileKey(path)
	if err != nil {
		t.Fatalf("LoadFileKey() error = %v", err)
	}
	defer key.Close()
	if key.Address() != "TMVQGm1qAQYVdetCeGRRkTWYYrLXuHK2HC" || !key.MatchesAddress(key.Address()) {
		t.Fatalf("Address() = %s", key.Address())
	}
	digest, _ := hex.DecodeString(strings.Repeat("01", 32))
	signature, err := key.SignDigest(digest)
	if err != nil || len(signature) != 65 || signature[64] > 1 {
		t.Fatalf("SignDigest() = %x, %v", signature, err)
	}
	if err := verifyDigestSignature(digest, signature, key.Address()); err != nil {
		t.Fatalf("verifyDigestSignature() error = %v", err)
	}
}

func TestFileKeyRejectsLoosePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte(strings.Repeat("0", 63)+"1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFileKey(path); err != ErrInvalidKeyFile {
		t.Fatalf("LoadFileKey() error = %v", err)
	}
}
