package tron

import (
	"errors"
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
