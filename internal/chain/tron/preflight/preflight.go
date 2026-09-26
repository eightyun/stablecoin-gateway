// Package preflight 在不持有私钥的前提下验证 TRON 节点和资产配置。
package preflight

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/chain/tron"
)

var ErrCheckFailed = errors.New("TRON 预检失败")

// FullNode 提供最新链头和合约只读调用。
type FullNode interface {
	Head(context.Context) (tron.Header, error)
	TokenMetadata(context.Context, string) (tron.TokenMetadata, error)
}

// SolidityNode 提供已固化链头。
type SolidityNode interface {
	SolidifiedHead(context.Context) (tron.Header, error)
}

// Config 是预检的安全阈值和预期资产元数据。
type Config struct {
	Network          string
	ContractAddress  string
	ExpectedSymbol   string
	ExpectedDecimals uint8
	MaxFinalizedLag  uint64
	MaxHeadAge       time.Duration
	MaxFutureSkew    time.Duration
}

// Result 是通过预检时的节点快照。
type Result struct {
	Network         string
	ContractAddress string
	Symbol          string
	Decimals        uint8
	Head            tron.Header
	SolidifiedHead  tron.Header
	FinalizedLag    uint64
}

// Check 执行只读测试网预检。
func Check(ctx context.Context, fullNode FullNode, solidityNode SolidityNode, config Config, now time.Time) (Result, error) {
	config.Network = strings.TrimSpace(config.Network)
	config.ContractAddress = strings.TrimSpace(config.ContractAddress)
	config.ExpectedSymbol = strings.TrimSpace(config.ExpectedSymbol)
	if fullNode == nil || solidityNode == nil || config.Network == "" || config.ContractAddress == "" ||
		config.ExpectedSymbol == "" || config.MaxFinalizedLag == 0 || config.MaxHeadAge <= 0 ||
		config.MaxFutureSkew < 0 || now.IsZero() {
		return Result{}, ErrCheckFailed
	}
	head, err := fullNode.Head(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("读取 FullNode 链头: %w", err)
	}
	solidifiedHead, err := solidityNode.SolidifiedHead(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("读取 SolidityNode 链头: %w", err)
	}
	if head.Height < solidifiedHead.Height {
		return Result{}, fmt.Errorf("固化高度 %d 超过最新高度 %d: %w", solidifiedHead.Height, head.Height, ErrCheckFailed)
	}
	lag := head.Height - solidifiedHead.Height
	if lag > config.MaxFinalizedLag {
		return Result{}, fmt.Errorf("固化落后 %d 个区块，阈值 %d: %w", lag, config.MaxFinalizedLag, ErrCheckFailed)
	}
	now = now.UTC()
	for name, header := range map[string]tron.Header{"FullNode": head, "SolidityNode": solidifiedHead} {
		if header.Timestamp.After(now.Add(config.MaxFutureSkew)) || now.Sub(header.Timestamp) > config.MaxHeadAge {
			return Result{}, fmt.Errorf("%s 链头时间 %s 异常: %w", name, header.Timestamp, ErrCheckFailed)
		}
	}
	metadata, err := fullNode.TokenMetadata(ctx, config.ContractAddress)
	if err != nil {
		return Result{}, fmt.Errorf("读取目标 TRC20 元数据: %w", err)
	}
	if metadata.Symbol != config.ExpectedSymbol || metadata.Decimals != config.ExpectedDecimals {
		return Result{}, fmt.Errorf(
			"TRC20 元数据为 %s/%d，期望 %s/%d: %w",
			metadata.Symbol, metadata.Decimals, config.ExpectedSymbol, config.ExpectedDecimals, ErrCheckFailed,
		)
	}
	return Result{
		Network: config.Network, ContractAddress: config.ContractAddress,
		Symbol: metadata.Symbol, Decimals: metadata.Decimals,
		Head: head, SolidifiedHead: solidifiedHead, FinalizedLag: lag,
	}, nil
}
