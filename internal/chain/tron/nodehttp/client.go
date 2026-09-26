// Package nodehttp 通过 TRON SolidityNode HTTP API 读取已固化链数据。
package nodehttp

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/eightyun/stablecoin-gateway/internal/chain/tron"
)

const (
	defaultMaxResponseBytes int64 = 64 << 20
	transferTopic                 = "ddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"
)

var (
	ErrInvalidConfig    = errors.New("TRON 节点配置无效")
	ErrInvalidHeight    = errors.New("TRON 区块高度超出节点接口范围")
	ErrInvalidResponse  = errors.New("TRON 节点响应无效")
	ErrResponseTooLarge = errors.New("TRON 节点响应超过大小限制")
)

// Config 是 SolidityNode HTTP 连接配置。
type Config struct {
	BaseURL          string
	Network          string
	APIKey           string
	MaxResponseBytes int64
}

// Client 读取 TRON FullNode 或 SolidityNode HTTP 接口。
type Client struct {
	baseURL          *url.URL
	network          string
	apiKey           string
	maxResponseBytes int64
	httpClient       *http.Client
}

// Head 返回 FullNode 当前最新链头。
func (client *Client) Head(ctx context.Context) (tron.Header, error) {
	var block wireBlock
	if err := client.post(ctx, "/wallet/getnowblock", struct{}{}, &block); err != nil {
		return tron.Header{}, err
	}
	header, err := parseHeader(block)
	if errors.Is(err, tron.ErrBlockNotFound) {
		return tron.Header{}, fmt.Errorf("读取最新链头: %w", ErrInvalidResponse)
	}
	return header, err
}

var _ tron.FinalizedReader = (*Client)(nil)
var _ tron.FinalizedTransactionReader = (*Client)(nil)

