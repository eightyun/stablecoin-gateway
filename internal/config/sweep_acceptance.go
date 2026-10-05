package config

import (
	"fmt"
	"math/big"
	"os"
	"strings"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/chain/tron"
)

const (
	defaultSweepAcceptanceTimeout      = 15 * time.Minute
	defaultSweepAcceptancePollInterval = 2 * time.Second
)

// SweepAcceptanceConfig 保存测试网归集端到端验收配置。
type SweepAcceptanceConfig struct {
	Planner            SweepPlannerConfig
	Signing            SweepSigningWorkerConfig
	Execution          SweepExecutionWorkerConfig
	SourceAddress      string
	DestinationAddress string
	MaximumAmount      string
	Timeout            time.Duration
	PollInterval       time.Duration
}

// LoadSweepAcceptance 加载受限于 Nile 或 Shasta 的归集端到端验收配置。
func LoadSweepAcceptance() (SweepAcceptanceConfig, error) {
	planner, err := LoadSweepPlanner()
	if err != nil {
		return SweepAcceptanceConfig{}, err
	}
	signing, err := LoadSweepSigningWorker()
	if err != nil {
		return SweepAcceptanceConfig{}, err
	}
	execution, err := LoadSweepExecutionWorker()
	if err != nil {
		return SweepAcceptanceConfig{}, err
	}
	if signing.Network != execution.Network || signing.SolidityNodeURL != execution.SolidityNodeURL ||
		planner.DatabaseURL != signing.DatabaseURL || planner.DatabaseURL != execution.DatabaseURL {
		return SweepAcceptanceConfig{}, fmt.Errorf("归集验收组件配置不一致")
	}
	if signing.Network != "tron-nile" && signing.Network != "tron-shasta" {
		return SweepAcceptanceConfig{}, fmt.Errorf("归集验收只允许 tron-nile 或 tron-shasta")
	}
	sourceAddress, err := requiredTRONAddress("GATEWAY_SWEEP_ACCEPTANCE_SOURCE_ADDRESS")
	if err != nil {
		return SweepAcceptanceConfig{}, err
	}
	destinationAddress, err := requiredTRONAddress("GATEWAY_SWEEP_ACCEPTANCE_DESTINATION_ADDRESS")
	if err != nil {
		return SweepAcceptanceConfig{}, err
	}
	if sourceAddress == destinationAddress {
		return SweepAcceptanceConfig{}, fmt.Errorf("归集验收来源和目标地址不能相同")
	}
	maximumAmount, err := requiredEnv("GATEWAY_SWEEP_ACCEPTANCE_MAX_AMOUNT")
	if err != nil {
		return SweepAcceptanceConfig{}, err
	}
	maximum, ok := new(big.Int).SetString(strings.TrimSpace(maximumAmount), 10)
	minimum, _ := new(big.Int).SetString(planner.MinimumAmount, 10)
	if !ok || maximum.Sign() <= 0 || len(maximum.String()) > 78 || maximum.Cmp(minimum) < 0 {
		return SweepAcceptanceConfig{}, fmt.Errorf("GATEWAY_SWEEP_ACCEPTANCE_MAX_AMOUNT 必须是至少等于归集阈值的最多 78 位正整数")
	}
	timeout, err := durationFromEnv("GATEWAY_SWEEP_ACCEPTANCE_TIMEOUT", defaultSweepAcceptanceTimeout)
	if err != nil {
		return SweepAcceptanceConfig{}, err
	}
	pollInterval, err := durationFromEnv(
		"GATEWAY_SWEEP_ACCEPTANCE_POLL_INTERVAL", defaultSweepAcceptancePollInterval,
	)
	if err != nil {
		return SweepAcceptanceConfig{}, err
	}
	if timeout < time.Minute || pollInterval >= timeout {
		return SweepAcceptanceConfig{}, fmt.Errorf("归集验收超时必须不少于 1 分钟且大于轮询间隔")
	}
	return SweepAcceptanceConfig{
		Planner: planner, Signing: signing, Execution: execution,
		SourceAddress: sourceAddress, DestinationAddress: destinationAddress,
		MaximumAmount: maximum.String(), Timeout: timeout, PollInterval: pollInterval,
	}, nil
}

func requiredTRONAddress(name string) (string, error) {
	value := strings.TrimSpace(os.Getenv(name))
	address, err := tron.NormalizeAddressHex(value)
	if err != nil {
		return "", fmt.Errorf("配置 %s 必须是有效 TRON 地址", name)
	}
	return address, nil
}
