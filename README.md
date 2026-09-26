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
- 基于 Transactional Outbox 的 Webhook Worker
- Webhook HMAC 签名、指数退避、死信与逐次投递审计
- 默认阻止私网目标、禁止重定向的 Webhook SSRF 防护
- 商户出款申请、TRON 地址校验与原子余额冻结
- 带审计记录的出款审批，以及拒绝时的原子余额解冻
- 带租约和栅栏令牌的出款签名队列，以及隔离签名器接口
- HTTPS 远程签名器客户端与 TRON FullNode 广播适配器
- 带租约的出款广播与确认 Worker，广播结果不确定时按原 txID 恢复
- 基于 SolidityNode 固化交易与 Receipt 的终态确认
- 成功出款原子结算、失败或安全过期出款原子解冻
- 可用余额与冻结余额分离查询

尚未完成：自动地址筛查、归集、对账、监控告警和生产钱包签名服务本身。

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

配置 Webhook 独立加密密钥，并为商户创建 HTTPS 端点：

```bash
# 同样只生成并持久保存一次；不要与 API Key 加密主密钥复用
openssl rand -base64 32
export GATEWAY_WEBHOOK_SECRET_ENCRYPTION_KEYS='{"v1":"替换为持久保存的Base64密钥"}'
export GATEWAY_WEBHOOK_SECRET_ACTIVE_VERSION='v1'

go run ./cmd/gateway-admin create-webhook-endpoint \
  --merchant-id '00000000-0000-0000-0000-000000000000' \
  --name 'production' \
  --url 'https://merchant.example/webhooks/gateway'

go run ./cmd/gateway-webhook-worker
```

Worker 默认拒绝私网、回环和链路本地目标，且不跟随重定向。仅在明确需要投递到可信内网时设置 `GATEWAY_WEBHOOK_ALLOW_PRIVATE_NETWORKS=true`。

审批或拒绝待审核出款：

```bash
go run ./cmd/gateway-admin approve-payout \
  --payout-id '00000000-0000-0000-0000-000000000000' \
  --reviewer 'ops@example.com' \
  --reason 'manual screening passed'

go run ./cmd/gateway-admin reject-payout \
  --payout-id '00000000-0000-0000-0000-000000000000' \
  --reviewer 'ops@example.com' \
  --reason 'destination screening denied'
```

审批通过后出款进入 `approved`；拒绝会在同一数据库事务中把冻结资金退回可用余额。命令重复执行相同决定是幂等的，冲突决定会失败。

## 商户 API

当前接口：

- `POST /v1/deposits`：从商户地址池幂等创建充值意图
- `GET /v1/deposits/{id}`：查询充值状态
- `GET /v1/balances`：查询各资产可用与冻结余额
- `POST /v1/payouts`：幂等创建出款并原子冻结余额
- `GET /v1/payouts/{id}`：查询出款状态

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

当前出款创建后进入 `pending_review`，资金从 `available` 转入 `frozen`；审批通过后进入 `approved`。签名 Worker 通过数据库租约领取任务，并只调用不暴露私钥的 `TransferSigner`；成功持久化唯一签名交易后才进入 `ready_for_broadcast`。执行 Worker 广播同一份不可变签名交易，并只以 SolidityNode 的固化交易与 Receipt 作为资金终态；广播超时会继续查询原 txID，不会直接创建第二笔付款。第一条出款网络只接受 TRON Base58Check 地址。

配置并启动出款签名 Worker：

```bash
export GATEWAY_PAYOUT_SIGNER_URL='https://signer.internal.example'
export GATEWAY_PAYOUT_SIGNER_BEARER_TOKEN='从密钥管理服务注入'
go run ./cmd/gateway-payout-signing-worker
```

远程签名服务必须实现 `POST /v1/tron/transfers:sign`，按 `Idempotency-Key` 幂等返回同一笔完整签名交易。生产环境应通过 mTLS 服务网格或等价工作负载身份保护该 HTTPS 链路；Bearer Token 仍必须从密钥管理服务注入，不能写入仓库。

配置 FullNode、SolidityNode 并启动出款执行 Worker：

```bash
export GATEWAY_TRON_NETWORK='tron-nile'
export GATEWAY_PAYOUT_TRON_FULL_NODE_URL='https://nile.trongrid.io'
export GATEWAY_PAYOUT_TRON_SOLIDITY_NODE_URL='https://nile.trongrid.io'
export GATEWAY_TRON_API_KEY='从密钥管理服务注入'
go run ./cmd/gateway-payout-execution-worker
```

上述配置仅表示具备测试网接入能力；仓库当前尚未完成 Nile 真实钱包和测试资产的端到端验证。生产环境应为两个节点端点配置独立容灾与监控。

## Webhook

当前投递 `deposit.confirmed`。事件信封固定为：

```json
{"id":"事件 UUID","type":"deposit.confirmed","created_at":"RFC3339 时间","data":{}}
```

请求包含 `X-Gateway-Event-ID`、`X-Gateway-Event-Type`、`X-Gateway-Event-Timestamp` 和 `X-Gateway-Signature`。签名值为：

```text
v1=HEX(HMAC_SHA256(secret, timestamp + "." + event_id + "." + raw_body))
```

只有 2xx 响应视为成功。投递采用至少一次语义；商户必须以 `X-Gateway-Event-ID` 幂等消费。失败会指数退避并在达到上限后进入死信。

## 网络测试门槛

- 测试网：出款审批、隔离签名、广播和固化确认链路已具备接入条件；下一阶段使用 Nile 钱包和测试资产做端到端验证，但地址筛查仍需先以人工审批作为安全闸门。
- 主网灰度：测试网持续运行、三角对账、监控告警、灾难恢复演练和外部安全审计全部通过后，才允许白名单与小额限额灰度。
- 主网不作为普通测试环境；任何主网验证都必须有明确损失上限、双人审批和停止开关。

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
