// Package acceptance 提供受控测试网端到端验收所需的安全检查。
package acceptance

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/chain/tron"
	"github.com/eightyun/stablecoin-gateway/internal/sweep"
	"github.com/eightyun/stablecoin-gateway/internal/wallet"
)

var (
	ErrInvalidSweepWallets = errors.New("归集验收钱包配置无效")
	ErrSweepFailed         = errors.New("归集验收链上执行失败")
)

// ExecutionReader 读取归集执行状态。
type ExecutionReader interface {
	GetExecution(context.Context, string) (sweep.ExecutionState, error)
}

// ValidateSweepWallets 确认验收地址与数据库登记的资产、网络和角色一致。
func ValidateSweepWallets(
	asset wallet.Asset,
	wallets []wallet.Wallet,
	network string,
	sourceAddress string,
	destinationAddress string,
) error {
	if strings.TrimSpace(asset.Network) != strings.TrimSpace(network) {
		return ErrInvalidSweepWallets
	}
	source, err := tron.NormalizeAddressHex(sourceAddress)
	if err != nil {
		return ErrInvalidSweepWallets
	}
	destination, err := tron.NormalizeAddressHex(destinationAddress)
	if err != nil || source == destination {
		return ErrInvalidSweepWallets
	}
	var sourceFound, destinationFound bool
	for _, item := range wallets {
		address, normalizeErr := tron.NormalizeAddressHex(item.Address)
		if normalizeErr != nil || item.AssetID != asset.ID {
			return ErrInvalidSweepWallets
		}
		if address == source && item.Role == "deposit" {
			sourceFound = true
		}
		if address == destination && item.Role == "hot" {
			destinationFound = true
		}
	}
	if !sourceFound || !destinationFound {
		return ErrInvalidSweepWallets
	}
	return nil
}

// WaitForSweep 等待归集进入成功或失败终态。
func WaitForSweep(
	ctx context.Context,
	reader ExecutionReader,
	planID string,
	pollInterval time.Duration,
) (sweep.ExecutionState, error) {
	if reader == nil || strings.TrimSpace(planID) == "" || pollInterval <= 0 {
		return sweep.ExecutionState{}, sweep.ErrExecutionNotFound
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		state, err := reader.GetExecution(ctx, planID)
		if err != nil {
			return sweep.ExecutionState{}, err
		}
		switch state.Status {
		case "succeeded":
			return state, nil
		case "failed":
			return state, fmt.Errorf("%w: %s", ErrSweepFailed, state.FailureReason)
		}
		select {
		case <-ctx.Done():
			return sweep.ExecutionState{}, ctx.Err()
		case <-ticker.C:
		}
	}
}
