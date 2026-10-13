package nodehttp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/chain/tron"
	"github.com/eightyun/stablecoin-gateway/internal/chain/tron/nodehttp"
)

const (
	blockHash  = "0000000000000001aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	parentHash = "0000000000000000bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	txID       = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	contract   = "41a614f803b6fd780986a42c78ec9c7f77e6ded13c"
	from       = "411111111111111111111111111111111111111111"
	to         = "412222222222222222222222222222222222222222"
)

var blockTime = time.Unix(1_700_000_000, 0).UTC()

func TestClientReadsFinalizedBlockAndTransfer(t *testing.T) {
	server := newNodeServer(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("TRON-PRO-API-KEY") != "secret" {
			t.Fatalf("API Key = %q", request.Header.Get("TRON-PRO-API-KEY"))
		}
		switch request.URL.Path {
		case "/node/walletsolidity/getnowblock":
			writeJSON(writer, blockJSON(nil))
		case "/node/walletsolidity/getblockbynum":
			writeJSON(writer, blockJSON([]string{"SUCCESS"}))
		case "/node/walletsolidity/gettransactioninfobyblocknum":
			writeJSON(writer, fmt.Sprintf(`[{"id":%q,"blockNumber":1,"receipt":{"result":"SUCCESS"},"log":[{"address":%q,"topics":[%q,%q,%q],"data":%q}]}]`,
				txID, contract[2:], transferTopic(), addressTopic(from), addressTopic(to), uint256Hex(1_000_000)))
		default:
			http.NotFound(writer, request)
		}
	})
	client := newClient(t, server.URL+"/node/", "secret", 0)

	head, err := client.SolidifiedHead(context.Background())
	if err != nil || head != (tron.Header{Height: 1, Hash: blockHash, ParentHash: parentHash, Timestamp: blockTime}) {
		t.Fatalf("SolidifiedHead() = %+v, %v", head, err)
	}
	block, err := client.SolidifiedBlockByHeight(context.Background(), 1)
	if err != nil {
		t.Fatalf("SolidifiedBlockByHeight() error = %v", err)
	}
	if len(block.Receipts) != 1 || block.Receipts[0].Outcome != tron.ExecutionSucceeded {
		t.Fatalf("Receipts = %+v", block.Receipts)
	}
	want := tron.Transfer{
		ID:   tron.EventID{Network: "tron-mainnet", Contract: contract, TransactionID: txID},
		From: from, To: to, Amount: "1000000",
	}
	if len(block.Transfers) != 1 || block.Transfers[0] != want {
		t.Fatalf("Transfers = %+v, 期望 %+v", block.Transfers, want)
	}
}

