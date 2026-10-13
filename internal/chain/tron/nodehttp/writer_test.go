package nodehttp_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/eightyun/stablecoin-gateway/internal/chain/tron"
	"github.com/eightyun/stablecoin-gateway/internal/chain/tron/nodehttp"
)

const writerTxID = "6e340b9cffb37a989ca544e6bb780a2c78901d3fb33738768511a30617afa01d"

func TestWriterBroadcastsOriginalSignedPayload(t *testing.T) {
	payload := signedPayload(writerTxID)
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/node/wallet/broadcasttransaction" ||
			request.Header.Get("TRON-PRO-API-KEY") != "api-key" {
			t.Fatalf("请求 = %s %s headers=%+v", request.Method, request.URL.Path, request.Header)
		}
		body, err := io.ReadAll(request.Body)
		if err != nil || string(body) != string(payload) {
			t.Fatalf("广播载荷 = %q, %v", body, err)
		}
		_, _ = response.Write([]byte(`{"result":true,"txid":"` + writerTxID + `"}`))
	}))
	defer server.Close()
	writer, err := nodehttp.NewWriter(nodehttp.WriterConfig{
		BaseURL: server.URL + "/node", APIKey: "api-key",
	}, server.Client())
	if err != nil {
		t.Fatalf("NewWriter() error = %v", err)
	}
	if err := writer.Broadcast(context.Background(), tron.SignedTransaction{ID: writerTxID, Payload: payload}); err != nil {
		t.Fatalf("Broadcast() error = %v", err)
	}
}

func TestWriterRejectsNodeRejectionAndMismatchedResponse(t *testing.T) {
	tests := []struct {
		name     string
		response string
		want     error
	}{
		{name: "节点拒绝", response: `{"result":false,"code":"SERVER_BUSY"}`, want: nodehttp.ErrBroadcastRejected},
		{name: "交易ID不一致", response: `{"result":true,"txid":"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"}`, want: nodehttp.ErrInvalidResponse},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				_, _ = response.Write([]byte(test.response))
			}))
			defer server.Close()
			writer, err := nodehttp.NewWriter(nodehttp.WriterConfig{BaseURL: server.URL}, server.Client())
			if err != nil {
				t.Fatalf("NewWriter() error = %v", err)
			}
			err = writer.Broadcast(context.Background(), tron.SignedTransaction{ID: writerTxID, Payload: signedPayload(writerTxID)})
			if !errors.Is(err, test.want) {
				t.Fatalf("Broadcast() error = %v", err)
			}
		})
	}
}

func TestWriterFailsOverWithIdenticalTransaction(t *testing.T) {
	payload := signedPayload(writerTxID)
	var primaryCalls atomic.Int64
	primary := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		primaryCalls.Add(1)
		body, _ := io.ReadAll(request.Body)
		if string(body) != string(payload) {
			t.Fatalf("主节点广播载荷 = %q", body)
		}
		http.Error(response, "temporarily unavailable", http.StatusServiceUnavailable)
	}))
	defer primary.Close()
	var backupCalls atomic.Int64
	backup := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		backupCalls.Add(1)
		body, _ := io.ReadAll(request.Body)
		if string(body) != string(payload) {
			t.Fatalf("备用节点广播载荷 = %q", body)
		}
		_, _ = response.Write([]byte(`{"result":true,"txid":"` + writerTxID + `"}`))
	}))
	defer backup.Close()
	writer, err := nodehttp.NewWriter(nodehttp.WriterConfig{
		BaseURL: primary.URL + "," + backup.URL,
	}, nil)
	if err != nil {
		t.Fatalf("NewWriter() error = %v", err)
	}
	transaction := tron.SignedTransaction{ID: writerTxID, Payload: payload}
	for range 2 {
		if err := writer.Broadcast(context.Background(), transaction); err != nil {
			t.Fatalf("Broadcast() error = %v", err)
		}
	}
	if primaryCalls.Load() != 1 || backupCalls.Load() != 2 {
		t.Fatalf("节点调用次数 primary=%d backup=%d", primaryCalls.Load(), backupCalls.Load())
	}
}

func TestWriterDoesNotFailOverOnNodeRejection(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`{"result":false,"code":"CONTRACT_VALIDATE_ERROR"}`))
	}))
	defer primary.Close()
	var backupCalls atomic.Int64
	backup := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		backupCalls.Add(1)
		_, _ = response.Write([]byte(`{"result":true,"txid":"` + writerTxID + `"}`))
	}))
	defer backup.Close()
	writer, err := nodehttp.NewWriter(nodehttp.WriterConfig{BaseURL: primary.URL + "," + backup.URL}, nil)
	if err != nil {
		t.Fatalf("NewWriter() error = %v", err)
	}
	err = writer.Broadcast(context.Background(), tron.SignedTransaction{
		ID: writerTxID, Payload: signedPayload(writerTxID),
	})
	if !errors.Is(err, nodehttp.ErrBroadcastRejected) {
		t.Fatalf("Broadcast() error = %v", err)
	}
	if backupCalls.Load() != 0 {
		t.Fatalf("节点拒绝后访问了备用节点 %d 次", backupCalls.Load())
	}
}

func TestWriterRejectsInvalidSignedTransaction(t *testing.T) {
	writer, err := nodehttp.NewWriter(nodehttp.WriterConfig{BaseURL: "https://node.example"}, nil)
	if err != nil {
		t.Fatalf("NewWriter() error = %v", err)
	}
	for _, transaction := range []tron.SignedTransaction{
		{},
		{ID: writerTxID, Payload: []byte("not-json")},
		{ID: strings.Repeat("a", 64), Payload: signedPayload(writerTxID)},
	} {
		if err := writer.Broadcast(context.Background(), transaction); !errors.Is(err, tron.ErrInvalidSignedTransaction) {
			t.Fatalf("Broadcast(%+v) error = %v", transaction, err)
		}
	}
}

func TestNewWriterRejectsInvalidConfiguration(t *testing.T) {
	for _, config := range []nodehttp.WriterConfig{
		{},
		{BaseURL: "ftp://node"},
		{BaseURL: "https://user:pass@node"},
		{BaseURL: "https://node?secret=value"},
		{BaseURL: "https://node,,https://backup"},
		{BaseURL: "https://1,https://2,https://3,https://4,https://5,https://6,https://7,https://8,https://9"},
	} {
		if _, err := nodehttp.NewWriter(config, nil); !errors.Is(err, nodehttp.ErrInvalidWriterConfig) {
			t.Errorf("NewWriter(%+v) error = %v", config, err)
		}
	}
}

func signedPayload(transactionID string) []byte {
	return []byte(`{"txID":"` + transactionID + `","raw_data":{"contract":[{}],"timestamp":1700000000000,"expiration":1700000060000},"raw_data_hex":"00","signature":["` + strings.Repeat("a", 130) + `"]}`)
}
