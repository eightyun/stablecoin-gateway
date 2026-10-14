package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/eightyun/stablecoin-gateway/internal/chain/tron"
	"github.com/eightyun/stablecoin-gateway/internal/signer"
)

var errUsage = errors.New("用法: gateway-kms-acceptance --region REGION --key-id KEY --expected-address ADDRESS [--sign] [--timeout 30s]")

type options struct {
	region          string
	keyID           string
	expectedAddress string
	sign            bool
	timeout         time.Duration
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, writer io.Writer) error {
	cfg, err := parseOptions(args)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.region))
	if err != nil {
		return redactedError(err)
	}
	key, err := signer.NewAWSKMSKey(ctx, kms.NewFromConfig(awsCfg), cfg.keyID)
	if err != nil {
		return redactedError(err)
	}
	result, err := key.CheckAcceptance(ctx, cfg.expectedAddress, cfg.sign)
	if err != nil {
		return redactedError(err)
	}
	mode := "public_key"
	if cfg.sign {
		mode = "signature"
	}
	return json.NewEncoder(writer).Encode(struct {
		Status    string    `json:"status"`
		Mode      string    `json:"mode"`
		CheckedAt time.Time `json:"checked_at"`
		signer.KMSAcceptanceResult
	}{Status: "ok", Mode: mode, CheckedAt: time.Now().UTC(), KMSAcceptanceResult: result})
}

func parseOptions(args []string) (options, error) {
	var cfg options
	flags := flag.NewFlagSet("gateway-kms-acceptance", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&cfg.region, "region", os.Getenv("GATEWAY_KMS_SIGNER_AWS_REGION"), "AWS 区域")
	flags.StringVar(&cfg.keyID, "key-id", os.Getenv("GATEWAY_KMS_SIGNER_AWS_KEY_ID"), "KMS 密钥标识")
	flags.StringVar(&cfg.expectedAddress, "expected-address", os.Getenv("GATEWAY_KMS_SIGNER_OWNER_ADDRESS"), "预先登记的 TRON 地址")
	flags.BoolVar(&cfg.sign, "sign", false, "执行随机验收挑战签名")
	flags.DurationVar(&cfg.timeout, "timeout", 30*time.Second, "验收总超时")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return options{}, errUsage
	}
	cfg.region = strings.TrimSpace(cfg.region)
	cfg.keyID = strings.TrimSpace(cfg.keyID)
	if !validIdentifier(cfg.region, 64) || !validIdentifier(cfg.keyID, 2048) || cfg.timeout <= 0 || cfg.timeout > time.Minute {
		return options{}, errUsage
	}
	address, err := tron.NormalizeAddressBase58(cfg.expectedAddress)
	if err != nil {
		return options{}, errors.New("必须提供有效的预期 TRON 地址")
	}
	cfg.expectedAddress = address
	return cfg, nil
}

func validIdentifier(value string, maximum int) bool {
	if value == "" || len(value) > maximum {
		return false
	}
	for _, character := range value {
		if character <= 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

// redactedError 保留固定错误类别，避免命令输出 ARN 或云服务原始错误。
func redactedError(err error) error {
	for _, known := range []error{
		context.Canceled, context.DeadlineExceeded, signer.ErrInvalidKMSConfig,
		signer.ErrInvalidKMSKey, signer.ErrInvalidKMSSignature, signer.ErrPolicyDenied,
	} {
		if errors.Is(err, known) {
			return known
		}
	}
	return errors.New("AWS KMS 验收调用失败，请检查凭证、权限、网络和 AWS 审计记录")
}