func TestClientReadsHeadAndTokenMetadata(t *testing.T) {
	server := newNodeServer(t, func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/wallet/getnowblock":
			writeJSON(writer, blockJSON(nil))
		case "/wallet/triggerconstantcontract":
			var body struct {
				Function string `json:"function_selector"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatalf("解析请求: %v", err)
			}
			switch body.Function {
			case "decimals()":
				writeJSON(writer, `{"constant_result":["0000000000000000000000000000000000000000000000000000000000000006"],"result":{"result":true}}`)
			case "symbol()":
				writeJSON(writer, `{"constant_result":["000000000000000000000000000000000000000000000000000000000000002000000000000000000000000000000000000000000000000000000000000000045553445400000000000000000000000000000000000000000000000000000000"],"result":{"result":true}}`)
			default:
				t.Fatalf("未知函数: %s", body.Function)
			}
		default:
			http.NotFound(writer, request)
		}
	})
	client := newClient(t, server.URL, "", 0)
	head, err := client.Head(context.Background())
	if err != nil || head.Height != 1 {
		t.Fatalf("Head() = %+v, %v", head, err)
	}
	metadata, err := client.TokenMetadata(context.Background(), "TXYZopYRdj2D9XRtbG411XZZ3kM5VkAeBf")
	if err != nil || metadata != (tron.TokenMetadata{Symbol: "USDT", Decimals: 6}) {
		t.Fatalf("TokenMetadata() = %+v, %v", metadata, err)
	}
}

func TestClientReadsFinalizedTokenBalance(t *testing.T) {
	const contractAddress = "TXYZopYRdj2D9XRtbG411XZZ3kM5VkAeBf"
	tests := []struct {
		name   string
		result string
		want   string
	}{
		{
			name:   "uint256 最大值",
			result: "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
			want:   "115792089237316195423570985008687907853269984665640564039457584007913129639935",
		},
		{name: "零余额", result: strings.Repeat("0", 64), want: "0"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := newNodeServer(t, func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path != "/node/walletsolidity/triggerconstantcontract" {
					t.Fatalf("请求路径 = %s", request.URL.Path)
				}
				var body struct {
					OwnerAddress    string `json:"owner_address"`
					ContractAddress string `json:"contract_address"`
					Function        string `json:"function_selector"`
					Parameter       string `json:"parameter"`
					Visible         bool   `json:"visible"`
				}
				if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
					t.Fatalf("解析请求: %v", err)
				}
				if body.OwnerAddress != "T9yD14Nj9j7xAB4dbGeiX9h8unkKHxuWwb" ||
					body.ContractAddress != contractAddress || body.Function != "balanceOf(address)" ||
					body.Parameter != "000000000000000000000000eca9bc828a3005b9a3b909f2cc5c2a54794de05f" ||
					!body.Visible {
					t.Fatalf("余额请求 = %+v", body)
				}
				writeJSON(writer, `{"constant_result":["`+test.result+`"],"result":{"result":true}}`)
			})
			balance, err := newClient(t, server.URL+"/node", "", 0).TokenBalance(
				context.Background(), contractAddress, contractAddress,
			)
			if err != nil || balance != test.want {
				t.Fatalf("TokenBalance() = %s, %v", balance, err)
			}
		})
	}
}

func TestClientRejectsInvalidTokenBalance(t *testing.T) {
	const contractAddress = "TXYZopYRdj2D9XRtbG411XZZ3kM5VkAeBf"
	const ownerAddress = "T9yD14Nj9j7xAB4dbGeiX9h8unkKHxuWwb"
	tests := []struct {
		name     string
		contract string
		owner    string
		response string
	}{
		{name: "合约地址无效", contract: "invalid", owner: ownerAddress},
		{name: "持有人地址无效", contract: contractAddress, owner: "invalid"},
		{name: "节点拒绝执行", contract: contractAddress, owner: ownerAddress, response: `{"result":{"result":false}}`},
		{name: "返回值长度错误", contract: contractAddress, owner: ownerAddress, response: `{"constant_result":["00"],"result":{"result":true}}`},
		{name: "返回值不是十六进制", contract: contractAddress, owner: ownerAddress, response: `{"constant_result":["zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"],"result":{"result":true}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := newNodeServer(t, func(writer http.ResponseWriter, _ *http.Request) {
				writeJSON(writer, test.response)
			})
			_, err := newClient(t, server.URL, "", 0).TokenBalance(
				context.Background(), test.contract, test.owner,
			)
			if err == nil {
				t.Fatal("TokenBalance() 未返回错误")
			}
		})
	}
}

func TestClientReadsSolidifiedTransactionOutcome(t *testing.T) {
	server := newNodeServer(t, func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/walletsolidity/gettransactionbyid":
			writeJSON(writer, fmt.Sprintf(`{"txID":%q,"ret":[{"contractRet":"SUCCESS"}]}`, txID))
		case "/walletsolidity/gettransactioninfobyid":
			writeJSON(writer, fmt.Sprintf(`{"id":%q,"blockNumber":12,"receipt":{"result":"SUCCESS"}}`, txID))
		default:
			http.NotFound(writer, request)
		}
	})
	state, err := newClient(t, server.URL, "", 0).Transaction(context.Background(), txID)
	if err != nil || state.Status != tron.TransactionSucceeded || !state.Solidified || state.Block.Height != 12 {
		t.Fatalf("Transaction() = %+v, %v", state, err)
	}
}

func TestClientReturnsNotFoundForMissingSolidifiedTransaction(t *testing.T) {
	server := newNodeServer(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/walletsolidity/gettransactionbyid" {
			t.Fatalf("不应查询回执: %s", request.URL.Path)
		}
		writeJSON(writer, `{}`)
	})
	state, err := newClient(t, server.URL, "", 0).Transaction(context.Background(), txID)
	if err != nil || state.Status != tron.TransactionNotFound {
		t.Fatalf("Transaction() = %+v, %v", state, err)
	}
}

