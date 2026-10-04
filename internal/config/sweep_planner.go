package config

import (
	"fmt"
	"math/big"
	"strings"
	"time"
)

const (
	defaultSweepPlannerOperationTimeout = 10 * time.Second
	defaultSweepPlannerIdleInterval     = 5 * time.Second
	defaultSweepPlannerRetryMin         = time.Second
	defaultSweepPlannerRetryMax         = 30 * time.Second
	defaultSweepMaxSnapshotAge          = 10 * time.Minute
)

// SweepPlannerConfig 保存归集规划进程配置。
type SweepPlannerConfig struct {
	DatabaseURL      string
	AssetID          string
	MinimumAmount    string
	MaxSnapshotAge   time.Duration
	OperationTimeout time.Duration
	IdleInterval     time.Duration
	RetryMin         time.Duration
	RetryMax         time.Duration
}

// LoadSweepPlanner 加载归集规划进程配置。
func LoadSweepPlanner() (SweepPlannerConfig, error) {
	databaseURL, err := requiredEnv("GATEWAY_DATABASE_URL")
	if err != nil {
		return SweepPlannerConfig{}, err
	}
	assetID, err := requiredEnv("GATEWAY_SWEEP_ASSET_ID")
	if err != nil {
		return SweepPlannerConfig{}, err
	}
	minimumAmount, err := requiredEnv("GATEWAY_SWEEP_MINIMUM_AMOUNT")
	if err != nil {
		return SweepPlannerConfig{}, err
	}
	amount, ok := new(big.Int).SetString(strings.TrimSpace(minimumAmount), 10)
	if !ok || amount.Sign() <= 0 || len(amount.String()) > 78 {
		return SweepPlannerConfig{}, fmt.Errorf("GATEWAY_SWEEP_MINIMUM_AMOUNT 必须是最多 78 位的正整数最小单位金额")
	}
	maxSnapshotAge, err := durationFromEnv("GATEWAY_SWEEP_MAX_SNAPSHOT_AGE", defaultSweepMaxSnapshotAge)
	if err != nil {
		return SweepPlannerConfig{}, err
	}
	operationTimeout, err := durationFromEnv("GATEWAY_SWEEP_PLANNER_OPERATION_TIMEOUT", defaultSweepPlannerOperationTimeout)
	if err != nil {
		return SweepPlannerConfig{}, err
	}
	idleInterval, err := durationFromEnv("GATEWAY_SWEEP_PLANNER_IDLE_INTERVAL", defaultSweepPlannerIdleInterval)
	if err != nil {
		return SweepPlannerConfig{}, err
	}
	retryMin, err := durationFromEnv("GATEWAY_SWEEP_PLANNER_RETRY_MIN", defaultSweepPlannerRetryMin)
	if err != nil {
		return SweepPlannerConfig{}, err
	}
	retryMax, err := durationFromEnv("GATEWAY_SWEEP_PLANNER_RETRY_MAX", defaultSweepPlannerRetryMax)
	if err != nil {
		return SweepPlannerConfig{}, err
	}
	if retryMax < retryMin {
		return SweepPlannerConfig{}, fmt.Errorf("GATEWAY_SWEEP_PLANNER_RETRY_MAX 不能小于 GATEWAY_SWEEP_PLANNER_RETRY_MIN")
	}
	return SweepPlannerConfig{
		DatabaseURL: databaseURL, AssetID: assetID, MinimumAmount: amount.String(),
		MaxSnapshotAge: maxSnapshotAge, OperationTimeout: operationTimeout,
		IdleInterval: idleInterval, RetryMin: retryMin, RetryMax: retryMax,
	}, nil
}
