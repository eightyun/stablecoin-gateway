package identity

import (
	"errors"
	"testing"
)

func TestNewUUID(t *testing.T) {
	first, err := NewUUID()
	if err != nil {
		t.Fatalf("NewUUID() error = %v", err)
	}
	second, err := NewUUID()
	if err != nil {
		t.Fatalf("NewUUID() error = %v", err)
	}
	if len(first) != 36 || first[14] != '4' || first == second {
		t.Fatalf("NewUUID() 生成结果无效: %q, %q", first, second)
	}
	if first[8] != '-' || first[13] != '-' || first[18] != '-' || first[23] != '-' {
		t.Fatalf("NewUUID() 格式无效: %q", first)
	}
}

func TestNewToken(t *testing.T) {
	first, err := NewToken(32)
	if err != nil {
		t.Fatalf("NewToken() error = %v", err)
	}
	second, err := NewToken(32)
	if err != nil || len(first) != 43 || first == second {
		t.Fatalf("NewToken() = %q, %q, %v", first, second, err)
	}
	if _, err := NewToken(8); !errors.Is(err, ErrInvalidTokenSize) {
		t.Fatalf("NewToken() error = %v", err)
	}
}

func TestValidUUID(t *testing.T) {
	if !ValidUUID("123e4567-e89b-12d3-a456-426614174000") {
		t.Fatal("ValidUUID() 拒绝有效 UUID")
	}
	for _, value := range []string{"", "123e4567-e89b-12d3-a456-42661417400z", "123e4567e89b12d3a456426614174000"} {
		if ValidUUID(value) {
			t.Fatalf("ValidUUID() 接受 %q", value)
		}
	}
}
