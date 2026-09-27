package reconciliation

import (
	"context"
	"errors"
	"testing"
)

func TestFindingFingerprintUsesStableIdentity(t *testing.T) {
	first := finding{
		RuleCode: "rule", Severity: SeverityCritical,
		ResourceType: "deposit", ResourceID: "deposit-1", AssetID: "asset-1",
		Evidence: []byte(`{"actual":"one"}`),
	}
	second := first
	second.Evidence = []byte(`{"actual":"two"}`)
	if findingFingerprint(first) != findingFingerprint(second) {
		t.Fatal("同一差异身份因证据变化生成了不同指纹")
	}
	second.ResourceID = "deposit-2"
	if findingFingerprint(first) == findingFingerprint(second) {
		t.Fatal("不同资源生成了相同指纹")
	}
}

func TestValidateFinding(t *testing.T) {
	valid := finding{
		RuleCode: "rule", Severity: SeverityCritical,
		ResourceType: "deposit", ResourceID: "deposit-1", Evidence: []byte(`{}`),
	}
	if err := validateFinding(valid); err != nil {
		t.Fatalf("validateFinding() error = %v", err)
	}
	valid.Evidence = []byte(`invalid`)
	if err := validateFinding(valid); err == nil {
		t.Fatal("validateFinding() 未拒绝无效 JSON")
	}
}

func TestResolveCaseRejectsInvalidRequestBeforeDatabase(t *testing.T) {
	store := &Store{}
	if _, err := store.ListOpenCases(context.Background(), 0); !errors.Is(err, ErrInvalidList) {
		t.Fatalf("ListOpenCases() error = %v", err)
	}
	if err := store.ResolveCase(context.Background(), ResolveRequest{}); !errors.Is(err, ErrInvalidResolve) {
		t.Fatalf("ResolveCase() error = %v", err)
	}
}
