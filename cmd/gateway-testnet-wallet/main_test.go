package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/eightyun/stablecoin-gateway/internal/signer"
)

func TestRunCreatesLoadableWalletWithoutOverwriting(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "nile-wallet.key")
	var result bytes.Buffer
	if err := run([]string{"--private-key-file", path}, &result); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	var created output
	if err := json.Unmarshal(result.Bytes(), &created); err != nil {
		t.Fatalf("解析输出: %v", err)
	}
	key, err := signer.LoadFileKey(path)
	if err != nil {
		t.Fatalf("LoadFileKey() error = %v", err)
	}
	defer key.Close()
	if created.Address != key.Address() || created.PrivateKeyFile != path {
		t.Fatalf("run() output = %+v", created)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("私钥文件权限 = %v, %v", info.Mode().Perm(), err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"--private-key-file", path}, &bytes.Buffer{}); !errors.Is(err, errUnsafeDestination) {
		t.Fatalf("重复 run() error = %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("重复执行修改了已有私钥")
	}
}

func TestRunRejectsUnsafeDestination(t *testing.T) {
	if err := run([]string{"--private-key-file", "relative.key"}, &bytes.Buffer{}); !errors.Is(err, errUnsafeDestination) {
		t.Fatalf("相对路径 run() error = %v", err)
	}
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := run(
		[]string{"--private-key-file", filepath.Join(directory, "nile-wallet.key")},
		&bytes.Buffer{},
	); !errors.Is(err, errUnsafeDestination) {
		t.Fatalf("不安全目录 run() error = %v", err)
	}
}
