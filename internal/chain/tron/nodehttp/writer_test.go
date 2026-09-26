package nodehttp_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

func signedPayload(transactionID string) []byte {
	return []byte(`{"txID":"` + transactionID + `","raw_data":{"contract":[{}],"timestamp":1700000000000,"expiration":1700000060000},"raw_data_hex":"00","signature":["` + strings.Repeat("a", 130) + `"]}`)
}
