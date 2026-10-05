package main

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/eightyun/stablecoin-gateway/internal/signer"
)

var (
	errUsage             = errors.New("用法: gateway-testnet-wallet --private-key-file ABSOLUTE_PATH")
	errUnsafeDestination = errors.New("测试网钱包私钥目标路径不安全")
)

type output struct {
	Address        string `json:"address"`
	PrivateKeyFile string `json:"private_key_file"`
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, writer io.Writer) error {
	flags := flag.NewFlagSet("gateway-testnet-wallet", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	privateKeyFile := flags.String("private-key-file", "", "测试网钱包私钥绝对路径")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return errUsage
	}
	path, err := validateDestination(*privateKeyFile)
	if err != nil {
		return err
	}
	if err := createPrivateKeyFile(path); err != nil {
		return err
	}
	key, err := signer.LoadFileKey(path)
	if err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("重新加载测试网钱包私钥: %w", err)
	}
	defer key.Close()
	if err := json.NewEncoder(writer).Encode(output{
		Address: key.Address(), PrivateKeyFile: path,
	}); err != nil {
		return fmt.Errorf("输出测试网钱包信息: %w", err)
	}
	return nil
}

func validateDestination(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || !filepath.IsAbs(value) {
		return "", errUnsafeDestination
	}
	path := filepath.Clean(value)
	parent := filepath.Dir(path)
	info, err := os.Lstat(parent)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return "", errUnsafeDestination
	}
	if _, err := os.Lstat(path); err == nil || !errors.Is(err, os.ErrNotExist) {
		return "", errUnsafeDestination
	}
	return path, nil
}

func createPrivateKeyFile(path string) (err error) {
	privateKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		return fmt.Errorf("生成测试网钱包私钥: %w", err)
	}
	defer privateKey.Zero()
	serialized := privateKey.Serialize()
	defer clearBytes(serialized)

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("创建测试网钱包私钥文件: %w", errUnsafeDestination)
	}
	created := true
	defer func() {
		if created {
			_ = os.Remove(path)
		}
	}()
	encoder := hex.NewEncoder(file)
	if _, err = encoder.Write(serialized); err != nil {
		_ = file.Close()
		return fmt.Errorf("写入测试网钱包私钥: %w", err)
	}
	if err = file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("同步测试网钱包私钥: %w", err)
	}
	if err = file.Close(); err != nil {
		return fmt.Errorf("关闭测试网钱包私钥: %w", err)
	}
	created = false
	return nil
}

func clearBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
