package nodehttp_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/eightyun/stablecoin-gateway/internal/chain/tron"
	"github.com/eightyun/stablecoin-gateway/internal/chain/tron/nodehttp"
)

func TestClientFiltersUntrackedContractsBeforeDecodingTransfers(t *testing.T) {
	base58, err := tron.NormalizeAddressBase58(contract)
	if err != nil {
		t.Fatal(err)
	}
	for _, tracked := range []string{contract, base58, " \t" + strings.ToUpper(contract) + "\n"} {
		t.Run(tracked, func(t *testing.T) {
			untracked := strings.Repeat("3", 40)
			logs := []map[string]any{
				{"address": untracked, "topics": []string{transferTopic(), addressTopic(from), addressTopic(to), uint256Hex(7)}, "data": ""},
				{"address": untracked, "topics": []string{transferTopic(), "invalid"}, "data": "invalid"},
				{"address": untracked, "topics": []string{transferTopic(), addressTopic(from), addressTopic(to)}, "data": uint256Hex(2)},
				{"address": contract[2:], "topics": []string{transferTopic(), addressTopic(from), addressTopic(to)}, "data": uint256Hex(1_000_000)},
			}
			server := trackedContractServer(t, logs)
			client, err := nodehttp.New(nodehttp.Config{BaseURL: server.URL, Network: "tron-nile", TrackedContract: tracked}, nil)
			if err != nil {
				t.Fatal(err)
			}
			block, err := client.SolidifiedBlockByHeight(context.Background(), 1)
			if err != nil {
				t.Fatal(err)
			}
			if len(block.Receipts) != 1 || block.Receipts[0].Outcome != tron.ExecutionSucceeded {
				t.Fatalf("完整 Receipt 未保留: %+v", block.Receipts)
			}
			want := tron.Transfer{ID: tron.EventID{Network: "tron-nile", Contract: contract, TransactionID: txID, LogIndex: 3}, From: from, To: to, Amount: "1000000"}
			if len(block.Transfers) != 1 || block.Transfers[0] != want {
				t.Fatalf("过滤结果或原始日志位置错误: %+v", block.Transfers)
			}
		})
	}
}

func TestClientKeepsTrackedTransfersFailClosed(t *testing.T) {
	for _, test := range []struct {
		name    string
		tracked string
		address string
		topics  []string
		data    string
	}{
		{"目标合约 NFT 形态", contract, contract[2:], []string{transferTopic(), addressTopic(from), addressTopic(to), uint256Hex(1)}, ""},
		{"目标合约金额错误", contract, contract[2:], []string{transferTopic(), addressTopic(from), addressTopic(to)}, "invalid"},
		{"目标合约付款地址错误", contract, contract[2:], []string{transferTopic(), "invalid", addressTopic(to)}, uint256Hex(1)},
		{"无法判断所属合约", contract, "invalid", []string{transferTopic(), addressTopic(from), addressTopic(to)}, uint256Hex(1)},
		{"未配置时保持严格解析", "", strings.Repeat("3", 40), []string{transferTopic(), addressTopic(from), addressTopic(to), uint256Hex(1)}, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := trackedContractServer(t, []map[string]any{{"address": test.address, "topics": test.topics, "data": test.data}})
			client, err := nodehttp.New(nodehttp.Config{BaseURL: server.URL, Network: "tron-nile", TrackedContract: test.tracked}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.SolidifiedBlockByHeight(context.Background(), 1); !errors.Is(err, nodehttp.ErrInvalidResponse) {
				t.Fatalf("未严格拒绝异常日志: %v", err)
			}
		})
	}
}

func TestClientRejectsInvalidTrackedContract(t *testing.T) {
	if _, err := nodehttp.New(nodehttp.Config{BaseURL: "https://nile.trongrid.io", Network: "tron-nile", TrackedContract: "invalid"}, nil); !errors.Is(err, nodehttp.ErrInvalidConfig) {
		t.Fatalf("非法目标合约未被拒绝: %v", err)
	}
}

func trackedContractServer(t *testing.T, logs []map[string]any) *httptest.Server {
	t.Helper()
	return newNodeServer(t, func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/walletsolidity/getblockbynum":
			writeJSON(writer, blockJSON([]string{"SUCCESS"}))
		case "/walletsolidity/gettransactioninfobyblocknum":
			writer.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(writer).Encode([]map[string]any{{"id": txID, "blockNumber": 1, "receipt": map[string]string{"result": "SUCCESS"}, "log": logs}}); err != nil {
				t.Fatal(err)
			}
		default:
			http.NotFound(writer, request)
		}
	})
}
