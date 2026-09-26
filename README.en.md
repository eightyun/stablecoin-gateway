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
- Transactional Outbox-based webhook worker
- Webhook HMAC signatures, exponential backoff, dead lettering, and per-attempt delivery audit
- Webhook SSRF protection that blocks private targets and redirects by default
- Merchant payout requests with TRON address validation and atomic balance freezing
- Audited payout review with atomic unfreezing on rejection
- Leased payout-signing queue with fencing tokens and an isolated signer interface
- Separate available and frozen balance reporting

Not yet implemented: a remote signer adapter, automated address screening, payout broadcasting/confirmation, wallet sweeping, reconciliation, and production wallet-signing infrastructure.

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

Configure a separate webhook encryption key and create an HTTPS endpoint for a merchant:

```bash
# Generate and persist this key once; do not reuse the API-key encryption master key
openssl rand -base64 32
export GATEWAY_WEBHOOK_SECRET_ENCRYPTION_KEYS='{"v1":"REPLACE_WITH_THE_PERSISTED_BASE64_KEY"}'
export GATEWAY_WEBHOOK_SECRET_ACTIVE_VERSION='v1'

go run ./cmd/gateway-admin create-webhook-endpoint \
  --merchant-id '00000000-0000-0000-0000-000000000000' \
  --name 'production' \
  --url 'https://merchant.example/webhooks/gateway'

go run ./cmd/gateway-webhook-worker
```

The worker rejects private, loopback, and link-local targets and does not follow redirects by default. Set `GATEWAY_WEBHOOK_ALLOW_PRIVATE_NETWORKS=true` only when delivering to a trusted internal network is intentional.

Approve or reject a pending payout:

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

Approval moves the payout to `approved`. Rejection returns frozen funds to the available balance in the same database transaction. Retrying an identical decision is idempotent; conflicting decisions fail.

## Merchant API

Available endpoints:

- `POST /v1/deposits`: idempotently create a deposit intent from the merchant's address pool
- `GET /v1/deposits/{id}`: retrieve deposit status
- `GET /v1/balances`: retrieve available and frozen balances by asset
- `POST /v1/payouts`: idempotently create a payout and atomically freeze funds
- `GET /v1/payouts/{id}`: retrieve payout status

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

New payouts enter `pending_review` and move funds from `available` to `frozen`; approval moves them to `approved`. A signing worker claims jobs through database leases and calls only the private-key-free `TransferSigner` boundary. A payout reaches `ready_for_broadcast` only after one immutable signed transaction has been persisted. The repository does not yet include a remote signer or node broadcaster adapter, so no on-chain transaction is produced. The initial payout rail accepts TRON Base58Check addresses only.

## Webhooks

The current event type is `deposit.confirmed`. The event envelope is stable:

```json
{"id":"event UUID","type":"deposit.confirmed","created_at":"RFC3339 timestamp","data":{}}
```

Requests include `X-Gateway-Event-ID`, `X-Gateway-Event-Type`, `X-Gateway-Event-Timestamp`, and `X-Gateway-Signature`. The signature is:

```text
v1=HEX(HMAC_SHA256(secret, timestamp + "." + event_id + "." + raw_body))
```

Only 2xx responses are successful. Delivery is at least once, so merchants must consume idempotently using `X-Gateway-Event-ID`. Failures use exponential backoff and become dead letters after the configured attempt limit.

## Network Testing Gates

- Testnet starts after payout approval/screening, an isolated signer interface, and TRON broadcast and confirmation workers are implemented, at roughly 75%–80% first-release completion.
- Mainnet canarying starts only after sustained testnet operation, three-way reconciliation, monitoring and alerting, disaster-recovery exercises, and an external security audit pass.
- Mainnet is never a general test environment. Every mainnet canary requires a defined loss limit, dual approval, and an emergency stop.

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
