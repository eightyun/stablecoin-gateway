package config

import "testing"

func TestLoadReconciliation(t *testing.T) {
	t.Setenv("GATEWAY_DATABASE_URL", "")
	if _, err := LoadReconciliation(); err == nil {
		t.Fatal("LoadReconciliation() 未拒绝空数据库地址")
	}
	t.Setenv("GATEWAY_DATABASE_URL", "postgres://gateway@example.com/gateway")
	loaded, err := LoadReconciliation()
	if err != nil || loaded.DatabaseURL != "postgres://gateway@example.com/gateway" {
		t.Fatalf("LoadReconciliation() = %+v, %v", loaded, err)
	}
}
