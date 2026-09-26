package payout

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/background"
	"github.com/eightyun/stablecoin-gateway/internal/chain/tron"
)

var ErrInvalidSigningWorker = errors.New("出款签名 Worker 配置无效")

// SigningQueue 定义签名 Worker 所需的持久化队列能力。
type SigningQueue interface {
	ClaimSigning(context.Context, string, time.Duration) (SigningClaim, error)
	CompleteSigning(context.Context, SigningClaim, tron.SignedTransaction) error
	ReleaseSigning(context.Context, SigningClaim, string) error
}

// SigningWorkerConfig 控制签名任务租约、超时与退避。
type SigningWorkerConfig struct {
	OperationTimeout time.Duration
	LeaseDuration    time.Duration
	IdleInterval     time.Duration
	RetryMin         time.Duration
	RetryMax         time.Duration
}

// SigningWorker 从数据库队列领取任务，但不持有任何私钥。
type SigningWorker struct {
	runner *background.Runner
}

// NewSigningWorker 创建出款签名 Worker。
func NewSigningWorker(
	queue SigningQueue,
	signer tron.TransferSigner,
	logger *slog.Logger,
	workerID string,
	config SigningWorkerConfig,
) (*SigningWorker, error) {
	workerID = strings.TrimSpace(workerID)
	if queue == nil || signer == nil || logger == nil || workerID == "" || len(workerID) > 128 ||
		config.OperationTimeout <= 0 || config.LeaseDuration <= config.OperationTimeout ||
		config.IdleInterval <= 0 || config.RetryMin <= 0 || config.RetryMax < config.RetryMin {
		return nil, ErrInvalidSigningWorker
	}
	operation := &signingOperation{
		queue: queue, signer: signer, logger: logger,
		workerID: workerID, leaseDuration: config.LeaseDuration,
	}
	runner, err := background.NewRunner(operation, logger, background.Config{
		Name: "payout-signing", OperationTimeout: config.OperationTimeout,
		IdleInterval: config.IdleInterval, RetryMin: config.RetryMin, RetryMax: config.RetryMax,
	}, func(err error) background.Decision {
		if errors.Is(err, ErrInvalidSigningClaim) || errors.Is(err, ErrInvalidSignedTransaction) ||
			errors.Is(err, ErrSignedTransactionConflict) {
			return background.Stop
		}
		return background.Retry
	})
	if err != nil {
		return nil, ErrInvalidSigningWorker
	}
	return &SigningWorker{runner: runner}, nil
}

// Run 持续执行签名任务，直到进程退出或遇到安全性错误。
func (worker *SigningWorker) Run(ctx context.Context) error {
	return worker.runner.Run(ctx)
}

type signingOperation struct {
	queue         SigningQueue
	signer        tron.TransferSigner
	logger        *slog.Logger
	workerID      string
	leaseDuration time.Duration
}

func (operation *signingOperation) RunOnce(ctx context.Context) (bool, error) {
	claim, err := operation.queue.ClaimSigning(ctx, operation.workerID, operation.leaseDuration)
	if errors.Is(err, ErrNoSigningJob) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	transaction, err := operation.signer.SignTransfer(ctx, tron.TransferSignRequest{
		RequestID: claim.PayoutID, Network: claim.Network,
		ContractAddress: claim.ContractAddress, DestinationAddress: claim.DestinationAddress,
		Amount: claim.Amount,
	})
	if err != nil {
		releaseErr := operation.queue.ReleaseSigning(ctx, claim, "signer_error")
		if releaseErr != nil {
			return true, errors.Join(fmt.Errorf("隔离签名服务失败: %w", err), releaseErr)
		}
		return true, fmt.Errorf("隔离签名服务失败: %w", err)
	}
	if err := operation.queue.CompleteSigning(ctx, claim, transaction); err != nil {
		return true, err
	}
	operation.logger.Info("出款交易签名已持久化",
		"payout_id", claim.PayoutID,
		"transaction_id", transaction.ID,
		"signing_attempt", claim.LeaseEpoch,
	)
	return true, nil
}
