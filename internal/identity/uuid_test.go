package identity

import "testing"

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
