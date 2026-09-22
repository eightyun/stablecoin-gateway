package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunRejectsInvalidArguments(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		databaseURL string
		wantError   string
	}{
		{name: "缺少动作", wantError: "用法"},
		{name: "不允许自动回滚", args: []string{"down"}, wantError: "用法"},
		{name: "缺少连接地址", args: []string{"up"}, wantError: "GATEWAY_DATABASE_URL"},
		{
			name:        "拒绝非 PostgreSQL 地址",
			args:        []string{"up"},
			databaseURL: "mysql://user:secret@localhost/db",
			wantError:   "PostgreSQL URL",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			err := run(context.Background(), test.args, test.databaseURL, &output)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("run() error = %v, 期望包含 %q", err, test.wantError)
			}
			if strings.Contains(err.Error(), "secret") || output.Len() != 0 {
				t.Fatalf("错误或输出泄露了连接信息: error=%v output=%q", err, output.String())
			}
		})
	}
}
