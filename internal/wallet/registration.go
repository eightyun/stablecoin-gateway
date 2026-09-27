package wallet

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/chain/tron"
	"github.com/eightyun/stablecoin-gateway/internal/identity"
	"github.com/jackc/pgx/v5"
)

const (
	RoleHot  = "hot"
	RoleCold = "cold"
	RoleFee  = "fee"
)

var (
	ErrInvalidRegistration = errors.New("托管钱包登记请求无效")
	ErrWalletConflict      = errors.New("托管钱包已存在但登记信息不一致")
)

// RegisterRequest 是一次带操作人和原因的非充值托管钱包登记。
type RegisterRequest struct {
	AssetID string
	Address string
	Role    string
	Actor   string
	Reason  string
}

// RegisterResult 返回规范化钱包信息和严格幂等结果。
type RegisterResult struct {
	WalletID     string    `json:"wallet_id"`
	AssetID      string    `json:"asset_id"`
	Address      string    `json:"address"`
	Role         string    `json:"role"`
	Actor        string    `json:"actor"`
	Reason       string    `json:"reason"`
	RegisteredAt time.Time `json:"registered_at"`
	Created      bool      `json:"created"`
}

// Register 原子登记非充值托管钱包及不可变审计。同一请求可安全重试。
func (store *Store) Register(ctx context.Context, request RegisterRequest) (result RegisterResult, err error) {
	request, err = normalizeRegisterRequest(request)
	if err != nil {
		return RegisterResult{}, err
	}
	transaction, err := store.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return RegisterResult{}, fmt.Errorf("开始托管钱包登记事务: %w", err)
	}
	defer func() {
		if rollbackErr := transaction.Rollback(context.Background()); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			err = errors.Join(err, fmt.Errorf("回滚托管钱包登记事务: %w", rollbackErr))
		}
	}()

	var network string
	if err = transaction.QueryRow(ctx, `
		SELECT network
		FROM assets
		WHERE id = $1 AND status = 'active'
		FOR SHARE
	`, request.AssetID).Scan(&network); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RegisterResult{}, ErrAssetUnavailable
		}
		return RegisterResult{}, fmt.Errorf("锁定托管钱包资产: %w", err)
	}
	if network != "tron-mainnet" && network != "tron-nile" && network != "tron-shasta" {
		return RegisterResult{}, ErrInvalidRegistration
	}

	walletID, err := identity.NewUUID()
	if err != nil {
		return RegisterResult{}, err
	}
	var insertedID string
	err = transaction.QueryRow(ctx, `
		INSERT INTO custody_wallets (id, asset_id, address, role, status)
		VALUES ($1, $2, $3, $4, 'active')
		ON CONFLICT DO NOTHING
		RETURNING id::TEXT
	`, walletID, request.AssetID, request.Address, request.Role).Scan(&insertedID)
	if err == nil {
		auditID, auditErr := identity.NewUUID()
		if auditErr != nil {
			return RegisterResult{}, auditErr
		}
		result = registerResult(insertedID, request, true)
		if err = transaction.QueryRow(ctx, `
			INSERT INTO custody_wallet_registration_audits (id, wallet_id, actor, reason)
			VALUES ($1, $2, $3, $4)
			RETURNING created_at
		`, auditID, insertedID, request.Actor, request.Reason).Scan(&result.RegisteredAt); err != nil {
			return RegisterResult{}, fmt.Errorf("记录托管钱包登记审计: %w", err)
		}
		if err = transaction.Commit(ctx); err != nil {
			return RegisterResult{}, fmt.Errorf("提交托管钱包登记: %w", err)
		}
		return result, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return RegisterResult{}, fmt.Errorf("登记托管钱包: %w", err)
	}

	var existingRole, status, actor, reason string
	var registeredAt time.Time
	err = transaction.QueryRow(ctx, `
		SELECT wallet.id::TEXT, wallet.role, wallet.status,
		       audit.actor, audit.reason, audit.created_at
		FROM custody_wallets AS wallet
		JOIN custody_wallet_registration_audits AS audit ON audit.wallet_id = wallet.id
		WHERE wallet.asset_id = $1 AND wallet.address = $2
		FOR SHARE OF wallet
	`, request.AssetID, request.Address).Scan(
		&insertedID, &existingRole, &status, &actor, &reason, &registeredAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return RegisterResult{}, ErrWalletConflict
	}
	if err != nil {
		return RegisterResult{}, fmt.Errorf("查询已有托管钱包: %w", err)
	}
	if existingRole != request.Role || status != "active" || actor != request.Actor || reason != request.Reason {
		return RegisterResult{}, ErrWalletConflict
	}
	result = registerResult(insertedID, request, false)
	result.RegisteredAt = registeredAt
	if err = transaction.Commit(ctx); err != nil {
		return RegisterResult{}, fmt.Errorf("提交托管钱包幂等查询: %w", err)
	}
	return result, nil
}

func normalizeRegisterRequest(request RegisterRequest) (RegisterRequest, error) {
	request.AssetID = strings.TrimSpace(request.AssetID)
	request.Role = strings.TrimSpace(request.Role)
	request.Actor = strings.TrimSpace(request.Actor)
	request.Reason = strings.TrimSpace(request.Reason)
	address, err := tron.NormalizeAddressHex(request.Address)
	if err != nil || request.AssetID == "" || len(request.AssetID) > 128 ||
		(request.Role != RoleHot && request.Role != RoleCold && request.Role != RoleFee) ||
		request.Actor == "" || len(request.Actor) > 128 ||
		request.Reason == "" || len(request.Reason) > 512 {
		return RegisterRequest{}, ErrInvalidRegistration
	}
	request.Address = address
	return request, nil
}

func registerResult(walletID string, request RegisterRequest, created bool) RegisterResult {
	return RegisterResult{
		WalletID: walletID, AssetID: request.AssetID, Address: request.Address,
		Role: request.Role, Actor: request.Actor, Reason: request.Reason, Created: created,
	}
}
