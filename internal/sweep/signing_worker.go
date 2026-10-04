package sweep

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/background"
	"github.com/eightyun/stablecoin-gateway/internal/chain/tron"
)

var (
	ErrInvalidSigningWorker      = errors.New("归集签名 Worker 配置无效")
	ErrBalanceHeadChanged        = errors.New("归集余额读取期间固化链头发生变化")
	ErrInsufficientSourceBalance = errors.New("归集来源地址固化余额不足")
)

// SigningQueue 定义归集签名 Worker 所需的租约队列。
type SigningQueue interface {
	ClaimSigning(context.Context, string, string, time.Duration) (SigningClaim, error)
	CompleteSigning(context.Context, SigningClaim, BalanceObservation, tron.SignedTransaction) error
	ReleaseSigning(context.Context, SigningClaim, string) error
}

// SigningWorkerConfig 控制归集签名任务的超时、租约和退避。
type SigningWorkerConfig struct {
	Network          string
	OperationTimeout time.Duration
	LeaseDuration    time.Duration
	IdleInterval     time.Duration
	RetryMin         time.Duration
	RetryMax         time.Duration
	Observer         background.Observer
}

// SigningWorker 在隔离签名之前重新核对来源地址固化余额。
type SigningWorker struct {
	runner *background.Runner
}

// NewSigningWorker 创建归集签名 Worker。
func NewSigningWorker(
	queue SigningQueue,
	reader tron.TokenBalanceReader,
	signer tron.SweepSigner,
	logger *slog.Logger,
	workerID string,
	config SigningWorkerConfig,
) (*SigningWorker, error) {
	workerID = strings.TrimSpace(workerID)
	config.Network = strings.TrimSpace(config.Network)
	if queue == nil || reader == nil || signer == nil || logger == nil || workerID == "" || len(workerID) > 128 ||
		config.Network == "" || len(config.Network) > 128 ||
		config.OperationTimeout <= 0 || config.LeaseDuration <= config.OperationTimeout ||
		config.IdleInterval <= 0 || config.RetryMin <= 0 || config.RetryMax < config.RetryMin {
		return nil, ErrInvalidSigningWorker
	}
	operation := &sweepSigningOperation{
		queue: queue, reader: reader, signer: signer, logger: logger,
		workerID: workerID, network: config.Network, leaseDuration: config.LeaseDuration,
	}
	runner, err := background.NewRunner(operation, logger, background.Config{
		Name: "sweep-signing", OperationTimeout: config.OperationTimeout,
		IdleInterval: config.IdleInterval, RetryMin: config.RetryMin, RetryMax: config.RetryMax,
		Observer: config.Observer,
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

// Run 持续执行归集签名任务。
func (worker *SigningWorker) Run(ctx context.Context) error {
	return worker.runner.Run(ctx)
}

type sweepSigningOperation struct {
	queue         SigningQueue
	reader        tron.TokenBalanceReader
	signer        tron.SweepSigner
	logger        *slog.Logger
	workerID      string
	network       string
	leaseDuration time.Duration
}

func (operation *sweepSigningOperation) RunOnce(ctx context.Context) (bool, error) {
	claim, err := operation.queue.ClaimSigning(ctx, operation.workerID, operation.network, operation.leaseDuration)
	if errors.Is(err, ErrNoSigningJob) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	observation, err := operation.observeBalance(ctx, claim)
	if err != nil {
		return true, operation.release(ctx, claim, balanceFailureCode(err), err)
	}
	transaction, err := operation.signer.SignSweep(ctx, tron.SweepSignRequest{
		RequestID: claim.PlanID, Network: claim.Network, SourceAddress: claim.SourceAddress,
		ContractAddress: claim.ContractAddress, DestinationAddress: claim.DestinationAddress,
		Amount: claim.Amount,
	})
	if err != nil {
		return true, operation.release(ctx, claim, "signer_error", fmt.Errorf("隔离归集签名服务失败: %w", err))
	}
	if err := operation.queue.CompleteSigning(ctx, claim, observation, transaction); err != nil {
		return true, err
	}
	operation.logger.Info("归集签名交易已持久化", "plan_id", claim.PlanID,
		"transaction_id", transaction.ID, "signing_attempt", claim.LeaseEpoch,
		"observed_balance", observation.Balance, "observed_block_height", observation.BlockHeight)
	return true, nil
}

func (operation *sweepSigningOperation) observeBalance(ctx context.Context, claim SigningClaim) (BalanceObservation, error) {
	start, err := operation.reader.SolidifiedHead(ctx)
	if err != nil {
		return BalanceObservation{}, fmt.Errorf("读取归集余额起始固化头: %w", err)
	}
	balance, err := operation.reader.TokenBalance(ctx, claim.ContractAddress, claim.SourceAddress)
	if err != nil {
		return BalanceObservation{}, fmt.Errorf("读取归集来源余额: %w", err)
	}
	end, err := operation.reader.SolidifiedHead(ctx)
	if err != nil {
		return BalanceObservation{}, fmt.Errorf("读取归集余额结束固化头: %w", err)
	}
	if start != end || start.Height == 0 || start.Height > math.MaxInt64 || !transactionIDPattern.MatchString(start.Hash) {
		return BalanceObservation{}, ErrBalanceHeadChanged
	}
	balanceValue, balanceOK := parsePositiveAmount(balance)
	required, requiredOK := parsePositiveAmount(claim.Amount)
	if !balanceOK || !requiredOK || balanceValue.Cmp(required) < 0 {
		return BalanceObservation{}, ErrInsufficientSourceBalance
	}
	return BalanceObservation{Balance: balance, BlockHeight: int64(start.Height), BlockHash: start.Hash}, nil
}

func (operation *sweepSigningOperation) release(
	ctx context.Context,
	claim SigningClaim,
	code string,
	cause error,
) error {
	if releaseErr := operation.queue.ReleaseSigning(ctx, claim, code); releaseErr != nil {
		return errors.Join(cause, releaseErr)
	}
	return cause
}

func balanceFailureCode(err error) string {
	switch {
	case errors.Is(err, ErrBalanceHeadChanged):
		return "balance_head_changed"
	case errors.Is(err, ErrInsufficientSourceBalance):
		return "insufficient_source_balance"
	default:
		return "balance_reader_error"
	}
}
