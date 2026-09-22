// Package tron 定义 TRON 链读取与广播的最小业务边界。
package tron

import (
	"context"
	"errors"
)

var ErrBlockNotFound = errors.New("区块不存在")

// Header 标识一条链上的区块。Hash 与 ParentHash 使用节点返回的规范化表示。
type Header struct {
	Height     uint64
	Hash       string
	ParentHash string
}

// EventID 标识一条 TRC20 日志。LogIndex 是交易 Receipt 中的原始日志位置。
type EventID struct {
	Network       string
	Contract      string
	TransactionID string
	LogIndex      uint32
}

// Transfer 是已验证执行成功的 TRC20 Transfer 日志，Amount 是最小单位十进制整数。
type Transfer struct {
	ID     EventID
	From   string
	To     string
	Amount string
}

// ExecutionOutcome 表示交易执行 Receipt 的结果，而非广播结果。
type ExecutionOutcome uint8

const (
	ExecutionSucceeded ExecutionOutcome = iota + 1
	ExecutionFailed
)

// Receipt 是一个已进入区块的交易执行结果。
type Receipt struct {
	TransactionID string
	Outcome       ExecutionOutcome
}

// Block 包含规范化区块、执行结果和成功交易的 TRC20 Transfer 日志。
type Block struct {
	Header    Header
	Receipts  []Receipt
	Transfers []Transfer
}

// TransactionStatus 区分未发现、待打包、执行成功和执行失败。
// 未发现不证明交易已失败；只有固化的执行结果才可用于资金终态。
type TransactionStatus uint8

const (
	TransactionNotFound TransactionStatus = iota
	TransactionPending
	TransactionSucceeded
	TransactionFailed
)

// TransactionState 保留执行结果所在区块及其固化状态。
type TransactionState struct {
	Status     TransactionStatus
	Block      Header
	Solidified bool
}

// SignedTransaction 是已经由独立签名器完成签名的交易。
// Payload 是提交到节点的序列化交易；适配器不得持有私钥。
type SignedTransaction struct {
	ID      string
	Payload []byte
}

// Reader 提供链头、固化头、按高度重扫和按交易 ID 恢复所需的读取能力。
type Reader interface {
	Head(ctx context.Context) (Header, error)
	SolidifiedHead(ctx context.Context) (Header, error)
	BlockByHeight(ctx context.Context, height uint64) (Block, error)
	Transaction(ctx context.Context, transactionID string) (TransactionState, error)
}

// Broadcaster 只负责将已签名交易交给节点。nil 错误只表示节点已接收，不代表执行成功。
// 错误可能发生在节点接收前或接收后；调用方必须按原交易 ID 查询，而不能直接重建付款。
type Broadcaster interface {
	Broadcast(ctx context.Context, transaction SignedTransaction) error
}