func TestClientRejectsInconsistentMissingTransaction(t *testing.T) {
	server := newNodeServer(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/walletsolidity/gettransactionbyid" {
			t.Fatalf("不应查询回执: %s", request.URL.Path)
		}
		writeJSON(writer, `{"ret":[{"contractRet":"SUCCESS"}]}`)
	})
	_, err := newClient(t, server.URL, "", 0).Transaction(context.Background(), txID)
	if !errors.Is(err, nodehttp.ErrInvalidResponse) {
		t.Fatalf("Transaction() error = %v", err)
	}
}

func TestClientSkipsFailedAndZeroValueTransfers(t *testing.T) {
	tests := []struct {
		name          string
		contractState string
		receiptState  string
		amount        uint64
	}{
		{name: "失败交易", contractState: "REVERT", receiptState: "REVERT", amount: 100},
		{name: "零金额", contractState: "SUCCESS", receiptState: "SUCCESS", amount: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := newNodeServer(t, func(writer http.ResponseWriter, request *http.Request) {
				switch request.URL.Path {
				case "/walletsolidity/getblockbynum":
					writeJSON(writer, blockJSON([]string{test.contractState}))
				case "/walletsolidity/gettransactioninfobyblocknum":
					writeJSON(writer, fmt.Sprintf(`[{"id":%q,"blockNumber":1,"receipt":{"result":%q},"log":[{"address":%q,"topics":[%q,%q,%q],"data":%q}]}]`,
						txID, test.receiptState, contract[2:], transferTopic(), addressTopic(from), addressTopic(to), uint256Hex(test.amount)))
				default:
					http.NotFound(writer, request)
				}
			})
			block, err := newClient(t, server.URL, "", 0).SolidifiedBlockByHeight(context.Background(), 1)
			if err != nil {
				t.Fatalf("SolidifiedBlockByHeight() error = %v", err)
			}
			if len(block.Transfers) != 0 {
				t.Fatalf("Transfers = %+v", block.Transfers)
			}
		})
	}
}

func TestClientRejectsIncompleteTransactionInfo(t *testing.T) {
	server := newNodeServer(t, func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/walletsolidity/getblockbynum":
			writeJSON(writer, blockJSON([]string{"SUCCESS"}))
		case "/walletsolidity/gettransactioninfobyblocknum":
			writeJSON(writer, `[]`)
		default:
			http.NotFound(writer, request)
		}
	})
	_, err := newClient(t, server.URL, "", 0).SolidifiedBlockByHeight(context.Background(), 1)
	if !errors.Is(err, nodehttp.ErrInvalidResponse) {
		t.Fatalf("SolidifiedBlockByHeight() error = %v", err)
	}
}

func TestClientRejectsMalformedTransfer(t *testing.T) {
	server := newNodeServer(t, func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/walletsolidity/getblockbynum":
			writeJSON(writer, blockJSON([]string{"SUCCESS"}))
		case "/walletsolidity/gettransactioninfobyblocknum":
			writeJSON(writer, fmt.Sprintf(`[{"id":%q,"blockNumber":1,"receipt":{"result":"SUCCESS"},"log":[{"address":%q,"topics":[%q,%q],"data":%q}]}]`,
				txID, contract[2:], transferTopic(), addressTopic(from), uint256Hex(1)))
		default:
			http.NotFound(writer, request)
		}
	})
	_, err := newClient(t, server.URL, "", 0).SolidifiedBlockByHeight(context.Background(), 1)
	if !errors.Is(err, nodehttp.ErrInvalidResponse) {
		t.Fatalf("SolidifiedBlockByHeight() error = %v", err)
	}
}

