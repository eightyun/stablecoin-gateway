//go:build soak

package nodehttp_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eightyun/stablecoin-gateway/internal/chain/tron"
	"github.com/eightyun/stablecoin-gateway/internal/chain/tron/nodehttp"
)

const (
	soakWorkers            = 6
	soakIterationsPerPhase = 12
	soakPhases             = 24
)

// TestFailoverSoakUnderEndpointChurn 验证并发读写遇到节点反复切换时不会改变签名交易或耗尽端点池。
func TestFailoverSoakUnderEndpointChurn(t *testing.T) {
	payload := signedPayload(writerTxID)
	states := make([]atomic.Bool, 3)
	states[0].Store(true)
	var calls [3]atomic.Int64
	var payloadMismatches atomic.Int64
	var unexpectedPaths atomic.Int64
	servers := make([]*httptest.Server, 0, len(states))
	for index := range states {
		index := index
		server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			calls[index].Add(1)
			if !states[index].Load() {
				http.Error(response, "temporarily unavailable", http.StatusServiceUnavailable)
				return
			}
			switch request.URL.Path {
			case "/walletsolidity/getnowblock":
				writeJSON(response, blockJSON(nil))
			case "/wallet/broadcasttransaction":
				body, err := io.ReadAll(request.Body)
				if err != nil || string(body) != string(payload) {
					payloadMismatches.Add(1)
					http.Error(response, "invalid payload", http.StatusBadRequest)
					return
				}
				writeJSON(response, `{"result":true,"txid":"`+writerTxID+`"}`)
			default:
				unexpectedPaths.Add(1)
				http.NotFound(response, request)
			}
		}))
		servers = append(servers, server)
		t.Cleanup(server.Close)
	}

	baseURLs := make([]string, 0, len(servers))
	for _, server := range servers {
		baseURLs = append(baseURLs, server.URL)
	}
	observer := &soakObserver{}
	client, err := nodehttp.New(nodehttp.Config{
		BaseURL: strings.Join(baseURLs, ","), Network: "nile", Observer: observer,
	}, nil)
	if err != nil {
		t.Fatalf("创建读取客户端: %v", err)
	}
	writer, err := nodehttp.NewWriter(nodehttp.WriterConfig{
		BaseURL: strings.Join(baseURLs, ","), Observer: observer,
	}, nil)
	if err != nil {
		t.Fatalf("创建广播客户端: %v", err)
	}
	transaction := tron.SignedTransaction{ID: writerTxID, Payload: payload}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	errors := &soakErrors{}
	current := 0
	for phase := range soakPhases {
		if phase > 0 {
			next := phase % len(states)
			// 先启用新节点再摘除旧节点，阶段内保持端点稳定，模拟安全运维切流。
			states[next].Store(true)
			states[current].Store(false)
			current = next
		}
		var workers sync.WaitGroup
		for range soakWorkers {
			workers.Add(1)
			go func() {
				defer workers.Done()
				for range soakIterationsPerPhase {
					head, readErr := client.SolidifiedHead(ctx)
					if readErr != nil {
						errors.add(fmt.Errorf("读取已固化链头: %w", readErr))
					} else if head.Height != 1 {
						errors.add(fmt.Errorf("已固化链头高度=%d，期望 1", head.Height))
					}
					if broadcastErr := writer.Broadcast(ctx, transaction); broadcastErr != nil {
						errors.add(fmt.Errorf("广播原交易: %w", broadcastErr))
					}
				}
			}()
		}
		workers.Wait()
	}

	if errors.count.Load() != 0 {
		t.Fatalf("并发故障切换出现 %d 个错误，首个错误: %v", errors.count.Load(), errors.first)
	}
	if payloadMismatches.Load() != 0 || unexpectedPaths.Load() != 0 {
		t.Fatalf("载荷不一致=%d，意外路径=%d", payloadMismatches.Load(), unexpectedPaths.Load())
	}
	for index := range calls {
		if calls[index].Load() == 0 {
			t.Fatalf("节点 %d 未收到请求", index+1)
		}
	}
	if observer.configured.Load() != 2 || observer.invalidConfiguration.Load() != 0 || observer.failures.Load() == 0 ||
		observer.switches.Load() == 0 || observer.exhausted.Load() != 0 {
		t.Fatalf("观察结果 configured=%d invalid_configuration=%d failures=%d switches=%d exhausted=%d",
			observer.configured.Load(), observer.invalidConfiguration.Load(), observer.failures.Load(),
			observer.switches.Load(), observer.exhausted.Load())
	}
}

type soakObserver struct {
	configured           atomic.Int64
	invalidConfiguration atomic.Int64
	failures             atomic.Int64
	switches             atomic.Int64
	exhausted            atomic.Int64
}

func (observer *soakObserver) Configured(operation string, endpointCount int) {
	observer.configured.Add(1)
	if endpointCount != 3 || (operation != nodehttp.OperationRead && operation != nodehttp.OperationBroadcast) {
		observer.invalidConfiguration.Add(1)
	}
}

func (observer *soakObserver) Failed(string, int, string) {
	observer.failures.Add(1)
}

func (observer *soakObserver) Switched(string, int, int) {
	observer.switches.Add(1)
}

func (observer *soakObserver) Exhausted(string) {
	observer.exhausted.Add(1)
}

type soakErrors struct {
	once  sync.Once
	first error
	count atomic.Int64
}

func (errors *soakErrors) add(err error) {
	errors.count.Add(1)
	errors.once.Do(func() {
		errors.first = err
	})
}
