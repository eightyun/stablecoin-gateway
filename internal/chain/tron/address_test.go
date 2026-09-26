package tron

import (
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func TestNormalizeAddress(t *testing.T) {
	const address = "T9yD14Nj9j7xAB4dbGeiX9h8unkKHxuWwb"
	value, err := NormalizeAddress("  " + address + "  ")
	if err != nil || value != address {
		t.Fatalf("NormalizeAddress() = %q, %v", value, err)
	}
	for _, invalid := range []string{
		"", "0x41", "T9yD14Nj9j7xAB4dbGeiX9h8unkKHxuWwa", "O9yD14Nj9j7xAB4dbGeiX9h8unkKHxuWwb",
	} {
		if _, err := NormalizeAddress(invalid); !errors.Is(err, ErrInvalidAddress) {
			t.Fatalf("NormalizeAddress(%q) error = %v", invalid, err)
		}
	}
}

func TestNormalizeAddressHex(t *testing.T) {
	const base58Address = "T9yD14Nj9j7xAB4dbGeiX9h8unkKHxuWwb"
	const hexAddress = "410000000000000000000000000000000000000000"
	for _, input := range []string{base58Address, strings.ToUpper(hexAddress)} {
		value, err := NormalizeAddressHex(input)
		if err != nil || value != hexAddress {
			t.Fatalf("NormalizeAddressHex(%q) = %q, %v", input, value, err)
		}
	}
	if _, err := NormalizeAddressHex("4100"); !errors.Is(err, ErrInvalidAddress) {
		t.Fatalf("NormalizeAddressHex() error = %v", err)
	}
}

func TestEncodeBase58CheckOfficialVector(t *testing.T) {
	payload, err := hex.DecodeString("415a523b449890854c8fc460ab602df9f31fe4293f")
	if err != nil {
		t.Fatal(err)
	}
	if address := encodeBase58Check(payload); address != "TJCnKsPa7y5okkXvQAidZBzqx3QyQ6sxMW" {
		t.Fatalf("encodeBase58Check() = %s", address)
	}
}

func TestNormalizeAddressBase58(t *testing.T) {
	const address = "TJCnKsPa7y5okkXvQAidZBzqx3QyQ6sxMW"
	value, err := NormalizeAddressBase58("415a523b449890854c8fc460ab602df9f31fe4293f")
	if err != nil || value != address {
		t.Fatalf("NormalizeAddressBase58() = %s, %v", value, err)
	}
}
