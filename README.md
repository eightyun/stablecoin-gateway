# Gateway

简体中文 | [English](README.en.md)

Gateway 是一个面向商户的生产级开源稳定币支付系统，目标覆盖充值、出款、双分录账本、链上索引、商户回调、对账与异常恢复。

首个稳定版本聚焦 USDT-TRC20，之后通过链适配接口扩展 EVM，并以独立扩展提供 x402 支持。

## 当前状态

项目处于早期建设阶段，尚未经过安全审计，不能直接用于承载真实资金。

目前已经实现：

- HTTP 服务基础骨架与健康检查
- PostgreSQL 版本化迁移
- 双分录账本约束与幂等过账
- Transactional Outbox 基础能力
- TRON 确定性模拟器
- SolidityNode 已固化区块读取与 TRC20 日志解析
- 带租约和栅栏令牌的持久化扫描游标
- 独立 TRON 扫描 Worker
- 充值地址、充值意图和链事件匹配模型
- 独立充值匹配与意图过期 Worker
- 精确到账的原子双分录入账与 `deposit.confirmed` Outbox 事件
- HMAC-SHA256 商户鉴权、时间窗校验与持久化 nonce 防重放
- 加密保存、支持版本轮换的商户 API Secret
- 地址池原子分配与幂等充值意图创建
- 商户充值查询与可用余额 API

尚未完成：Webhook 投递、出款、归集、风控筛查、对账和生产钱包签名基础设施。

## 本地运行

要求：

- Go 1.23 或更高版本
- PostgreSQL

生成 API Secret 的加密主密钥并配置 API：

```bash
# 只生成一次，并将结果安全保存到密钥管理服务
openssl rand -base64 32

export GATEWAY_DATABASE_URL='postgres://gateway:password@127.0.0.1:5432/gateway?sslmode=disable'
export GATEWAY_API_KEY_ENCRYPTION_KEYS='{"v1":"替换为上一步生成并持久保存的Base64密钥"}'
export GATEWAY_API_KEY_ACTIVE_VERSION='v1'

go run ./cmd/gateway-api
curl http://127.0.0.1:8080/healthz
```

数据库迁移：

```bash
export GATEWAY_DATABASE_URL='postgres://gateway:password@127.0.0.1:5432/gateway?sslmode=disable'
go run ./cmd/gateway-migrate up
```

为数据库中已存在的活跃商户创建 API 凭证：

```bash
go run ./cmd/gateway-admin create-api-key \
  --merchant-id '00000000-0000-0000-0000-000000000000' \
  --name 'production'
```

Secret 只在创建时输出一次。数据库只保存 AES-256-GCM 密文；主密钥丢失后已有 Secret 无法恢复，生产环境必须通过密钥管理服务持久保存并注入。

## 商户 API

当前接口：

- `POST /v1/deposits`：从商户地址池幂等创建充值意图
- `GET /v1/deposits/{id}`：查询充值状态
- `GET /v1/balances`：查询各资产可用余额

创建充值时必须传 `Idempotency-Key`。金额使用资产最小单位的十进制整数字符串，例如 1 USDT（6 位精度）传 `"1000000"`。

每个商户请求必须携带：

- `X-Gateway-Key`
- `X-Gateway-Timestamp`：Unix 秒
- `X-Gateway-Nonce`：16～128 位 URL-safe 随机字符串，每个 API Key 下不可重复
- `X-Gateway-Signature`：HMAC-SHA256 的小写十六进制值

待签名规范字符串为：

```text
UPPERCASE_METHOD\nREQUEST_URI\nTIMESTAMP\nNONCE\nSHA256_HEX(BODY)
```

其中 `REQUEST_URI` 包含查询字符串。服务端默认接受前后 5 分钟时间窗，并在签名验证成功后原子消费 nonce。

启动 TRON 索引器前，根据 [.env.example](.env.example) 配置节点、资产合约、扫描起点及其父区块哈希，然后运行：

```bash
go run ./cmd/gateway-indexer
```

启动充值匹配 Worker：

```bash
go run ./cmd/gateway-deposit-worker
```

索引器只读取已固化区块，不持有私钥，也不广播交易。充值 Worker 消费索引器已持久化的链事件，支持多实例并发处理。数据库迁移必须在 API 或 Worker 启动前独立完成。

## 安全边界

- 业务服务不得保存或记录明文私钥。
- 生产签名必须通过独立远程签名服务、HSM 或 MPC 适配器完成。
- 主网能力必须显式启用，并通过发布检查清单。
- 地址筛查、KYC、AML 与牌照责任由部署运营方根据所在司法辖区落实。
- 在安全审计、灾难恢复演练和资金对账验收完成前，不应承载真实资金。

## License

Apache-2.0，详见 [LICENSE](LICENSE)。
