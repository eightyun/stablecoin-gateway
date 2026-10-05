// Package sweep 负责生成可审计的托管钱包归集计划。
package sweep

import (
	"errors"
	"math/big"
	"strings"
	"time"
)

var (
	ErrDatabaseRequired = errors.New("数据库连接不能为空")
	ErrInvalidPolicy    = errors.New("归集策略无效")
	ErrAssetUnavailable = errors.New("归集资产不存在或未启用")
	ErrNoCandidate      = errors.New("没有满足安全条件的归集候选")
	ErrHotWalletCount   = errors.New("资产必须且只能配置一个活动热钱包")
)

// Policy 定义生成归集计划所需的安全策略。
type Policy struct {
	AssetID            string
	MinimumAmount      string
	MaximumAmount      string
	SourceAddress      string
	DestinationAddress string
	MaxSnapshotAge     time.Duration
}

// Plan 是绑定固化余额快照的不可变归集意图。
type Plan struct {
	ID                  string    `json:"id"`
	AssetID             string    `json:"asset_id"`
	SourceWalletID      string    `json:"source_wallet_id"`
	SourceAddress       string    `json:"source_address"`
	DestinationWalletID string    `json:"destination_wallet_id"`
	DestinationAddress  string    `json:"destination_address"`
	SnapshotRunID       string    `json:"snapshot_run_id"`
	SnapshotBlockHeight int64     `json:"snapshot_block_height"`
	Amount              string    `json:"amount"`
	MinimumAmount       string    `json:"minimum_amount"`
	CreatedAt           time.Time `json:"created_at"`
}

func validatePolicy(policy Policy) error {
	policy.AssetID = strings.TrimSpace(policy.AssetID)
	if policy.AssetID == "" || policy.MaxSnapshotAge <= 0 {
		return ErrInvalidPolicy
	}
	if _, ok := parsePositiveAmount(policy.MinimumAmount); !ok {
		return ErrInvalidPolicy
	}
	if policy.MaximumAmount != "" {
		maximum, ok := parsePositiveAmount(policy.MaximumAmount)
		minimum, _ := parsePositiveAmount(policy.MinimumAmount)
		if !ok || maximum.Cmp(minimum) < 0 {
			return ErrInvalidPolicy
		}
	}
	if len(strings.TrimSpace(policy.SourceAddress)) > 256 ||
		len(strings.TrimSpace(policy.DestinationAddress)) > 256 {
		return ErrInvalidPolicy
	}
	return nil
}

func parsePositiveAmount(value string) (*big.Int, bool) {
	if value == "" || len(value) > 78 || (len(value) > 1 && value[0] == '0') {
		return nil, false
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return nil, false
		}
	}
	amount, ok := new(big.Int).SetString(value, 10)
	return amount, ok && amount.Sign() > 0 && amount.BitLen() <= 256
}
