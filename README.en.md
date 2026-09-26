# Gateway

[简体中文](README.md) | English

Gateway is a production-oriented, open-source stablecoin payment system for merchants. Its target scope includes deposits, payouts, double-entry accounting, blockchain indexing, merchant webhooks, reconciliation, and failure recovery.

The first stable release focuses on USDT-TRC20. EVM networks will be added through chain adapters, while x402 support will remain an independent extension.

## Project Status

This project is under active early-stage development. It has not been security audited and must not be used to hold or move real funds yet.

Implemented components:

- HTTP service scaffold and health check
- Versioned PostgreSQL migrations
- Double-entry ledger invariants and idempotent posting
- Transactional Outbox foundation
- Deterministic TRON simulator
- SolidityNode finalized-block reader and TRC20 log decoder
- Durable scan cursor with leases and fencing tokens
- Standalone TRON indexer worker
- Deposit addresses, deposit intents, and chain-event matching model
- Standalone deposit matching and intent-expiration worker

Not yet implemented: merchant APIs, automatic deposit posting, webhooks, payouts, wallet sweeping, risk screening, reconciliation, and production key infrastructure.

## Local Development

Requirements:

- Go 1.23 or newer
- PostgreSQL

Start the API:

```bash
go run ./cmd/gateway-api
curl http://127.0.0.1:8080/healthz
```

Apply database migrations:

```bash
export GATEWAY_DATABASE_URL='postgres://gateway:password@127.0.0.1:5432/gateway?sslmode=disable'
go run ./cmd/gateway-migrate up
```

Before starting the TRON indexer, configure the node, asset contract, initial scan height, and its parent block hash using [.env.example](.env.example), then run:

```bash
go run ./cmd/gateway-indexer
```

Start the deposit matching worker:

```bash
go run ./cmd/gateway-deposit-worker
```

The indexer reads finalized blocks only. It does not hold private keys or broadcast transactions. The deposit worker consumes persisted chain events and supports multiple concurrent instances. Database migrations must be applied independently before starting API or worker processes.

## Security Boundary

- Application services must never store or log plaintext private keys.
- Production signing must use an isolated remote signer, HSM, or MPC adapter.
- Mainnet capabilities must be explicitly enabled and pass a release checklist.
- Deployers are responsible for address screening, KYC, AML, and licensing obligations in their jurisdictions.
- Do not process real funds before completing a security audit, disaster-recovery exercises, and reconciliation acceptance tests.

## License

Apache-2.0. See [LICENSE](LICENSE).