func TestClientHandlesNodeErrorsAndLimits(t *testing.T) {
	t.Run("HTTP 200 错误体", func(t *testing.T) {
		server := newNodeServer(t, func(writer http.ResponseWriter, _ *http.Request) {
			writeJSON(writer, `{"Error":"rate limited"}`)
		})
		_, err := newClient(t, server.URL, "", 0).SolidifiedHead(context.Background())
		if !errors.Is(err, nodehttp.ErrInvalidResponse) {
			t.Fatalf("SolidifiedHead() error = %v", err)
		}
	})
	t.Run("响应过大", func(t *testing.T) {
		server := newNodeServer(t, func(writer http.ResponseWriter, _ *http.Request) {
			writeJSON(writer, `{"padding":"`+strings.Repeat("x", 128)+`"}`)
		})
		_, err := newClient(t, server.URL, "", 32).SolidifiedHead(context.Background())
		if !errors.Is(err, nodehttp.ErrResponseTooLarge) {
			t.Fatalf("SolidifiedHead() error = %v", err)
		}
	})
	t.Run("区块不存在", func(t *testing.T) {
		server := newNodeServer(t, func(writer http.ResponseWriter, _ *http.Request) {
			writeJSON(writer, `{}`)
		})
		_, err := newClient(t, server.URL, "", 0).SolidifiedBlockByHeight(context.Background(), 1)
		if !errors.Is(err, tron.ErrBlockNotFound) {
			t.Fatalf("SolidifiedBlockByHeight() error = %v", err)
		}
	})
}

func TestClientFailsOverAndKeepsWorkingEndpoint(t *testing.T) {
	observer := &nodeObserverStub{}
	var primaryCalls atomic.Int64
	primary := newNodeServer(t, func(writer http.ResponseWriter, _ *http.Request) {
		primaryCalls.Add(1)
		http.Error(writer, "temporarily unavailable", http.StatusServiceUnavailable)
	})
	var backupCalls atomic.Int64
	backup := newNodeServer(t, func(writer http.ResponseWriter, request *http.Request) {
		backupCalls.Add(1)
		if request.URL.Path != "/walletsolidity/getnowblock" {
			t.Fatalf("备用节点请求路径 = %s", request.URL.Path)
		}
		writeJSON(writer, blockJSON(nil))
	})
	client, err := nodehttp.New(nodehttp.Config{
		BaseURL: primary.URL + ", " + backup.URL, Network: "tron-mainnet", Observer: observer,
	}, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	for range 2 {
		if _, err := client.SolidifiedHead(context.Background()); err != nil {
			t.Fatalf("SolidifiedHead() error = %v", err)
		}
	}
	if primaryCalls.Load() != 1 || backupCalls.Load() != 2 {
		t.Fatalf("节点调用次数 primary=%d backup=%d", primaryCalls.Load(), backupCalls.Load())
	}
	if observer.configuredOperation != nodehttp.OperationRead || observer.configuredEndpoints != 2 ||
		len(observer.failures) != 1 || observer.failures[0] != (observedFailure{
		operation: nodehttp.OperationRead, endpoint: 0, reason: nodehttp.FailureHTTPRetryable,
	}) || len(observer.switches) != 1 || observer.switches[0] != (observedSwitch{
		operation: nodehttp.OperationRead, from: 0, to: 1,
	}) || observer.exhausted != 0 {
		t.Fatalf("观察事件 = %+v", observer)
	}
}

func TestClientDoesNotFailOverOnDeterministicHTTPError(t *testing.T) {
	primary := newNodeServer(t, func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, "bad request", http.StatusBadRequest)
	})
	var backupCalls atomic.Int64
	backup := newNodeServer(t, func(writer http.ResponseWriter, _ *http.Request) {
		backupCalls.Add(1)
		writeJSON(writer, blockJSON(nil))
	})
	client, err := nodehttp.New(nodehttp.Config{
		BaseURL: primary.URL + "," + backup.URL, Network: "tron-mainnet",
	}, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := client.SolidifiedHead(context.Background()); !errors.Is(err, nodehttp.ErrInvalidResponse) {
		t.Fatalf("SolidifiedHead() error = %v", err)
	}
	if backupCalls.Load() != 0 {
		t.Fatalf("确定性错误访问了备用节点 %d 次", backupCalls.Load())
	}
}

