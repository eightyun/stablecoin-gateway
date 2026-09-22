package indexer

import "testing"

func TestNewCursorStoreRejectsNilDatabase(t *testing.T) {
	_, err := NewCursorStore(nil)
	if err != ErrDatabaseRequired {
		t.Fatalf("NewCursorStore() error = %v, 期望 %v", err, ErrDatabaseRequired)
	}
}
