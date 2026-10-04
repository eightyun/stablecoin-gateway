# stablecoin-gateway

简体中文 | [English](README.en.md)

[stablecoin-gateway](https://github.com/eightyun/stablecoin-gateway) 是一个面向商户的生产级开源稳定币支付系统，目标覆盖充值、出款、双分录账本、链上索引、商户回调、对账与异常恢复。

首个稳定版本聚焦 USDT-TRC20，之后通过链适配接口扩展 EVM。x402 作为独立的 scheme/network 协议扩展，不依赖 EVM，可优先在 TRON Nile 验证。

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
- 不可变出款审批审计，以及拒绝时的原子余额解冻和 Webhook 通知
- 带租约和栅栏令牌的出款签名队列，以及隔离签名器接口
- HTTPS 远程签名器客户端与 TRON FullNode 广播适配器
- 签名交易与付款地址、合约、收款地址、金额、费用及有效期的本地语义绑定
- 带租约的出款广播与确认 Worker，广播结果不确定时按原 txID 恢复
- 基于 SolidityNode 固化交易与 Receipt 的终态确认
- 成功出款原子结算、失败或安全过期出款原子解冻
- TRON 测试网只读预检与默认关闭的主网广播安全开关
- 独立的 Nile/Shasta 测试网签名服务、合约/金额白名单与持久化防重复签名
- Nile 真实 USDT 的充值入账，以及商户 API 驱动的出款、签名、广播、固化结算与安全过期恢复验证
- 可用余额与冻结余额分离查询
- 可重复读快照下的账本/业务引用与金额、借贷方向、账户归属对账，以及差异工单去重和关闭审计
- 统一托管钱包登记，以及绑定稳定账本检查点的固化 TRC20 钱包余额快照
- 链上托管总余额与同次账本检查点的资产对账，区分短款和长款并生成可审计工单
- 长运行资金 Worker 的 Prometheus 指标、存活探针和 PostgreSQL 就绪探针
- 独立业务风险监控进程，以及覆盖对账差异、出款积压、Outbox 死信、索引停滞和任务过期的 Prometheus 告警规则
- 基于租约和不可变结果的出款地址筛查 Worker；筛查缺失、非 allow 或过期时禁止批准
- 基于已固化链事件的入金来源地址筛查 Worker；只有有效 allow 才能入账，deny/review 自动隔离
- 基于固化钱包快照和完整入金来源证明的归集 Planner；只生成不可变计划，尚不签名或广播

尚未完成：归集签名/广播/确认执行、包含链上/账本/在途/通道的完整四层对账、监控仪表盘，以及基于 KMS/HSM/MPC 的生产钱包签名后端。

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

采集指定资产下全部已登记活动托管钱包的已固化 TRC20 余额快照：

```bash
export GATEWAY_WALLET_SNAPSHOT_ASSET_ID='usdt-tron-nile'
export GATEWAY_PAYOUT_TRON_SOLIDITY_NODE_URL='https://nile.trongrid.io'
export GATEWAY_TRON_API_KEY='可选的节点API密钥'

go run ./cmd/gateway-wallet-snapshot
```

迁移会把已有充值地址回填为 `deposit` 托管钱包，新充值地址也会原子登记。采集前后及落库时会核对托管账本检查点；存在待广播/待确认出款或账本变化时命令会安全失败。该命令不自动调账。

从最新固化快照生成归集计划：

```bash
export GATEWAY_SWEEP_ASSET_ID='usdt-tron-nile'
export GATEWAY_SWEEP_MINIMUM_AMOUNT='10000000'
export GATEWAY_SWEEP_MAX_SNAPSHOT_AGE='10m'
export GATEWAY_OBSERVABILITY_ADDR='127.0.0.1:9097'

go run ./cmd/gateway-sweep-planner
```

Planner 要求资产恰好有一个活动热钱包、索引游标已越过快照高度，并验证截至该高度的全部入金事件均已成功匹配且入金地址筛查结果为 `allow`；事件金额总和还必须与地址快照余额完全一致。任一证据缺失都会跳过该地址。当前进程只写不可变 `sweep_plans`，不读取私钥、不广播、不修改账本；在归集执行器上线前，这些计划不会移动资金。

登记参与快照的热钱包、冷钱包或手续费钱包：

```bash
go run ./cmd/gateway-admin register-custody-wallet \
  --asset-id 'usdt-tron-nile' \
  --address 'T...' \
  --role 'hot' \
  --actor 'ops@example.com' \
  --reason 'primary payout wallet'
```

地址会统一保存为 `41` 前缀的小写十六进制。登记操作和审计记录在同一事务提交；`deposit` 角色不能通过该命令创建。

执行账本完整性、业务引用及最新钱包资产快照对账，并查看未关闭工单：

```bash
go run ./cmd/gateway-reconcile
go run ./cmd/gateway-admin list-reconciliation-cases --limit 100
```

对账只发现差异，不会自动修改账本或业务状态。完成核实和人工处置后，通过带审计信息的命令关闭工单：

```bash
go run ./cmd/gateway-admin resolve-reconciliation-case \
  --case-id '00000000-0000-0000-0000-000000000000' \
  --actor 'ops@example.com' \
  --reason 'verified and corrected'
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

## 监控与告警

Indexer、入金筛查、充值匹配、归集规划、出款筛查、出款签名、出款执行和 Webhook Worker 默认在 `127.0.0.1:9090` 暴露：

- `GET /healthz`：进程存活探针
- `GET /readyz`：带超时的 PostgreSQL 就绪探针
- `GET /metrics`：Prometheus 指标，包括执行结果、耗时、连续失败、最后成功时间和运行状态

```bash
export GATEWAY_OBSERVABILITY_ADDR='127.0.0.1:9090'
curl http://127.0.0.1:9090/healthz
curl http://127.0.0.1:9090/readyz
curl http://127.0.0.1:9090/metrics
```

多个 Worker 在同一主机直接运行时必须分配不同端口。容器或 Kubernetes 可复用容器内端口；如果监听非回环地址，应通过网络策略限制指标端口访问。仓库提供告警规则，但不负责部署 Prometheus 或 Alertmanager。

独立启动只读业务风险监控进程：

```bash
export GATEWAY_DATABASE_URL='postgres://gateway:password@127.0.0.1:5432/gateway?sslmode=disable'
export GATEWAY_OBSERVABILITY_ADDR='127.0.0.1:9096'
export GATEWAY_MONITOR_REFRESH_INTERVAL='15s'
go run ./cmd/gateway-monitor
```

该进程在 PostgreSQL 可重复读事务内周期采集快照，Prometheus 抓取只读取内存 Gauge，不会按抓取频率查询数据库。它暴露未关闭对账工单、出款状态与最早创建时间、入金/出款筛查积压和风险决策、Outbox 积压与死信、索引游标活动、最近对账和钱包快照时间。指标只用于监控，不会自动改账或改变业务状态。

可直接加载 [`configs/prometheus/alerts.yml`](configs/prometheus/alerts.yml)；其中进程存活规则约定 Prometheus job 名为 `stablecoin-gateway-monitor`。出款等待、索引停滞、日终对账和钱包快照阈值是安全起点，上线前必须按节点固化速度、任务调度频率和业务 SLA 调整。当前索引告警检测游标是否停止活动，精确的链头高度差将在接入节点级监控后补充。

## 地址筛查

已固化的入金事件必须先得到当前有效的 `allow`，充值匹配器才会累计金额或生成账本分录。`deny/review` 会写入不可变的 `deposit_event_matches` 人工复核事实，不产生商户可用余额；Provider 故障、缺失结果和过期结果均 fail-closed。

配置 Provider 后，应先启动入金筛查 Worker，再启动充值匹配 Worker：

```bash
export GATEWAY_SCREENING_PROVIDER_URL='https://screening.internal.example'
export GATEWAY_SCREENING_PROVIDER_NAME='production-adapter'
export GATEWAY_SCREENING_PROVIDER_BEARER_TOKEN='从密钥管理服务注入'
go run ./cmd/gateway-deposit-screening-worker
go run ./cmd/gateway-deposit-worker

go run ./cmd/gateway-admin list-deposit-screenings --limit 100
```

该查询同时返回等待隔离和已经隔离的 `deny/review` 结果；`quarantined=true` 表示充值匹配器已经写入人工复核事实且未产生商户余额。后续退回、报告或解封必须依据部署地合规流程人工执行，系统不会自动处分资金。

从旧版本升级时必须先停止旧充值匹配 Worker，再执行迁移 21，并按“入金筛查 → 充值匹配”的顺序启动新进程；否则旧二进制不具备该入账闸门。

入金 Provider 请求使用 `direction=inbound`、`deposit_screening_id`、`source_address` 和平台 `destination_address`；出款请求继续使用 `direction=outbound`、`payout_id` 和 `destination_address`。两种请求均发送网络、资产、合约和最小单位金额，并以 `Idempotency-Key` 保证供应商侧幂等。

审批或拒绝待审核出款：

出款创建时会在同一数据库事务中生成地址筛查任务。先配置内部 HTTPS Provider 适配服务并启动 Screening Worker：

```bash
go run ./cmd/gateway-payout-screening-worker
```

Provider 必须实现 `POST /v1/address-screenings`，按 `Idempotency-Key` 幂等处理请求，并返回：

```json
{
  "decision": "allow",
  "reason_codes": ["low_risk"],
  "provider_reference": "provider-case-id",
  "checked_at": "2026-09-28T00:00:00Z",
  "valid_until": "2026-09-29T00:00:00Z"
}
```

核心系统只接受 HTTPS、拒绝重定向、限制响应大小，并校验决策枚举、原因码、时间窗口和最长有效期；原始响应仅保存 SHA-256 摘要，规范结果以不可变记录保存。该接口应由部署方适配真实 Chainalysis、TRM、Elliptic 或其他合规供应商，项目本身不声称本地规则可以替代供应商及合规判断。私网部署建议通过 mTLS 服务网格提供工作负载身份。

只有仍在有效期内的 `allow` 结果才能执行批准；`deny`、`review`、Provider 故障和过期结果全部 fail-closed。运营仍可随时拒绝出款并原子解冻：

```bash
go run ./cmd/gateway-admin list-payout-screenings --limit 100

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

商户 API 对外返回的 TRON 充值地址、出款地址和合约地址统一为 Base58Check，创建出款也只接受 Base58Check 目的地址。链日志、数据库及 `GATEWAY_TRON_CONTRACT` 使用小写 `41` 前缀十六进制规范值，在 HTTP 边界完成转换，避免内部比较出现同一地址的两种文本表示。

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
export GATEWAY_PAYOUT_SIGNER_ADDRESS='专用热钱包地址'
export GATEWAY_PAYOUT_SIGNER_MAX_FEE_LIMIT='100000000'
export GATEWAY_PAYOUT_SIGNER_CA_FILE='/run/secrets/signer-ca.pem'
go run ./cmd/gateway-payout-signing-worker
```

远程签名服务必须实现 `POST /v1/tron/transfers:sign`，按 `Idempotency-Key` 幂等返回同一笔完整签名交易。Worker 会再次解析签名交易，逐项核对付款地址、TRC20 合约、收款地址、金额、`fee_limit` 和有效期，不能只信任 signer 返回的 txID。私有 PKI 可通过 `GATEWAY_PAYOUT_SIGNER_CA_FILE` 追加信任根；直接 mTLS 可再成对配置 `GATEWAY_PAYOUT_SIGNER_CLIENT_CERT_FILE` 与 `GATEWAY_PAYOUT_SIGNER_CLIENT_KEY_FILE`。系统不提供跳过证书校验的开关。生产环境也可通过 mTLS 服务网格或等价工作负载身份保护该 HTTPS 链路；Bearer Token 仍必须从密钥管理服务注入，不能写入仓库。

仓库提供的 `gateway-testnet-signer` 只允许 Nile/Shasta，使用本地文件私钥，明确拒绝主网。它适合下一阶段测试网端到端验收，不是生产密钥托管方案。准备一个独立测试钱包，将 64 位十六进制私钥写入权限为 `0600` 的文件，并创建权限为 `0700` 的幂等存储目录；私钥、Bearer Token 和 TLS 私钥均不得提交到 Git：

```bash
chmod 600 /secure/path/nile-wallet.key /secure/path/signer-tls.key
install -d -m 700 /secure/path/signer-state

export GATEWAY_TESTNET_SIGNER_TLS_CERT_FILE='/secure/path/signer-tls.crt'
export GATEWAY_TESTNET_SIGNER_TLS_KEY_FILE='/secure/path/signer-tls.key'
export GATEWAY_TESTNET_SIGNER_PRIVATE_KEY_FILE='/secure/path/nile-wallet.key'
export GATEWAY_TESTNET_SIGNER_STORE_DIR='/secure/path/signer-state'
export GATEWAY_TESTNET_SIGNER_BEARER_TOKEN='从密钥管理服务注入的高熵令牌'
export GATEWAY_TESTNET_SIGNER_NETWORK='tron-nile'
export GATEWAY_TESTNET_SIGNER_FULL_NODE_URL='https://nile.trongrid.io'
export GATEWAY_TESTNET_SIGNER_OWNER_ADDRESS='测试钱包地址'
export GATEWAY_TESTNET_SIGNER_CONTRACTS='测试网 USDT 合约地址'
export GATEWAY_TESTNET_SIGNER_MAX_AMOUNT='10000000'

go run ./cmd/gateway-testnet-signer
```

签名服务固定使用 HTTPS 和 Bearer 鉴权，并在私钥使用前验证它与付款地址一致。它只签署单笔白名单 TRC20 `transfer`，对网络、合约、收款地址、金额、费用和有效期做二次校验；结果以请求 UUID 持久化后才响应。当前文件存储只支持单实例，部署多个 signer 实例前必须替换为具备一致性约束的共享存储。

配置 FullNode、SolidityNode 并启动出款执行 Worker：

```bash
export GATEWAY_TRON_NETWORK='tron-nile'
export GATEWAY_PAYOUT_TRON_FULL_NODE_URL='https://nile.trongrid.io'
export GATEWAY_PAYOUT_TRON_SOLIDITY_NODE_URL='https://nile.trongrid.io'
export GATEWAY_TRON_API_KEY='从密钥管理服务注入'
go run ./cmd/gateway-payout-execution-worker
```

上述配置已具备测试网签名、广播和确认链路。2026-09-27 已使用全新 Nile 专用钱包完成真实测试资产验收：[`e7f6ee9…aef39`](https://nile.tronscan.org/#/transaction/e7f6ee9ba5f10f119454d8df0a33b633ca6915a31f2847b82a1d5d25859aef39) 验证 signer 构造、签名、广播和 SolidityNode 固化确认；[`fa242e1b…cc82b`](https://nile.tronscan.org/#/transaction/fa242e1bdc988f926582b169944ddf6eeb64cac55c7e83e79d38e371385cc82b) 验证 Indexer 捕获真实 Transfer、Deposit Worker 匹配充值意图、双分录入账并生成 `deposit.confirmed` Outbox；[`86283b5f…e9ac`](https://nile.tronscan.org/#/transaction/86283b5fb90fc9aaf01162aff936df9c2c2b5ad7a89856ee972d691e87f0e9ac) 验证商户 API 创建出款、原子冻资、人工审批、私有 CA Signer、广播、固化确认和账本结算。同轮还验证了交易安全过期且未上链时自动失败并原子解冻。以上只是功能性实测，不等同于生产验收；生产环境仍应为节点端点配置独立容灾与监控。

在配置钱包和远程签名器前，先执行不持有私钥、不写链的 Nile 预检：

```bash
export GATEWAY_TRON_NETWORK='tron-nile'
export GATEWAY_PAYOUT_TRON_FULL_NODE_URL='https://nile.trongrid.io'
export GATEWAY_PAYOUT_TRON_SOLIDITY_NODE_URL='https://nile.trongrid.io'
export GATEWAY_TRON_PREFLIGHT_CONTRACT='TXYZopYRdj2D9XRtbG411XZZ3kM5VkAeBf'
export GATEWAY_TRON_PREFLIGHT_EXPECTED_SYMBOL='USDT'
export GATEWAY_TRON_PREFLIGHT_EXPECTED_DECIMALS='6'
go run ./cmd/gateway-tron-preflight
```

预检会拒绝主网、过旧或超前的链头、固化高度异常以及合约元数据不匹配。Nile Endpoint 与 Faucet 信息以 [TRON 官方网络文档](https://developers.tron.network/docs/networks)和[测试币文档](https://developers.tron.network/docs/getting-testnet-tokens-on-tron)为准。出款执行进程连接 `tron-mainnet` 时还必须显式设置 `GATEWAY_TRON_MAINNET_ENABLED=true`；这只是防误操作开关，不替代发布审批、限额和审计。

## Webhook

当前投递 `deposit.confirmed`、`payout.rejected`、`payout.succeeded` 和 `payout.failed`。充值事件与充值入账同事务创建；出款拒绝事件与审批审计、余额解冻同事务创建；其他出款事件与终态账本结算或解冻同事务创建。事件信封固定为：

```json
{"id":"事件 UUID","type":"事件类型","created_at":"RFC3339 时间","data":{}}
```

出款终态事件 `data` 包含 `payout_id`、`merchant_id`、`merchant_reference`、`asset_id`、`network`、`destination_address`、`amount`、`status`、`transaction_id` 和 `ledger_transaction_id`；`payout.failed` 额外包含 `failure_reason`。`payout.rejected` 尚无链上交易，因此不包含 `transaction_id`，额外包含稳定的 `reason_code` 和 `reviewed_at`；内部审批人和审批备注只进入审计表，不向商户泄露。TRON 目的地址按商户 API 约定使用 Base58Check。

请求包含 `X-Gateway-Event-ID`、`X-Gateway-Event-Type`、`X-Gateway-Event-Timestamp` 和 `X-Gateway-Signature`。签名值为：

```text
v1=HEX(HMAC_SHA256(secret, timestamp + "." + event_id + "." + raw_body))
```

只有 2xx 响应视为成功。投递采用至少一次语义；商户必须以 `X-Gateway-Event-ID` 幂等消费。失败会指数退避并在达到上限后进入死信。

## 网络测试门槛

- 测试网：Nile 真实充值入账与商户 API 驱动的出款成功闭环已经完成，并验证了一条安全过期恢复路径；下一阶段补齐更多故障注入、持续运行、对账与监控。
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
- 仓库内文件密钥 signer 仅限测试网；生产签名必须改用独立 KMS/HSM/MPC 后端。
- 主网能力必须显式启用，并通过发布检查清单。
- 地址筛查、KYC、AML 与牌照责任由部署运营方根据所在司法辖区落实。
- 在安全审计、灾难恢复演练和资金对账验收完成前，不应承载真实资金。

## License

Apache-2.0，详见 [LICENSE](LICENSE)。
