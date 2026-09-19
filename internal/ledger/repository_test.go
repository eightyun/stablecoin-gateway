package ledger

import "testing"

func TestRequestFingerprint(t *testing.T) {
	base := Transaction{
		ID:             "journal-1",
		RequesterType:  "merchant",
		RequesterID:    "merchant-1",
		IdempotencyKey: "deposit:1",
		ReferenceType:  "deposit",
		ReferenceID:    "deposit-1",
		Entries: []Entry{
			{AccountID: "custody", AssetID: "usdt-tron", Side: Debit, Amount: 10},
			{AccountID: "merchant", AssetID: "usdt-tron", Side: Credit, Amount: 10},
		},
	}

	first, err := requestFingerprint(base)
	if err != nil {
		t.Fatalf("requestFingerprint() error = %v", err)
	}

	sameRequest := base
	sameRequest.ID = "journal-2"
	second, err := requestFingerprint(sameRequest)
	if err != nil {
		t.Fatalf("requestFingerprint() error = %v", err)
	}
	if first != second {
		t.Fatal("相同业务请求使用不同交易 ID 时摘要应保持一致")
	}

	differentRequest := base
	differentRequest.Entries = append([]Entry(nil), base.Entries...)
	differentRequest.Entries[0].Amount = 11
	third, err := requestFingerprint(differentRequest)
	if err != nil {
		t.Fatalf("requestFingerprint() error = %v", err)
	}
	if first == third {
		t.Fatal("不同金额的业务请求摘要不应相同")
	}
}

func TestNewPostgreSQLRepositoryRejectsNilDatabase(t *testing.T) {
	_, err := NewPostgreSQLRepository(nil)
	if err != ErrDatabaseRequired {
		t.Fatalf("NewPostgreSQLRepository() error = %v, 期望 %v", err, ErrDatabaseRequired)
	}
}
