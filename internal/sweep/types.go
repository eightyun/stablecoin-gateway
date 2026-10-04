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
	AssetID        string
	MinimumAmount  string
	MaxSnapshotAge time.Duration
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
	amount, ok := new(big.Int).SetString(policy.MinimumAmount, 10)
	if !ok || amount.Sign() <= 0 || len(amount.String()) > 78 {
		return ErrInvalidPolicy
	}
	return nil
}
