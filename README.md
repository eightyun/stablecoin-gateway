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

尚未完成：商户 API、自动充值过账、Webhook、出款、归集、风控筛查、对账和生产密钥基础设施。

## 本地运行

要求：

- Go 1.23 或更高版本
- PostgreSQL

启动 API：

```bash
go run ./cmd/gateway-api
curl http://127.0.0.1:8080/healthz
```

数据库迁移：

```bash
export GATEWAY_DATABASE_URL='postgres://gateway:password@127.0.0.1:5432/gateway?sslmode=disable'
go run ./cmd/gateway-migrate up
```

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
