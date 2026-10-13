package migrations

import "testing"

func TestLatestVersionValidatesEmbeddedMigrations(t *testing.T) {
	version, err := LatestVersion()
	if err != nil {
		t.Fatalf("LatestVersion() error = %v", err)
	}
	if version == 0 {
		t.Fatal("LatestVersion() 返回零")
	}
}
