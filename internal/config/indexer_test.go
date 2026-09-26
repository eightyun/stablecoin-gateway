package config

import (
	"strings"
	"testing"
	"time"
)

const (
	testContract = "41A614F803B6FD780986A42C78EC9C7F77E6DED13C"
	testAnchor   = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
)

func TestLoadIndexer(t *testing.T) {
	setValidIndexerEnvironment(t)
	t.Setenv("GATEWAY_TRON_MAX_RESPONSE_BYTES", "1048576")
	config, err := LoadIndexer()
	if err != nil {
		t.Fatalf("LoadIndexer() error = %v", err)
	}
	if config.Contract != strings.ToLower(testContract) || config.AnchorHash != strings.ToLower(testAnchor) {
		t.Fatalf("十六进制配置未规范化: %+v", config)
	}
	if config.StartHeight != 100 || config.MaxResponseBytes != 1048576 || config.WorkerID == "" {
		t.Fatalf("LoadIndexer() = %+v", config)
	}
	if config.StepTimeout != 45*time.Second || config.LeaseDuration != time.Minute {
		t.Fatalf("默认时间配置 = %+v", config)
	}
}

func TestLoadIndexerRejectsUnsafeConfiguration(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "缺少数据库", key: "GATEWAY_DATABASE_URL", value: ""},
		{name: "合约不是 41 地址", key: "GATEWAY_TRON_CONTRACT", value: "40" + strings.Repeat("a", 40)},
		{name: "起点不是正数", key: "GATEWAY_TRON_START_HEIGHT", value: "0"},
		{name: "锚点长度错误", key: "GATEWAY_TRON_ANCHOR_HASH", value: "aa"},
		{name: "响应限制不是正数", key: "GATEWAY_TRON_MAX_RESPONSE_BYTES", value: "-1"},
		{name: "租约余量不足", key: "GATEWAY_INDEXER_LEASE_DURATION", value: "49s"},
		{name: "退避上限小于下限", key: "GATEWAY_INDEXER_RETRY_MAX", value: "500ms"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setValidIndexerEnvironment(t)
			t.Setenv(test.key, test.value)
			if _, err := LoadIndexer(); err == nil {
				t.Fatal("LoadIndexer() 未拒绝无效配置")
			}
		})
	}
}

func setValidIndexerEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"GATEWAY_DATABASE_URL", "GATEWAY_TRON_NODE_URL", "GATEWAY_TRON_NETWORK",
		"GATEWAY_TRON_CONTRACT", "GATEWAY_TRON_START_HEIGHT", "GATEWAY_TRON_ANCHOR_HASH",
		"GATEWAY_TRON_API_KEY", "GATEWAY_TRON_MAX_RESPONSE_BYTES", "GATEWAY_INDEXER_WORKER_ID",
		"GATEWAY_INDEXER_STEP_TIMEOUT", "GATEWAY_INDEXER_LEASE_DURATION", "GATEWAY_INDEXER_IDLE_INTERVAL",
		"GATEWAY_INDEXER_RETRY_MIN", "GATEWAY_INDEXER_RETRY_MAX",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("GATEWAY_DATABASE_URL", "postgres://gateway:test@localhost/gateway")
	t.Setenv("GATEWAY_TRON_NODE_URL", "https://api.trongrid.io")
	t.Setenv("GATEWAY_TRON_NETWORK", "tron-mainnet")
	t.Setenv("GATEWAY_TRON_CONTRACT", testContract)
	t.Setenv("GATEWAY_TRON_START_HEIGHT", "100")
	t.Setenv("GATEWAY_TRON_ANCHOR_HASH", testAnchor)
}
