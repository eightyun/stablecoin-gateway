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
- Atomic double-entry posting and `deposit.confirmed` Outbox events for exact deposits
- HMAC-SHA256 merchant authentication with clock-skew validation and persistent nonce replay protection
- Encrypted merchant API secrets with versioned key rotation
- Atomic address-pool allocation and idempotent deposit-intent creation
- Merchant deposit lookup and available-balance APIs

Not yet implemented: webhook delivery, payouts, wallet sweeping, risk screening, reconciliation, and production wallet-signing infrastructure.

## Local Development

Requirements:

- Go 1.23 or newer
- PostgreSQL

Generate an API-secret encryption key and start the API:

```bash
# Generate once and persist the result in a secrets manager
openssl rand -base64 32

export GATEWAY_DATABASE_URL='postgres://gateway:password@127.0.0.1:5432/gateway?sslmode=disable'
export GATEWAY_API_KEY_ENCRYPTION_KEYS='{"v1":"REPLACE_WITH_THE_PERSISTED_BASE64_KEY"}'
export GATEWAY_API_KEY_ACTIVE_VERSION='v1'

go run ./cmd/gateway-api
curl http://127.0.0.1:8080/healthz
```

Apply database migrations:

```bash
export GATEWAY_DATABASE_URL='postgres://gateway:password@127.0.0.1:5432/gateway?sslmode=disable'
go run ./cmd/gateway-migrate up
```

Create API credentials for an existing active merchant:

```bash
go run ./cmd/gateway-admin create-api-key \
  --merchant-id '00000000-0000-0000-0000-000000000000' \
  --name 'production'
```

The secret is returned only once. PostgreSQL stores only AES-256-GCM ciphertext. Existing secrets cannot be recovered if the master key is lost, so production deployments must persist and inject it through a secrets manager.

## Merchant API

Available endpoints:

- `POST /v1/deposits`: idempotently create a deposit intent from the merchant's address pool
- `GET /v1/deposits/{id}`: retrieve deposit status
- `GET /v1/balances`: retrieve available balances by asset

Deposit creation requires `Idempotency-Key`. Amounts are decimal integer strings in the asset's smallest unit. For example, 1 USDT with 6 decimals is `"1000000"`.

Every merchant request requires:

- `X-Gateway-Key`
- `X-Gateway-Timestamp`: Unix seconds
- `X-Gateway-Nonce`: a 16–128 character URL-safe random value, unique per API key
- `X-Gateway-Signature`: lowercase hexadecimal HMAC-SHA256

The canonical string is:

```text
UPPERCASE_METHOD\nREQUEST_URI\nTIMESTAMP\nNONCE\nSHA256_HEX(BODY)
```

`REQUEST_URI` includes the query string. The server accepts a five-minute clock skew by default and atomically consumes the nonce after signature verification.

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
