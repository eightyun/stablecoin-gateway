package nodehttp_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
	if err != nil || head != (tron.Header{Height: 1, Hash: blockHash, ParentHash: parentHash}) {
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

func TestNewRejectsInvalidConfiguration(t *testing.T) {
	for _, config := range []nodehttp.Config{
		{},
		{BaseURL: "ftp://node", Network: "tron"},
		{BaseURL: "https://user:pass@node", Network: "tron"},
		{BaseURL: "https://node?secret=value", Network: "tron"},
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
	return fmt.Sprintf(`{"blockID":%q,"block_header":{"raw_data":{"number":1,"parentHash":%q}}%s}`, blockHash, parentHash, transactions)
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