// New 创建 SolidityNode HTTP 客户端。nil HTTP 客户端使用带超时的标准客户端。
func New(config Config, httpClient *http.Client) (*Client, error) {
	baseURL, err := url.Parse(strings.TrimSpace(config.BaseURL))
	if err != nil || (baseURL.Scheme != "http" && baseURL.Scheme != "https") ||
		baseURL.Host == "" || baseURL.User != nil || baseURL.RawQuery != "" || baseURL.Fragment != "" ||
		strings.TrimSpace(config.Network) == "" || config.MaxResponseBytes < 0 {
		return nil, ErrInvalidConfig
	}
	if httpClient == nil {
		httpClient = &http.Client{
			Timeout: 15 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	maxResponseBytes := config.MaxResponseBytes
	if maxResponseBytes == 0 {
		maxResponseBytes = defaultMaxResponseBytes
	}
	baseURL.Path = strings.TrimRight(baseURL.Path, "/")
	return &Client{
		baseURL:          baseURL,
		network:          strings.TrimSpace(config.Network),
		apiKey:           config.APIKey,
		maxResponseBytes: maxResponseBytes,
		httpClient:       httpClient,
	}, nil
}

// SolidifiedHead 返回 SolidityNode 当前已固化链头。
func (client *Client) SolidifiedHead(ctx context.Context) (tron.Header, error) {
	var block wireBlock
	if err := client.post(ctx, "/walletsolidity/getnowblock", struct{}{}, &block); err != nil {
		return tron.Header{}, err
	}
	header, err := parseHeader(block)
	if errors.Is(err, tron.ErrBlockNotFound) {
		return tron.Header{}, fmt.Errorf("读取已固化链头: %w", ErrInvalidResponse)
	}
	return header, err
}

// SolidifiedBlockByHeight 返回指定高度的已固化区块及其执行结果和 TRC20 日志。
func (client *Client) SolidifiedBlockByHeight(ctx context.Context, height uint64) (tron.Block, error) {
	if height > math.MaxInt64 {
		return tron.Block{}, ErrInvalidHeight
	}
	request := struct {
		Number int64 `json:"num"`
	}{Number: int64(height)}
	var blockResponse wireBlock
	if err := client.post(ctx, "/walletsolidity/getblockbynum", request, &blockResponse); err != nil {
		return tron.Block{}, err
	}
	header, err := parseHeader(blockResponse)
	if err != nil {
		return tron.Block{}, err
	}
	if header.Height != height {
		return tron.Block{}, fmt.Errorf("请求高度 %d 返回高度 %d: %w", height, header.Height, ErrInvalidResponse)
	}
	transactions, err := parseTransactions(blockResponse.Transactions)
	if err != nil {
		return tron.Block{}, err
	}
	if len(transactions) == 0 {
		return tron.Block{Header: header}, nil
	}

	var infoResponse []wireTransactionInfo
	if err := client.post(ctx, "/walletsolidity/gettransactioninfobyblocknum", request, &infoResponse); err != nil {
		return tron.Block{}, err
	}
	infos, err := parseTransactionInfos(height, infoResponse)
	if err != nil || len(infos) != len(transactions) {
		if err != nil {
			return tron.Block{}, err
		}
		return tron.Block{}, fmt.Errorf("区块交易数 %d，执行结果数 %d: %w", len(transactions), len(infos), ErrInvalidResponse)
	}

	block := tron.Block{Header: header, Receipts: make([]tron.Receipt, 0, len(transactions))}
	for _, transaction := range transactions {
		info, found := infos[transaction.id]
		if !found {
			return tron.Block{}, fmt.Errorf("交易 %s 缺少执行结果: %w", transaction.id, ErrInvalidResponse)
		}
		outcome, err := executionOutcome(transaction.success, info)
		if err != nil {
			return tron.Block{}, fmt.Errorf("交易 %s: %w", transaction.id, err)
		}
		block.Receipts = append(block.Receipts, tron.Receipt{TransactionID: transaction.id, Outcome: outcome})
		if outcome != tron.ExecutionSucceeded {
			continue
		}
		transfers, err := client.parseTransfers(transaction.id, info.Logs)
		if err != nil {
			return tron.Block{}, err
		}
		block.Transfers = append(block.Transfers, transfers...)
	}
	return block, nil
}

// Transaction 查询 SolidityNode 中已固化的交易体和智能合约执行回执。
func (client *Client) Transaction(ctx context.Context, transactionID string) (tron.TransactionState, error) {
	transactionID, err := normalizedHash(transactionID)
	if err != nil {
		return tron.TransactionState{}, err
	}
	request := struct {
		Value string `json:"value"`
	}{Value: transactionID}
	var transaction wireTransaction
	if err := client.post(ctx, "/walletsolidity/gettransactionbyid", request, &transaction); err != nil {
		return tron.TransactionState{}, err
	}
	if transaction.ID == "" {
		if len(transaction.Results) != 0 {
			return tron.TransactionState{}, ErrInvalidResponse
		}
		return tron.TransactionState{Status: tron.TransactionNotFound}, nil
	}
	transactions, err := parseTransactions([]wireTransaction{transaction})
	if err != nil || transactions[0].id != transactionID {
		return tron.TransactionState{}, ErrInvalidResponse
	}
	var info wireTransactionInfo
	if err := client.post(ctx, "/walletsolidity/gettransactioninfobyid", request, &info); err != nil {
		return tron.TransactionState{}, err
	}
	if info.ID == "" {
		if info.BlockNumber != 0 || info.Result != "" || info.Receipt.Result != "" || len(info.Logs) != 0 {
			return tron.TransactionState{}, ErrInvalidResponse
		}
		return tron.TransactionState{Status: tron.TransactionPending, Solidified: true}, nil
	}
	infoID, err := normalizedHash(info.ID)
	if err != nil || infoID != transactionID || info.BlockNumber < 0 {
		return tron.TransactionState{}, ErrInvalidResponse
	}
	outcome, err := executionOutcome(transactions[0].success, info)
	if err != nil {
		return tron.TransactionState{}, err
	}
	status := tron.TransactionFailed
	if outcome == tron.ExecutionSucceeded {
		status = tron.TransactionSucceeded
	}
	return tron.TransactionState{
		Status: status, Block: tron.Header{Height: uint64(info.BlockNumber)}, Solidified: true,
	}, nil
}

// TokenMetadata 通过 FullNode 常量调用读取 TRC20 symbol 和 decimals。
func (client *Client) TokenMetadata(ctx context.Context, contractAddress string) (tron.TokenMetadata, error) {
	contractAddress, err := tron.NormalizeAddress(contractAddress)
	if err != nil {
		return tron.TokenMetadata{}, err
	}
	decimalsResult, err := client.constantCall(ctx, contractAddress, "decimals()")
	if err != nil {
		return tron.TokenMetadata{}, fmt.Errorf("读取 TRC20 decimals: %w", err)
	}
	decimals, err := decodeABIDecimals(decimalsResult)
	if err != nil {
		return tron.TokenMetadata{}, err
	}
	symbolResult, err := client.constantCall(ctx, contractAddress, "symbol()")
	if err != nil {
		return tron.TokenMetadata{}, fmt.Errorf("读取 TRC20 symbol: %w", err)
	}
	symbol, err := decodeABIString(symbolResult)
	if err != nil {
		return tron.TokenMetadata{}, err
	}
	return tron.TokenMetadata{Symbol: symbol, Decimals: decimals}, nil
}

func (client *Client) constantCall(ctx context.Context, contractAddress, selector string) (string, error) {
	request := struct {
		OwnerAddress    string `json:"owner_address"`
		ContractAddress string `json:"contract_address"`
		Function        string `json:"function_selector"`
		Visible         bool   `json:"visible"`
	}{
		OwnerAddress:    "T9yD14Nj9j7xAB4dbGeiX9h8unkKHxuWwb",
		ContractAddress: contractAddress,
		Function:        selector,
		Visible:         true,
	}
	var response struct {
		ConstantResult []string `json:"constant_result"`
		Result         struct {
			Accepted *bool `json:"result"`
		} `json:"result"`
	}
	if err := client.post(ctx, "/wallet/triggerconstantcontract", request, &response); err != nil {
		return "", err
	}
	if response.Result.Accepted == nil || !*response.Result.Accepted || len(response.ConstantResult) != 1 {
		return "", ErrInvalidResponse
	}
	return response.ConstantResult[0], nil
}

func decodeABIDecimals(value string) (uint8, error) {
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 32 || !bytes.Equal(decoded[:31], make([]byte, 31)) {
		return 0, ErrInvalidResponse
	}
	return decoded[31], nil
}

func decodeABIString(value string) (string, error) {
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) < 64 || len(decoded)%32 != 0 {
		return "", ErrInvalidResponse
	}
	offsetValue := new(big.Int).SetBytes(decoded[:32])
	if !offsetValue.IsInt64() {
		return "", ErrInvalidResponse
	}
	offset := offsetValue.Int64()
	if offset < 32 || offset%32 != 0 || offset > int64(len(decoded)-32) {
		return "", ErrInvalidResponse
	}
	lengthValue := new(big.Int).SetBytes(decoded[offset : offset+32])
	if !lengthValue.IsInt64() {
		return "", ErrInvalidResponse
	}
	length := lengthValue.Int64()
	start := offset + 32
	if length <= 0 || length > 64 || start > int64(len(decoded))-length {
		return "", ErrInvalidResponse
	}
	symbolBytes := decoded[start : start+length]
	if !utf8.Valid(symbolBytes) {
		return "", ErrInvalidResponse
	}
	symbol := string(symbolBytes)
	for _, character := range symbol {
		if character < 0x21 || character > 0x7e {
			return "", ErrInvalidResponse
		}
	}
	return symbol, nil
}

type wireBlock struct {
	BlockID     string `json:"blockID"`
	BlockHeader struct {
		RawData struct {
			Number     uint64 `json:"number"`
			ParentHash string `json:"parentHash"`
			Timestamp  int64  `json:"timestamp"`
		} `json:"raw_data"`
	} `json:"block_header"`
	Transactions []wireTransaction `json:"transactions"`
}

type wireTransaction struct {
	ID      string `json:"txID"`
	Results []struct {
		ContractResult string `json:"contractRet"`
	} `json:"ret"`
}

type wireTransactionInfo struct {
	ID          string `json:"id"`
	BlockNumber int64  `json:"blockNumber"`
	Result      string `json:"result"`
	Receipt     struct {
		Result string `json:"result"`
	} `json:"receipt"`
	Logs []wireLog `json:"log"`
}

type wireLog struct {
	Address string   `json:"address"`
	Topics  []string `json:"topics"`
	Data    string   `json:"data"`
}

type transactionSummary struct {
	id      string
	success bool
}

func parseHeader(block wireBlock) (tron.Header, error) {
	if block.BlockID == "" {
		return tron.Header{}, tron.ErrBlockNotFound
	}
	hash, err := normalizedHash(block.BlockID)
	if err != nil {
		return tron.Header{}, err
	}
	expectedPrefix := fmt.Sprintf("%016x", block.BlockHeader.RawData.Number)
	if !strings.HasPrefix(hash, expectedPrefix) {
		return tron.Header{}, fmt.Errorf("区块 ID 与高度不一致: %w", ErrInvalidResponse)
	}
	if block.BlockHeader.RawData.Timestamp <= 0 {
		return tron.Header{}, fmt.Errorf("区块时间无效: %w", ErrInvalidResponse)
	}
	blockTime := time.UnixMilli(block.BlockHeader.RawData.Timestamp).UTC()
	if blockTime.Year() < 2018 || blockTime.Year() > 9999 {
		return tron.Header{}, fmt.Errorf("区块时间超出支持范围: %w", ErrInvalidResponse)
	}
	parentHash := ""
	if block.BlockHeader.RawData.Number > 0 {
		parentHash, err = normalizedHash(block.BlockHeader.RawData.ParentHash)
		if err != nil {
			return tron.Header{}, err
		}
	}
	return tron.Header{Height: block.BlockHeader.RawData.Number, Hash: hash, ParentHash: parentHash, Timestamp: blockTime}, nil
}

func parseTransactions(input []wireTransaction) ([]transactionSummary, error) {
	transactions := make([]transactionSummary, 0, len(input))
	seen := make(map[string]struct{}, len(input))
	for _, transaction := range input {
		id, err := normalizedHash(transaction.ID)
		if err != nil {
			return nil, fmt.Errorf("交易 ID: %w", err)
		}
		if _, exists := seen[id]; exists || len(transaction.Results) == 0 {
			return nil, fmt.Errorf("交易 %s 重复或缺少执行状态: %w", id, ErrInvalidResponse)
		}
		seen[id] = struct{}{}
		success := true
		for _, result := range transaction.Results {
			if result.ContractResult != "SUCCESS" {
				success = false
			}
		}
		transactions = append(transactions, transactionSummary{id: id, success: success})
	}
	return transactions, nil
}

func parseTransactionInfos(height uint64, input []wireTransactionInfo) (map[string]wireTransactionInfo, error) {
	infos := make(map[string]wireTransactionInfo, len(input))
	for _, info := range input {
		id, err := normalizedHash(info.ID)
		if err != nil || info.BlockNumber < 0 || uint64(info.BlockNumber) != height {
			return nil, fmt.Errorf("交易执行结果不属于目标区块: %w", ErrInvalidResponse)
		}
		if _, exists := infos[id]; exists {
			return nil, fmt.Errorf("交易执行结果 %s 重复: %w", id, ErrInvalidResponse)
		}
		info.ID = id
		infos[id] = info
	}
	return infos, nil
}

func executionOutcome(blockSuccess bool, info wireTransactionInfo) (tron.ExecutionOutcome, error) {
	infoSuccess := info.Result == "" || info.Result == "SUCESS"
	receiptSuccess := info.Receipt.Result == "SUCCESS"
	if info.Result != "" && info.Result != "SUCESS" && info.Result != "FAILED" {
		return 0, fmt.Errorf("未知交易结果 %q: %w", info.Result, ErrInvalidResponse)
	}
	if info.Receipt.Result != "" && info.Receipt.Result != "SUCCESS" && blockSuccess {
		return 0, fmt.Errorf("区块与 Receipt 执行结果不一致: %w", ErrInvalidResponse)
	}
	if receiptSuccess && !blockSuccess {
		return 0, fmt.Errorf("区块与 Receipt 执行结果不一致: %w", ErrInvalidResponse)
	}
	if blockSuccess && infoSuccess && (receiptSuccess || info.Receipt.Result == "") {
		return tron.ExecutionSucceeded, nil
	}
	return tron.ExecutionFailed, nil
}

func (client *Client) parseTransfers(transactionID string, logs []wireLog) ([]tron.Transfer, error) {
	transfers := make([]tron.Transfer, 0)
	for index, log := range logs {
		if len(log.Topics) == 0 || !strings.EqualFold(log.Topics[0], transferTopic) {
			continue
		}
		if uint64(index) > uint64(^uint32(0)) || len(log.Topics) != 3 {
			return nil, fmt.Errorf("交易 %s 的 Transfer 日志结构错误: %w", transactionID, ErrInvalidResponse)
		}
		contract, err := normalizedAddress(log.Address)
		if err != nil {
			return nil, fmt.Errorf("交易 %s 的合约地址: %w", transactionID, err)
		}
		from, err := topicAddress(log.Topics[1])
		if err != nil {
			return nil, fmt.Errorf("交易 %s 的付款地址: %w", transactionID, err)
		}
		to, err := topicAddress(log.Topics[2])
		if err != nil {
			return nil, fmt.Errorf("交易 %s 的收款地址: %w", transactionID, err)
		}
		amount, err := uint256(log.Data)
		if err != nil {
			return nil, fmt.Errorf("交易 %s 的 Transfer 金额: %w", transactionID, err)
		}
		if amount == "0" {
			continue
		}
		transfers = append(transfers, tron.Transfer{
			ID:   tron.EventID{Network: client.network, Contract: contract, TransactionID: transactionID, LogIndex: uint32(index)},
			From: from, To: to, Amount: amount,
		})
	}
	return transfers, nil
}

func (client *Client) post(ctx context.Context, endpoint string, payload, output any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("编码 TRON 节点请求: %w", err)
	}
	target := *client.baseURL
	target.Path += endpoint
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("创建 TRON 节点请求: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	if client.apiKey != "" {
		request.Header.Set("TRON-PRO-API-KEY", client.apiKey)
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("请求 TRON 节点: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, client.maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("读取 TRON 节点响应: %w", err)
	}
	if int64(len(responseBody)) > client.maxResponseBytes {
		return ErrResponseTooLarge
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("TRON 节点 HTTP 状态 %d: %w", response.StatusCode, ErrInvalidResponse)
	}
	var nodeError struct {
		Error string `json:"Error"`
	}
	if len(responseBody) == 0 || (json.Unmarshal(responseBody, &nodeError) == nil && nodeError.Error != "") {
		return fmt.Errorf("TRON 节点拒绝请求: %s: %w", nodeError.Error, ErrInvalidResponse)
	}
	if err := json.Unmarshal(responseBody, output); err != nil {
		return fmt.Errorf("解析 TRON 节点响应: %w", errors.Join(ErrInvalidResponse, err))
	}
	return nil
}

func normalizedHash(value string) (string, error) {
	value = strings.ToLower(value)
	if len(value) != 64 {
		return "", ErrInvalidResponse
	}
	if _, err := hex.DecodeString(value); err != nil {
		return "", fmt.Errorf("哈希不是十六进制: %w", ErrInvalidResponse)
	}
	return value, nil
}

func normalizedAddress(value string) (string, error) {
	value = strings.ToLower(value)
	if len(value) == 40 {
		value = "41" + value
	}
	if len(value) != 42 || !strings.HasPrefix(value, "41") {
		return "", ErrInvalidResponse
	}
	if _, err := hex.DecodeString(value); err != nil {
		return "", fmt.Errorf("地址不是十六进制: %w", ErrInvalidResponse)
	}
	return value, nil
}

func topicAddress(value string) (string, error) {
	value = strings.ToLower(value)
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 32 || !bytes.Equal(decoded[:12], make([]byte, 12)) {
		return "", ErrInvalidResponse
	}
	return "41" + hex.EncodeToString(decoded[12:]), nil
}

func uint256(value string) (string, error) {
	if len(value) != 64 {
		return "", ErrInvalidResponse
	}
	if _, err := hex.DecodeString(value); err != nil {
		return "", ErrInvalidResponse
	}
	amount, ok := new(big.Int).SetString(value, 16)
	if !ok {
		return "", ErrInvalidResponse
	}
	return amount.Text(10), nil
}