func TestClientFailsOverOnInvalidJSON(t *testing.T) {
	primary := newNodeServer(t, func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"incomplete"`))
	})
	var backupCalls atomic.Int64
	backup := newNodeServer(t, func(writer http.ResponseWriter, _ *http.Request) {
		backupCalls.Add(1)
		writeJSON(writer, blockJSON(nil))
	})
	client, err := nodehttp.New(nodehttp.Config{
		BaseURL: primary.URL + "," + backup.URL, Network: "tron-mainnet",
	}, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := client.SolidifiedHead(context.Background()); err != nil {
		t.Fatalf("SolidifiedHead() error = %v", err)
	}
	if backupCalls.Load() != 1 {
		t.Fatalf("无效 JSON 后备用节点调用次数 = %d", backupCalls.Load())
	}
}

func TestClientObservesExhaustedNodePool(t *testing.T) {
	first := newNodeServer(t, func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, "unavailable", http.StatusServiceUnavailable)
	})
	second := newNodeServer(t, func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, "unavailable", http.StatusBadGateway)
	})
	observer := &nodeObserverStub{}
	client, err := nodehttp.New(nodehttp.Config{
		BaseURL: first.URL + "," + second.URL, Network: "tron-mainnet", Observer: observer,
	}, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := client.SolidifiedHead(context.Background()); !errors.Is(err, nodehttp.ErrInvalidResponse) {
		t.Fatalf("SolidifiedHead() error = %v", err)
	}
	if len(observer.failures) != 2 || observer.exhausted != 1 || len(observer.switches) != 0 {
		t.Fatalf("节点池耗尽观察事件 = %+v", observer)
	}
}

func TestNewRejectsInvalidConfiguration(t *testing.T) {
	for _, config := range []nodehttp.Config{
		{},
		{BaseURL: "ftp://node", Network: "tron"},
		{BaseURL: "https://user:pass@node", Network: "tron"},
		{BaseURL: "https://node?secret=value", Network: "tron"},
		{BaseURL: "https://node,,https://backup", Network: "tron"},
		{BaseURL: "https://1,https://2,https://3,https://4,https://5,https://6,https://7,https://8,https://9", Network: "tron"},
		{BaseURL: "https://node", Network: ""},
		{BaseURL: "https://node", Network: "tron", MaxResponseBytes: -1},
	} {
		if _, err := nodehttp.New(config, nil); !errors.Is(err, nodehttp.ErrInvalidConfig) {
			t.Errorf("New(%+v) error = %v", config, err)
		}
	}
}

func newClient(t *testing.T, baseURL, apiKey string, maxResponseBytes int64) *nodehttp.Client {
	t.Helper()
	client, err := nodehttp.New(nodehttp.Config{
		BaseURL: baseURL, Network: "tron-mainnet", APIKey: apiKey, MaxResponseBytes: maxResponseBytes,
	}, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return client
}

func newNodeServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

func writeJSON(writer http.ResponseWriter, body string) {
	writer.Header().Set("Content-Type", "application/json")
	_, _ = writer.Write([]byte(body))
}

func blockJSON(results []string) string {
	transactions := ""
	if results != nil {
		parts := make([]string, 0, len(results))
		for _, result := range results {
			parts = append(parts, fmt.Sprintf(`{"contractRet":%q}`, result))
		}
		transactions = fmt.Sprintf(`,"transactions":[{"txID":%q,"ret":[%s]}]`, txID, strings.Join(parts, ","))
	}
	return fmt.Sprintf(`{"blockID":%q,"block_header":{"raw_data":{"number":1,"parentHash":%q,"timestamp":1700000000000}}%s}`, blockHash, parentHash, transactions)
}

func transferTopic() string {
	return "ddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"
}

func addressTopic(address string) string {
	return strings.Repeat("0", 24) + address[2:]
}

func uint256Hex(value uint64) string {
	return fmt.Sprintf("%064x", value)
}

type observedFailure struct {
	operation string
	endpoint  int
	reason    string
}

type observedSwitch struct {
	operation string
	from      int
	to        int
}

type nodeObserverStub struct {
	configuredOperation string
	configuredEndpoints int
	failures            []observedFailure
	switches            []observedSwitch
	exhausted           int
}

func (observer *nodeObserverStub) Configured(operation string, endpointCount int) {
	observer.configuredOperation = operation
	observer.configuredEndpoints = endpointCount
}

func (observer *nodeObserverStub) Failed(operation string, endpointIndex int, reason string) {
	observer.failures = append(observer.failures, observedFailure{
		operation: operation, endpoint: endpointIndex, reason: reason,
	})
}

func (observer *nodeObserverStub) Switched(operation string, previousEndpointIndex, currentEndpointIndex int) {
	observer.switches = append(observer.switches, observedSwitch{
		operation: operation, from: previousEndpointIndex, to: currentEndpointIndex,
	})
}

func (observer *nodeObserverStub) Exhausted(string) {
	observer.exhausted++
}
