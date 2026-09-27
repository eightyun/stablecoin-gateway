package monitoring

import (
	"context"
	"errors"
	"testing"
)

func TestSamplerUpdatesMetrics(t *testing.T) {
	want := Snapshot{OpenReconciliationCases: map[string]int64{"critical": 1}}
	sink := &metricsStub{}
	sampler, err := NewSampler(sourceStub{snapshot: want}, sink)
	if err != nil {
		t.Fatalf("NewSampler() error = %v", err)
	}
	worked, err := sampler.RunOnce(context.Background())
	if err != nil || worked {
		t.Fatalf("RunOnce() = %v, %v", worked, err)
	}
	if sink.snapshot.OpenReconciliationCases["critical"] != 1 {
		t.Fatalf("Update() snapshot = %+v", sink.snapshot)
	}
}

func TestSamplerDoesNotReplaceMetricsOnReadFailure(t *testing.T) {
	want := errors.New("database unavailable")
	sink := &metricsStub{}
	sampler, err := NewSampler(sourceStub{err: want}, sink)
	if err != nil {
		t.Fatalf("NewSampler() error = %v", err)
	}
	if _, err := sampler.RunOnce(context.Background()); !errors.Is(err, want) {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if sink.calls != 0 {
		t.Fatalf("Update() calls = %d", sink.calls)
	}
}

type sourceStub struct {
	snapshot Snapshot
	err      error
}

func (stub sourceStub) Snapshot(context.Context) (Snapshot, error) {
	return stub.snapshot, stub.err
}

type metricsStub struct {
	calls    int
	snapshot Snapshot
}

func (stub *metricsStub) Update(snapshot Snapshot) {
	stub.calls++
	stub.snapshot = snapshot
}
