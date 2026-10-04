package config

import (
	"testing"
	"time"
)

func TestLoadSweepPlanner(t *testing.T) {
	clearSweepPlannerEnvironmentForTest(t)
	t.Setenv("GATEWAY_DATABASE_URL", "postgres://gateway:test@localhost/gateway")
	t.Setenv("GATEWAY_SWEEP_ASSET_ID", "usdt-tron-nile")
	t.Setenv("GATEWAY_SWEEP_MINIMUM_AMOUNT", "10000000")
	config, err := LoadSweepPlanner()
	if err != nil {
		t.Fatalf("LoadSweepPlanner() error = %v", err)
	}
	if config.MinimumAmount != "10000000" || config.MaxSnapshotAge != 10*time.Minute ||
		config.OperationTimeout != 10*time.Second || config.IdleInterval != 5*time.Second {
		t.Fatalf("LoadSweepPlanner() = %+v", config)
	}
}

func TestLoadSweepPlannerRejectsInvalidAmountAndRetry(t *testing.T) {
	for _, test := range []struct {
		name  string
		env   string
		value string
	}{
		{name: "金额不是正整数", env: "GATEWAY_SWEEP_MINIMUM_AMOUNT", value: "0"},
		{name: "金额超过数据库精度", env: "GATEWAY_SWEEP_MINIMUM_AMOUNT", value: "1000000000000000000000000000000000000000000000000000000000000000000000000000000"},
		{name: "退避倒置", env: "GATEWAY_SWEEP_PLANNER_RETRY_MAX", value: "500ms"},
	} {
		t.Run(test.name, func(t *testing.T) {
			clearSweepPlannerEnvironmentForTest(t)
			t.Setenv("GATEWAY_DATABASE_URL", "postgres://gateway:test@localhost/gateway")
			t.Setenv("GATEWAY_SWEEP_ASSET_ID", "usdt-tron-nile")
			t.Setenv("GATEWAY_SWEEP_MINIMUM_AMOUNT", "10000000")
			t.Setenv(test.env, test.value)
			if _, err := LoadSweepPlanner(); err == nil {
				t.Fatal("LoadSweepPlanner() 未拒绝无效配置")
			}
		})
	}
}

func clearSweepPlannerEnvironmentForTest(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"GATEWAY_DATABASE_URL", "GATEWAY_SWEEP_ASSET_ID", "GATEWAY_SWEEP_MINIMUM_AMOUNT",
		"GATEWAY_SWEEP_MAX_SNAPSHOT_AGE", "GATEWAY_SWEEP_PLANNER_OPERATION_TIMEOUT",
		"GATEWAY_SWEEP_PLANNER_IDLE_INTERVAL", "GATEWAY_SWEEP_PLANNER_RETRY_MIN",
		"GATEWAY_SWEEP_PLANNER_RETRY_MAX",
	} {
		t.Setenv(name, "")
	}
}
