# stablecoin-gateway

[简体中文](README.md) | English

[stablecoin-gateway](https://github.com/eightyun/stablecoin-gateway) is a production-oriented, open-source stablecoin payment system for merchants. Its target scope includes deposits, payouts, double-entry accounting, blockchain indexing, merchant webhooks, reconciliation, and failure recovery.

The first stable release focuses on USDT-TRC20, with EVM networks added later through chain adapters. x402 remains an independent scheme/network extension, does not depend on EVM, and can be validated on TRON Nile first.

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
- Immutable payout-review audit with atomic unfreezing and webhook notification on rejection
- Leased payout-signing queue with fencing tokens and an isolated signer interface
- HTTPS remote-signer client and TRON FullNode broadcast adapter
- Local semantic binding of signed transactions to owner, contract, destination, amount, fee, and lifetime
- Leased payout broadcast and confirmation worker with recovery by the original txID
- Final payout confirmation from SolidityNode transactions and receipts
- Atomic settlement on success and atomic fund release on safe failure or expiry
- Read-only TRON testnet preflight and a mainnet broadcast safety switch that is off by default
- Isolated Nile/Shasta testnet signer with contract/amount policy and durable replay protection
- Live Nile USDT validation covering deposit posting plus merchant-API-driven payout, signing, broadcast, finalized settlement, and safe expiry recovery
- Separate available and frozen balance reporting
- Repeatable-read reconciliation of ledger references, amounts, debit/credit direction, and account ownership, with deduplicated cases and audited resolution
- Unified custody-wallet registration and finalized TRC20 wallet snapshots bound to stable ledger checkpoints
- Custody asset reconciliation between on-chain totals and the ledger checkpoint captured with the same snapshot, with separate shortfall and excess cases
- Prometheus metrics, liveness probes, and PostgreSQL readiness probes for long-running fund workers
- A dedicated business-risk monitor with Prometheus alerts for reconciliation findings, payout backlog, Outbox dead letters, stalled indexing, and stale scheduled controls
- A leased payout-address screening worker with immutable results; missing, non-allow, or expired screening evidence blocks approval
- A finalized-event-driven inbound screening worker; only a current allow can be credited, while deny/review results are quarantined
- A finalized-snapshot sweep planner plus a signing worker with balance preflight, fenced leases, and a dedicated signer endpoint

Not yet implemented: sweep broadcast/finality execution, complete four-layer reconciliation across chain, ledger, in-flight funds, and providers, monitoring dashboards, and a production KMS/HSM/MPC signing backend.

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

Capture finalized TRC20 balances for every registered active custody wallet of an asset:

```bash
export GATEWAY_WALLET_SNAPSHOT_ASSET_ID='usdt-tron-nile'
export GATEWAY_PAYOUT_TRON_SOLIDITY_NODE_URL='https://nile.trongrid.io'
export GATEWAY_TRON_API_KEY='OPTIONAL_NODE_API_KEY'

go run ./cmd/gateway-wallet-snapshot
```

The migration backfills existing deposit addresses as `deposit` custody wallets, and new deposit addresses are registered atomically. The command checks the custody-ledger checkpoint before and after chain reads and again while saving; it fails safely if the ledger changes or a payout is awaiting broadcast/finality. It never adjusts balances automatically.

Create sweep plans from the latest finalized snapshot:

```bash
export GATEWAY_SWEEP_ASSET_ID='usdt-tron-nile'
export GATEWAY_SWEEP_MINIMUM_AMOUNT='10000000'
export GATEWAY_SWEEP_MAX_SNAPSHOT_AGE='10m'
export GATEWAY_OBSERVABILITY_ADDR='127.0.0.1:9097'

go run ./cmd/gateway-sweep-planner
```

The planner requires exactly one active hot wallet, an indexer cursor beyond the snapshot height, and proof that every inbound event through that height was matched and allowed by inbound address screening. The event sum must exactly equal the snapshotted address balance. Missing evidence fails closed. The current process only writes immutable `sweep_plans`; it never reads private keys, broadcasts transactions, or changes the ledger, so no funds move until the sweep executor is implemented.

Configure an isolated signer and start the sweep-signing worker:

```bash
export GATEWAY_TRON_NETWORK='tron-nile'
export GATEWAY_SWEEP_TRON_SOLIDITY_NODE_URL='https://nile.trongrid.io'
export GATEWAY_SWEEP_SIGNER_URL='https://signer.internal.example'
export GATEWAY_SWEEP_SIGNER_BEARER_TOKEN='INJECT_FROM_A_SECRET_MANAGER'
export GATEWAY_SWEEP_SIGNER_MAX_FEE_LIMIT='100000000'

go run ./cmd/gateway-sweep-signing-worker
```

After claiming a plan, the worker reads the source balance between two identical finalized heads. It refuses to sign if the balance is below the planned amount or the head changes. The sweep signer must implement `POST /v1/tron/sweeps:sign`, explicitly bind `source_address`, and idempotently return the same transaction for the plan UUID. The client reparses the transaction and verifies its source, contract, hot-wallet destination, amount, fee, and lifetime. Success only moves the execution to `ready_for_broadcast`; this stage still does not broadcast or modify the ledger. The repository testnet signer supports this endpoint but only for its single configured source key; production deployments need a KMS/HSM/MPC backend that authorizes the source address and selects its key.

Register a hot, cold, or fee wallet that must be included in snapshots:

```bash
go run ./cmd/gateway-admin register-custody-wallet \
  --asset-id 'usdt-tron-nile' \
  --address 'T...' \
  --role 'hot' \
  --actor 'ops@example.com' \
  --reason 'primary payout wallet'
```

Addresses are stored as normalized lowercase `41`-prefixed hex. The wallet and immutable registration audit commit in one transaction; the command cannot create `deposit` wallets.

Run ledger-integrity, business-reference, and latest wallet-asset reconciliation, then list open cases:

```bash
go run ./cmd/gateway-reconcile
go run ./cmd/gateway-admin list-reconciliation-cases --limit 100
```

Reconciliation only detects discrepancies; it never changes ledger or business state automatically. After verification and manual remediation, resolve the case with audited operator details:

```bash
go run ./cmd/gateway-admin resolve-reconciliation-case \
  --case-id '00000000-0000-0000-0000-000000000000' \
  --actor 'ops@example.com' \
  --reason 'verified and corrected'
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

## Monitoring and Alerts

The indexer, inbound screener, deposit matcher, sweep planner, sweep signer, payout screener, payout signer, payout executor, and webhook worker expose these endpoints on `127.0.0.1:9090` by default:

- `GET /healthz`: process liveness
- `GET /readyz`: timeout-bounded PostgreSQL readiness
- `GET /metrics`: Prometheus operation results, duration, consecutive failures, last-success time, and running state

```bash
export GATEWAY_OBSERVABILITY_ADDR='127.0.0.1:9090'
curl http://127.0.0.1:9090/healthz
curl http://127.0.0.1:9090/readyz
curl http://127.0.0.1:9090/metrics
```

Assign a different port to each worker when several run directly on the same host. Containers or Kubernetes may reuse the container-local port. If binding beyond loopback, restrict the metrics port with network policy. The repository provides alert rules but does not deploy Prometheus or Alertmanager.

Start the independent, read-only business-risk monitor:

```bash
export GATEWAY_DATABASE_URL='postgres://gateway:password@127.0.0.1:5432/gateway?sslmode=disable'
export GATEWAY_OBSERVABILITY_ADDR='127.0.0.1:9096'
export GATEWAY_MONITOR_REFRESH_INTERVAL='15s'
go run ./cmd/gateway-monitor
```

The process periodically captures a repeatable-read PostgreSQL snapshot. Prometheus scrapes in-memory gauges and therefore does not query the database at scrape frequency. Metrics cover open reconciliation cases, payout status and oldest creation time, inbound/outbound screening backlog and risk decisions, Outbox backlog and dead letters, indexer cursor activity, and the latest reconciliation and wallet-snapshot timestamps. They are observational only and never mutate ledger or business state.

Load [`configs/prometheus/alerts.yml`](configs/prometheus/alerts.yml) into Prometheus. The process-down rule assumes the scrape job is named `stablecoin-gateway-monitor`. The payout, cursor, reconciliation, and wallet-snapshot thresholds are safe starting points and must be tuned to chain finality, scheduling frequency, and production SLAs. Cursor staleness currently detects stopped indexer activity; exact chain-head lag will follow with node-level monitoring.

## Address Screening

A finalized inbound event must have a current `allow` before the deposit matcher can accumulate it or post ledger entries. `deny/review` creates an immutable manual-review `deposit_event_matches` fact without creating merchant available balance. Provider failure, missing evidence, and expired evidence all fail closed.

Configure the provider, then start the inbound screener before the deposit matcher:

```bash
export GATEWAY_SCREENING_PROVIDER_URL='https://screening.internal.example'
export GATEWAY_SCREENING_PROVIDER_NAME='production-adapter'
export GATEWAY_SCREENING_PROVIDER_BEARER_TOKEN='INJECT_FROM_SECRET_MANAGER'
go run ./cmd/gateway-deposit-screening-worker
go run ./cmd/gateway-deposit-worker

go run ./cmd/gateway-admin list-deposit-screenings --limit 100
```

The listing includes both pending-isolation and already quarantined `deny/review` findings. `quarantined=true` means the deposit matcher has written the manual-review fact without creating merchant balance. Return, reporting, or release actions remain explicit compliance operations; the system never disposes of funds automatically.

When upgrading, stop the old deposit matcher before applying migration 21, then start the new processes in inbound-screener → deposit-matcher order. An old binary does not contain this crediting gate.

Inbound provider requests use `direction=inbound`, `deposit_screening_id`, `source_address`, and the platform `destination_address`. Outbound requests retain `direction=outbound`, `payout_id`, and `destination_address`. Both include network, asset, contract, and smallest-unit amount, and require provider-side idempotency through `Idempotency-Key`.

Approve or reject a pending payout:

Payout creation atomically creates an address-screening job. Configure an internal HTTPS provider adapter and start the screening worker first:

```bash
go run ./cmd/gateway-payout-screening-worker
```

The provider must implement `POST /v1/address-screenings`, process `Idempotency-Key` idempotently, and return:

```json
{
  "decision": "allow",
  "reason_codes": ["low_risk"],
  "provider_reference": "provider-case-id",
  "checked_at": "2026-09-28T00:00:00Z",
  "valid_until": "2026-09-29T00:00:00Z"
}
```

The core accepts HTTPS only, rejects redirects, bounds response size, and validates decisions, reason codes, timestamps, and maximum validity. It stores a SHA-256 digest of the raw response and an immutable normalized result. Deployments should adapt this contract to Chainalysis, TRM, Elliptic, or another compliance provider; the project does not claim that a local list replaces vendor data or compliance judgment. For private networking, use an mTLS service mesh or equivalent workload identity.

Approval requires a current `allow` result. `deny`, `review`, provider failure, and expired results all fail closed. Operators may still reject a payout and atomically release frozen funds:

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

Approval moves the payout to `approved`. Rejection returns frozen funds to the available balance in the same database transaction. Retrying an identical decision is idempotent; conflicting decisions fail.

## Merchant API

Available endpoints:

- `POST /v1/deposits`: idempotently create a deposit intent from the merchant's address pool
- `GET /v1/deposits/{id}`: retrieve deposit status
- `GET /v1/balances`: retrieve available and frozen balances by asset
- `POST /v1/payouts`: idempotently create a payout and atomically freeze funds
- `GET /v1/payouts/{id}`: retrieve payout status

Deposit creation requires `Idempotency-Key`. Amounts are decimal integer strings in the asset's smallest unit. For example, 1 USDT with 6 decimals is `"1000000"`.

Merchant API responses expose TRON deposit, payout, and contract addresses as Base58Check, and payout creation accepts only a Base58Check destination. Chain logs, database records, and `GATEWAY_TRON_CONTRACT` use canonical lowercase `41`-prefixed hexadecimal values; conversion happens only at the HTTP boundary so internal comparisons never mix two textual forms of the same address.

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

New payouts enter `pending_review` and move funds from `available` to `frozen`; approval moves them to `approved`. A signing worker claims jobs through database leases and calls only the private-key-free `TransferSigner` boundary. A payout reaches `ready_for_broadcast` only after one immutable signed transaction has been persisted. The execution worker broadcasts that exact transaction and accepts only a finalized SolidityNode transaction and receipt as the financial terminal state. A broadcast timeout continues tracking the original txID instead of creating a second payment. The initial payout rail accepts TRON Base58Check addresses only.

Configure and start the payout-signing worker:

```bash
export GATEWAY_PAYOUT_SIGNER_URL='https://signer.internal.example'
export GATEWAY_PAYOUT_SIGNER_BEARER_TOKEN='injected-by-your-secrets-manager'
export GATEWAY_PAYOUT_SIGNER_ADDRESS='dedicated-hot-wallet-address'
export GATEWAY_PAYOUT_SIGNER_MAX_FEE_LIMIT='100000000'
export GATEWAY_PAYOUT_SIGNER_CA_FILE='/run/secrets/signer-ca.pem'
go run ./cmd/gateway-payout-signing-worker
```

The remote service must implement `POST /v1/tron/transfers:sign` and return the same complete signed transaction for repeated `Idempotency-Key` values. The worker parses the result again and binds it to the expected owner, TRC20 contract, destination, amount, `fee_limit`, and lifetime instead of trusting only the returned txID. Private PKI roots can be appended with `GATEWAY_PAYOUT_SIGNER_CA_FILE`; direct mTLS additionally accepts a paired `GATEWAY_PAYOUT_SIGNER_CLIENT_CERT_FILE` and `GATEWAY_PAYOUT_SIGNER_CLIENT_KEY_FILE`. There is no option to skip certificate verification. Production deployments may also protect this HTTPS path with a mutual-TLS service mesh or equivalent workload identity. The bearer token must still be injected from a secrets manager.

The included `gateway-testnet-signer` accepts only Nile or Shasta. It uses a local file key and explicitly rejects mainnet, so it is intended for testnet end-to-end acceptance rather than production key custody. Prepare a dedicated test wallet, store its 64-character hexadecimal private key in a `0600` file, and create a `0700` idempotency directory. Never commit the wallet key, bearer token, or TLS private key:

```bash
chmod 600 /secure/path/nile-wallet.key /secure/path/signer-tls.key
install -d -m 700 /secure/path/signer-state

export GATEWAY_TESTNET_SIGNER_TLS_CERT_FILE='/secure/path/signer-tls.crt'
export GATEWAY_TESTNET_SIGNER_TLS_KEY_FILE='/secure/path/signer-tls.key'
export GATEWAY_TESTNET_SIGNER_PRIVATE_KEY_FILE='/secure/path/nile-wallet.key'
export GATEWAY_TESTNET_SIGNER_STORE_DIR='/secure/path/signer-state'
export GATEWAY_TESTNET_SIGNER_BEARER_TOKEN='high-entropy-token-from-a-secret-manager'
export GATEWAY_TESTNET_SIGNER_NETWORK='tron-nile'
export GATEWAY_TESTNET_SIGNER_FULL_NODE_URL='https://nile.trongrid.io'
export GATEWAY_TESTNET_SIGNER_OWNER_ADDRESS='test-wallet-address'
export GATEWAY_TESTNET_SIGNER_CONTRACTS='testnet-usdt-contract-address'
export GATEWAY_TESTNET_SIGNER_MAX_AMOUNT='10000000'

go run ./cmd/gateway-testnet-signer
```

The signer requires HTTPS and bearer authentication and verifies that the key matches the configured owner before serving. It signs only one allowlisted TRC20 `transfer` after revalidating the network, contract, destination, amount, fee, and lifetime. A UUID-bound result is durably persisted before the response is returned. The current file store is single-instance; replace it with a consistency-enforced shared store before running multiple signer replicas.

Configure the FullNode and SolidityNode endpoints, then start the payout execution worker:

```bash
export GATEWAY_TRON_NETWORK='tron-nile'
export GATEWAY_PAYOUT_TRON_FULL_NODE_URL='https://nile.trongrid.io'
export GATEWAY_PAYOUT_TRON_SOLIDITY_NODE_URL='https://nile.trongrid.io'
export GATEWAY_TRON_API_KEY='injected-by-your-secrets-manager'
go run ./cmd/gateway-payout-execution-worker
```

This configuration provides the testnet signing, broadcast, and confirmation path. On 2026-09-27, a fresh Nile-only wallet completed live test-asset checks: [`e7f6ee9…aef39`](https://nile.tronscan.org/#/transaction/e7f6ee9ba5f10f119454d8df0a33b633ca6915a31f2847b82a1d5d25859aef39) covered signer construction, signing, broadcast, and SolidityNode finalization; [`fa242e1b…cc82b`](https://nile.tronscan.org/#/transaction/fa242e1bdc988f926582b169944ddf6eeb64cac55c7e83e79d38e371385cc82b) covered Indexer ingestion of a real Transfer, Deposit Worker intent matching, double-entry posting, and creation of the `deposit.confirmed` Outbox event; and [`86283b5f…e9ac`](https://nile.tronscan.org/#/transaction/86283b5fb90fc9aaf01162aff936df9c2c2b5ad7a89856ee972d691e87f0e9ac) covered merchant API payout creation, atomic freezing, manual approval, a private-CA signer, broadcast, finalization, and ledger settlement. The same run also verified automatic failure and atomic fund release when a transaction safely expired without reaching the chain. This is functional testnet evidence rather than production acceptance; production deployments still need independent node failover and monitoring.

Before configuring a wallet and remote signer, run the read-only Nile preflight, which holds no private key and writes nothing on-chain:

```bash
export GATEWAY_TRON_NETWORK='tron-nile'
export GATEWAY_PAYOUT_TRON_FULL_NODE_URL='https://nile.trongrid.io'
export GATEWAY_PAYOUT_TRON_SOLIDITY_NODE_URL='https://nile.trongrid.io'
export GATEWAY_TRON_PREFLIGHT_CONTRACT='TXYZopYRdj2D9XRtbG411XZZ3kM5VkAeBf'
export GATEWAY_TRON_PREFLIGHT_EXPECTED_SYMBOL='USDT'
export GATEWAY_TRON_PREFLIGHT_EXPECTED_DECIMALS='6'
go run ./cmd/gateway-tron-preflight
```

The preflight rejects mainnet, stale or future-dated heads, excessive finality lag, and mismatched token metadata. Use the [official TRON network documentation](https://developers.tron.network/docs/networks) and [test-token guide](https://developers.tron.network/docs/getting-testnet-tokens-on-tron) as the source of truth for Nile endpoints and faucets. A payout execution worker connected to `tron-mainnet` also requires `GATEWAY_TRON_MAINNET_ENABLED=true`; this guard does not replace release approval, limits, or audits.

## Webhooks

Current event types are `deposit.confirmed`, `payout.rejected`, `payout.succeeded`, and `payout.failed`. Deposit events are created in the same transaction as deposit posting. Payout rejection events are created atomically with the review audit and fund release; other payout events are created in the same transaction as terminal settlement or fund release. The event envelope is stable:

```json
{"id":"event UUID","type":"event type","created_at":"RFC3339 timestamp","data":{}}
```

Terminal payout event `data` includes `payout_id`, `merchant_id`, `merchant_reference`, `asset_id`, `network`, `destination_address`, `amount`, `status`, `transaction_id`, and `ledger_transaction_id`; `payout.failed` additionally includes `failure_reason`. Since `payout.rejected` has no on-chain transaction, it omits `transaction_id` and additionally includes a stable `reason_code` and `reviewed_at`. Internal reviewer identity and review notes remain audit-only and are never exposed to merchants. TRON destinations use Base58Check, matching the merchant API.

Requests include `X-Gateway-Event-ID`, `X-Gateway-Event-Type`, `X-Gateway-Event-Timestamp`, and `X-Gateway-Signature`. The signature is:

```text
v1=HEX(HMAC_SHA256(secret, timestamp + "." + event_id + "." + raw_body))
```

Only 2xx responses are successful. Delivery is at least once, so merchants must consume idempotently using `X-Gateway-Event-ID`. Failures use exponential backoff and become dead letters after the configured attempt limit.

## Network Testing Gates

- Nile now covers both live deposit posting and a merchant-API-driven successful payout loop, plus one safe-expiry recovery path. The next phase adds broader fault injection, sustained operation, reconciliation, and monitoring.
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
- The repository's file-key signer is testnet-only; production signing must use an independent KMS/HSM/MPC backend.
- Mainnet capabilities must be explicitly enabled and pass a release checklist.
- Deployers are responsible for address screening, KYC, AML, and licensing obligations in their jurisdictions.
- Do not process real funds before completing a security audit, disaster-recovery exercises, and reconciliation acceptance tests.

## License

Apache-2.0. See [LICENSE](LICENSE).
