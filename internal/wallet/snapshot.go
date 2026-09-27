// Package wallet 管理托管钱包登记和链上余额快照。
package wallet

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/chain/tron"
	"github.com/eightyun/stablecoin-gateway/internal/identity"
)

var (
	ErrInvalidSnapshot = errors.New("钱包余额快照无效")
	ErrHeadChanged     = errors.New("采集期间已固化链头发生变化")
)

// Asset 是余额采集所需的链上资产信息。
type Asset struct {
	ID              string
	Network         string
	ContractAddress string
}

// Wallet 是平台控制的链上地址，不包含私钥。
type Wallet struct {
	ID      string
	AssetID string
	Address string
	Role    string
}

// Balance 是一个钱包在指定固化链头的资产余额。
type Balance struct {
	WalletID string
	Amount   string
}

// Snapshot 是一次完整且链头一致的钱包余额采样。
type Snapshot struct {
	ID           string
	Asset        Asset
	Block        tron.Header
	Balances     []Balance
	TotalBalance string
	Ledger       LedgerCheckpoint
}

// CollectSnapshot 串行读取所有钱包余额，并用前后固化头保证采样未跨高度。
func CollectSnapshot(
	ctx context.Context,
	reader tron.TokenBalanceReader,
	asset Asset,
	wallets []Wallet,
) (Snapshot, error) {
	if reader == nil || strings.TrimSpace(asset.ID) == "" || strings.TrimSpace(asset.Network) == "" ||
		strings.TrimSpace(asset.ContractAddress) == "" || len(wallets) == 0 {
		return Snapshot{}, ErrInvalidSnapshot
	}
	if err := validateWalletSet(asset.ID, wallets); err != nil {
		return Snapshot{}, err
	}
	start, err := reader.SolidifiedHead(ctx)
	if err != nil {
		return Snapshot{}, fmt.Errorf("读取采集起始固化头: %w", err)
	}
	if !validHeader(start) {
		return Snapshot{}, ErrInvalidSnapshot
	}

	ordered := append([]Wallet(nil), wallets...)
	sort.Slice(ordered, func(left, right int) bool { return ordered[left].ID < ordered[right].ID })
	balances := make([]Balance, 0, len(ordered))
	total := new(big.Int)
	for _, item := range ordered {
		amount, balanceErr := reader.TokenBalance(ctx, asset.ContractAddress, item.Address)
		if balanceErr != nil {
			return Snapshot{}, fmt.Errorf("读取钱包 %s 余额: %w", item.ID, balanceErr)
		}
		value, valid := parseAmount(amount)
		if !valid {
			return Snapshot{}, ErrInvalidSnapshot
		}
		total.Add(total, value)
		if total.BitLen() > 256 || len(total.String()) > 78 {
			return Snapshot{}, ErrInvalidSnapshot
		}
		balances = append(balances, Balance{WalletID: item.ID, Amount: amount})
	}

	end, err := reader.SolidifiedHead(ctx)
	if err != nil {
		return Snapshot{}, fmt.Errorf("读取采集结束固化头: %w", err)
	}
	if start != end {
		return Snapshot{}, ErrHeadChanged
	}
	snapshotID, err := identity.NewUUID()
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{
		ID: snapshotID, Asset: asset, Block: start, Balances: balances, TotalBalance: total.String(),
	}, nil
}

func validateWalletSet(assetID string, wallets []Wallet) error {
	ids := make(map[string]struct{}, len(wallets))
	addresses := make(map[string]struct{}, len(wallets))
	for _, item := range wallets {
		if !identity.ValidUUID(item.ID) || item.AssetID != assetID || strings.TrimSpace(item.Address) == "" {
			return ErrInvalidSnapshot
		}
		if _, exists := ids[item.ID]; exists {
			return ErrInvalidSnapshot
		}
		if _, exists := addresses[item.Address]; exists {
			return ErrInvalidSnapshot
		}
		ids[item.ID] = struct{}{}
		addresses[item.Address] = struct{}{}
	}
	return nil
}

func validHeader(header tron.Header) bool {
	hash, err := hex.DecodeString(header.Hash)
	return err == nil && len(hash) == 32 && header.Height <= math.MaxInt64 &&
		header.Hash == strings.ToLower(header.Hash) && !header.Timestamp.IsZero() &&
		header.Timestamp.Location() == time.UTC
}

func parseAmount(amount string) (*big.Int, bool) {
	if amount == "" || len(amount) > 78 || (len(amount) > 1 && amount[0] == '0') {
		return nil, false
	}
	for _, digit := range amount {
		if digit < '0' || digit > '9' {
			return nil, false
		}
	}
	value, valid := new(big.Int).SetString(amount, 10)
	return value, valid && value.Sign() >= 0 && value.BitLen() <= 256
}
