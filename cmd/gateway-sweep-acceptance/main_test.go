package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/sweep"
)

type plannerStub struct {
	responses []plannerResponse
	calls     int
}

type plannerResponse struct {
	plan sweep.Plan
	err  error
}

func (stub *plannerStub) PlanNext(context.Context, sweep.Policy) (sweep.Plan, error) {
	response := stub.responses[stub.calls]
	stub.calls++
	return response.plan, response.err
}

func TestWaitForPlanRetriesPendingScreening(t *testing.T) {
	expected := sweep.Plan{ID: "plan-1"}
	store := &plannerStub{responses: []plannerResponse{
		{err: sweep.ErrScreeningPending},
		{plan: expected},
	}}

	actual, err := waitForPlan(context.Background(), store, sweep.Policy{}, time.Millisecond)
	if err != nil {
		t.Fatalf("等待归集计划: %v", err)
	}
	if actual.ID != expected.ID {
		t.Fatalf("计划 ID = %q，期望 %q", actual.ID, expected.ID)
	}
	if store.calls != 2 {
		t.Fatalf("规划调用次数 = %d，期望 2", store.calls)
	}
}

func TestWaitForPlanReturnsScreeningBlock(t *testing.T) {
	store := &plannerStub{responses: []plannerResponse{{err: sweep.ErrScreeningBlocked}}}

	_, err := waitForPlan(context.Background(), store, sweep.Policy{}, time.Millisecond)
	if !errors.Is(err, sweep.ErrScreeningBlocked) {
		t.Fatalf("错误 = %v，期望 %v", err, sweep.ErrScreeningBlocked)
	}
	if store.calls != 1 {
		t.Fatalf("规划调用次数 = %d，期望 1", store.calls)
	}
}

func TestWaitForPlanHonorsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := &plannerStub{responses: []plannerResponse{{err: sweep.ErrScreeningPending}}}

	_, err := waitForPlan(ctx, store, sweep.Policy{}, time.Hour)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("错误 = %v，期望 %v", err, context.Canceled)
	}
}
