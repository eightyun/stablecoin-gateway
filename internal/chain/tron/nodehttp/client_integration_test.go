//go:build integration

package nodehttp_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/chain/tron/nodehttp"
)

func TestClientReadsLiveSolidifiedBlock(t *testing.T) {
	baseURL := os.Getenv("GATEWAY_TEST_TRON_NODE_URL")
	if baseURL == "" {
		t.Skip("未设置 GATEWAY_TEST_TRON_NODE_URL")
	}
	client, err := nodehttp.New(nodehttp.Config{
		BaseURL: baseURL,
		Network: "tron-live-test",
		APIKey:  os.Getenv("GATEWAY_TEST_TRON_API_KEY"),
	}, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	head, err := client.SolidifiedHead(ctx)
	if err != nil {
		t.Fatalf("SolidifiedHead() error = %v", err)
	}
	block, err := client.SolidifiedBlockByHeight(ctx, head.Height)
	if err != nil {
		t.Fatalf("SolidifiedBlockByHeight(%d) error = %v", head.Height, err)
	}
	if block.Header != head {
		t.Fatalf("链头 = %+v, 区块 = %+v", head, block.Header)
	}
}
