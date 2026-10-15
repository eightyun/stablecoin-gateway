# stablecoin-gateway

[简体中文](README.md) | English

[![CI](https://github.com/eightyun/stablecoin-gateway/actions/workflows/ci.yml/badge.svg)](https://github.com/eightyun/stablecoin-gateway/actions/workflows/ci.yml)

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
- Multi-endpoint TRON FullNode/SolidityNode failover with Prometheus alerts for reads, transaction construction, and same-txID broadcast
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
- Production signer backed by a non-exportable AWS KMS secp256k1 key and cross-instance PostgreSQL idempotency
- Dedicated KMS signer health probes, bounded request metrics, redacted audit logs, and signing request deadlines
- Standalone KMS acceptance command for public-key checks or random challenge signing with redacted JSON output
- Live Nile USDT validation covering deposit posting plus merchant-API-driven payout, signing, broadcast, finalized settlement, and safe expiry recovery
- Separate available and frozen balance reporting
- Repeatable-read reconciliation of ledger references, amounts, debit/credit direction, and account ownership, with deduplicated cases and audited resolution
- Unified custody-wallet registration and finalized TRC20 wallet snapshots bound to stable ledger checkpoints
- Bounded custody reconciliation across finalized wallet balances, ledger checkpoints, and payout/sweep in-flight evidence, with separate shortfall and excess cases
- Prometheus metrics, liveness probes, and PostgreSQL readiness probes for long-running fund workers
- A dedicated business-risk monitor with Prometheus alerts for reconciliation findings, payout backlog, Outbox dead letters, stalled indexing, and stale scheduled controls
- A leased payout-address screening worker with immutable results; missing, non-allow, or expired screening evidence blocks approval
- A finalized-event-driven inbound screening worker; only a current allow can be credited, while deny/review results are quarantined
- Pre-sweep screening bound to the source, hot-wallet destination, finalized snapshot, and exact amount; only a current allow can create a plan
- A finalized-snapshot sweep planner with balance preflight, isolated signing, exact-transaction broadcast recovery, and finalized execution state
- A read-only pre-activation audit for restored PostgreSQL databases covering migration compatibility, ledger/business references, wallet assets, open cases, pending work, and expired leases
- GitHub Actions release gates covering builds, unit tests, per-package isolated PostgreSQL integration tests, race-enabled node-failover soak tests, and Prometheus rule validation
- Grafana operations dashboard for fund tasks, screening, reconciliation, indexing, workers, nodes, and optional signers, with CI query and snapshot-freshness validation

Not yet implemented: external fiat/PSP balance adapters and reconciliation, and HSM/MPC or hierarchical key routing for sweeping large address sets. The current AWS KMS signer targets a single payout hot wallet; mainnet deployment still requires a security audit and controlled pre-production validation.

v0.1 requires neither an AWS account nor live AWS KMS acceptance. The KMS adapter and acceptance command are optional deployment capabilities; not using AWS does not block development or release. Core acceptance uses the existing automated tests and isolated testnet signer. Production deployers choose their signing backend and validate the implementation they actually use.

## Release Packages and Deployment

Packages currently target testnet release candidates, not approved mainnet deployments. Supported targets are Linux/macOS on amd64/arm64. Building requires Git and Go; running an installed package does not require Go.

```bash
# Run from a clean, committed checkout; omitted version defaults to snapshot-COMMIT
go run ./tools/release -target linux/amd64 -version v0.1.0-rc.1
cd build/release
sha256sum --check stablecoin-gateway_v0.1.0-rc.1_linux_amd64.tar.gz.sha256
# On macOS use shasum -a 256 --check with the corresponding .sha256 file
tar -xzf stablecoin-gateway_v0.1.0-rc.1_linux_amd64.tar.gz
```

`v0.1.0-rc.1` is an example, not an existing release. These commands do not create a Git tag or GitHub Release. Alternatively, manually run **Build release packages** in Actions and download the platform Artifact (retained for 14 days). That workflow can read source and upload Artifacts, but cannot publish a Release. Extract the outer Actions ZIP before checking the enclosed `.tar.gz`.

Each package includes all `gateway-*` commands, embedded migrations, both READMEs, license, security policy, public configuration examples, and `manifest.json`. The manifest records version, Git revision, commit time, dirty state, Go version, target, and each file's SHA-256. `go version -m bin/gateway-api` displays embedded VCS metadata. Archive timestamps and ordering are fixed; a clean commit, identical toolchain, and target produce repeatable packages. Existing outputs are never overwritten. Local-only `-allow-dirty` builds use a `_dirty` filename suffix and must not be published as releases. Checksums detect content changes; they do not replace publisher signatures, an audit, or a trusted download source.

Only explicitly allowlisted public files are packaged. Actual `.env` files, private keys, signer state, and local `docs/` are excluded. Never put credentials in `.env.example`; copy templates outside the repository and inject only the settings each process needs.

### Single-host Testnet Deployment (Linux + systemd)

These steps assume supported Linux/systemd, separate PostgreSQL, trusted nodes, and configured screening/isolated signer services. Establish backups, least-privilege database roles, and network controls first. Validate certificates for remote PostgreSQL; local `sslmode=disable` examples are not remote deployment defaults. Keep migration and runtime roles separate. The service template is for the API and long-running workers without local private keys, not signers, migrations, or scheduled commands.

```bash
# Run as the deployment operator; replace REPLACE_VERSION with the extracted directory version
sudo useradd --system --no-create-home --shell /usr/sbin/nologin gateway-api
sudo useradd --system --no-create-home --shell /usr/sbin/nologin gateway-monitor
sudo install -d -m 0755 /opt/stablecoin-gateway/releases
sudo cp -R stablecoin-gateway_REPLACE_VERSION_linux_amd64 /opt/stablecoin-gateway/releases/
sudo ln -s /opt/stablecoin-gateway/releases/stablecoin-gateway_REPLACE_VERSION_linux_amd64 /opt/stablecoin-gateway/current
sudo install -d -m 0700 /etc/stablecoin-gateway
sudo install -m 0600 /opt/stablecoin-gateway/current/configs/systemd/api.env.example /etc/stablecoin-gateway/api.env
sudo install -m 0600 /opt/stablecoin-gateway/current/configs/systemd/monitor.env.example /etc/stablecoin-gateway/monitor.env
sudo install -m 0644 /opt/stablecoin-gateway/current/configs/systemd/stablecoin-gateway@.service /etc/systemd/system/
# Fill both env files using a secure editor; no export or shell expansion in systemd EnvironmentFile
```

On first installation, `current` must not exist. Never overwrite a version directory or give the runtime user ownership of it. Keep configuration directories and environment files root-managed. Generate and securely back up the API encryption master key once. Configure signer keys/TLS separately; never inject signer credentials into the API.

Startup order:

1. Inject the migration role's `GATEWAY_DATABASE_URL` securely into the standalone `bin/gateway-migrate up`, then check `bin/gateway-migrate version`; dirty migrations are not acceptable. SQL migrations are embedded in the executable.
2. Start the API and monitor to validate basic connectivity; they alone do not provide a complete payment pipeline:

   ```bash
   sudo systemctl daemon-reload
   sudo systemctl enable --now stablecoin-gateway@api stablecoin-gateway@monitor
   curl --fail http://127.0.0.1:8080/healthz
   curl --fail http://127.0.0.1:9090/readyz
   sudo journalctl -u stablecoin-gateway@api -u stablecoin-gateway@monitor -n 50 --no-pager
   ```

3. Complete testnet preflight and provision reviewed asset/merchant/deposit-address/custody-wallet records as described below. Deploy the screening adapter and isolated testnet signer. Missing valid screening evidence must block processing, not default to allow. Initial asset/merchant provisioning still requires operator-reviewed database writes; there is no one-click onboarding or production-wallet installer.
4. Create a separate system account `gateway-<instance>` and `/etc/stablecoin-gateway/<instance>.env` (0600) per process using `.env.example` and the relevant sections below. Assign unique `GATEWAY_OBSERVABILITY_ADDR` ports on a shared host. Enable `indexer`, `deposit-screening-worker`, `deposit-worker`, `payout-screening-worker`, `payout-signing-worker`, `payout-execution-worker`, and `webhook-worker`, for example `systemctl enable --now stablecoin-gateway@indexer`. Payouts still require valid screening and manual approval.
5. For sweeping, also enable `sweep-screening-worker`, `sweep-planner`, `sweep-signing-worker`, and `sweep-execution-worker`. Schedule `gateway-wallet-snapshot` followed by `gateway-reconcile` with the appropriate asset settings, frequency, timeout, failure alerting, and no overlapping runs. These are one-shot commands; do not apply the auto-restart service template to them.
6. Connect Prometheus, alert delivery, and Grafana. Check worker `/readyz`, actual task progress, and snapshot freshness. API `/healthz` checks HTTP liveness only, not database readiness or payment acceptance. Expose the API through a TLS reverse proxy; restrict observability ports to monitoring networks.

For upgrades, close merchant write ingress, pause new payout creation/approval and sweep planning, gracefully stop affected processes, and record in-flight txIDs. Preserve consistent backups of the database, encryption-key versions, and signer idempotency state. Rehearse the target migration and `gateway-recovery-audit` on an isolated restored database, then migrate the stopped production deployment, switch `current`, and start in stages. Verify task takeover and reconciliation before reopening ingress. Roll back executables only after confirming schema/message backward compatibility; otherwise use approved restore/forward repair. Never automatically downgrade the database, discard signer records, or recreate in-flight transactions.

Sustained-run acceptance remains separate: pin a commit/configuration/testnet wallet and observe real testnet deposits, payouts, sweeps, scheduled snapshots/reconciliation, restarts, and alert delivery over an agreed window. Record duplicate-settlement checks, unexplained differences, backlog, and error rates. Passing a package build or health probe does not complete that acceptance.

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

# Apply migrations independently before starting the API
go run ./cmd/gateway-migrate up
go run ./cmd/gateway-api
# Check liveness from another terminal
curl http://127.0.0.1:8080/healthz
```

Capture finalized TRC20 balances for every registered active custody wallet of an asset:

```bash
export GATEWAY_WALLET_SNAPSHOT_ASSET_ID='usdt-tron-nile'
export GATEWAY_PAYOUT_TRON_SOLIDITY_NODE_URL='https://nile.trongrid.io'
export GATEWAY_TRON_API_KEY='OPTIONAL_NODE_API_KEY'

go run ./cmd/gateway-wallet-snapshot
```

The migration backfills existing deposit addresses as `deposit` custody wallets, and new deposit addresses are registered atomically. The command checks the custody-ledger and in-flight checkpoint before and after chain reads and again while saving; it fails safely if that checkpoint changes. In-flight payouts and sweeps are persisted as immutable snapshot evidence instead of blocking continuous reconciliation. The command never adjusts balances automatically.

Create sweep plans from the latest finalized snapshot:

```bash
export GATEWAY_SWEEP_ASSET_ID='usdt-tron-nile'
export GATEWAY_SWEEP_MINIMUM_AMOUNT='10000000'
export GATEWAY_SWEEP_MAX_SNAPSHOT_AGE='10m'
export GATEWAY_OBSERVABILITY_ADDR='127.0.0.1:9097'

go run ./cmd/gateway-sweep-planner
```

The planner requires exactly one active hot wallet, an indexer cursor beyond the snapshot height, and proof that every inbound event through that height was matched and allowed by inbound address screening. Total inbound events minus prior successful sweeps must exactly equal the snapshotted address balance. Missing evidence fails closed. The process only writes immutable sweep-screening evidence and `sweep_plans`; it never reads private keys, broadcasts transactions, or changes the ledger.

The planner verifies that inbound screening was valid when the deposit match was created, then creates an independent pre-sweep screening job for an otherwise eligible source. That job binds the source address, hot-wallet destination, finalized snapshot, and exact amount. A plan is created only while its current result is a valid `allow`; deny/review, missing, and expired results all fail closed.

Start the sweep-screening worker before the planner. The provider reuses the `/v1/address-screenings` contract with direction `sweep`:

```bash
export GATEWAY_SWEEP_SCREENING_WORKER_ID='sweep-screening-1'
export GATEWAY_SCREENING_PROVIDER_URL='https://screening-adapter.internal'
export GATEWAY_SCREENING_PROVIDER_NAME='production-provider'
export GATEWAY_SCREENING_PROVIDER_BEARER_TOKEN='INJECT_FROM_A_SECRET_MANAGER'

go run ./cmd/gateway-sweep-screening-worker
```

Deploy in this order: database migration, sweep-screening worker, then planner. Without the worker, the planner safely stops after creating a screening job; it never creates a plan or reaches signing.

Configure an isolated signer and start the sweep-signing worker:

```bash
export GATEWAY_TRON_NETWORK='tron-nile'
export GATEWAY_SWEEP_TRON_SOLIDITY_NODE_URL='https://nile.trongrid.io'
export GATEWAY_SWEEP_SIGNER_URL='https://signer.internal.example'
export GATEWAY_SWEEP_SIGNER_BEARER_TOKEN='INJECT_FROM_A_SECRET_MANAGER'
export GATEWAY_SWEEP_SIGNER_MAX_FEE_LIMIT='100000000'

go run ./cmd/gateway-sweep-signing-worker
```

After claiming a plan, the worker reads the source balance between two identical finalized heads. It refuses to sign if the balance is below the planned amount or the head changes. The sweep signer must implement `POST /v1/tron/sweeps:sign`, explicitly bind `source_address`, and idempotently return the same transaction for the plan UUID. The client reparses the transaction and verifies its source, contract, hot-wallet destination, amount, fee, and lifetime. Success moves the execution to `ready_for_broadcast`. The repository testnet signer supports this endpoint but only for its single configured source key; production deployments need a KMS/HSM/MPC backend that authorizes the source address and selects its key.

Start the sweep broadcast and finality worker:

```bash
export GATEWAY_SWEEP_TRON_FULL_NODE_URL='https://nile.trongrid.io'
export GATEWAY_SWEEP_TRON_SOLIDITY_NODE_URL='https://nile.trongrid.io'

go run ./cmd/gateway-sweep-execution-worker
```

The worker only broadcasts the immutable signed transaction stored in the database. A lost node response moves the execution to `confirming`; subsequent recovery queries or rebroadcasts the exact same bytes and txid, never a newly constructed transaction. Only a finalized successful result from a Solidity Node produces `succeeded`. A finalized execution failure, or a transaction that remains missing after the finalized chain time passes its expiration, produces `failed`. Sweeps move funds between custody wallets and therefore do not change merchant liability ledgers. After a successful sweep, future finalized snapshots subtract prior successful sweep amounts, allowing the same deposit address to safely sweep later deposits again.

After preparing a dedicated test database and Nile/Shasta wallets, run the one-shot acceptance command to exercise finalized snapshot capture, exact address/amount planning, isolated signing, exact-transaction broadcast, finalized confirmation, a second snapshot, and wallet-asset reconciliation:

```bash
export GATEWAY_SWEEP_ACCEPTANCE_SOURCE_ADDRESS='deposit-address-controlled-by-the-test-signer'
export GATEWAY_SWEEP_ACCEPTANCE_DESTINATION_ADDRESS='registered-unique-hot-address'
export GATEWAY_SWEEP_ACCEPTANCE_MAX_AMOUNT='1000000'
go run ./cmd/gateway-sweep-acceptance
```

The command hard-rejects mainnet and requires explicit source, destination, and maximum amount bounds. It creates no plan when the current source balance exceeds that maximum. Start the sweep-screening worker and isolated signer separately first; the command waits for screening within its bounded timeout. Output contains only plan, transaction, snapshot, and reconciliation identifiers—never keys or signed transaction bytes. Do not run it against a shared production database.

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

For targeted acceptance of one asset's wallet snapshots, scope only the wallet-asset reconciliation; ledger-integrity checks remain global:

```bash
export GATEWAY_RECONCILIATION_WALLET_ASSET_ID='usdt-tron-nile'
go run ./cmd/gateway-reconcile
```

The immutable reconciliation run records the selected asset scope. A scoped run fails safely when that asset has no finalized wallet snapshot; it never treats "checked zero items" as a successful reconciliation, and it does not refresh the global wallet-reconciliation freshness metric.

Wallet-asset reconciliation uses a conservative interval. Its lower bound is the finalized custody-wallet balance; its upper bound adds payouts whose broadcast outcome remains uncertain. Ledger custody above the upper bound is a shortfall, while custody below the lower bound is an excess. Sweeps move funds between custody wallets, so they are recorded as in-flight evidence without being counted twice. Reconciliation only detects discrepancies; it never changes ledger or business state automatically. After verification and manual remediation, resolve the case with audited operator details:

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

The indexer, inbound screener, deposit matcher, pre-sweep screener, sweep planner, sweep signer, sweep executor, payout screener, payout signer, payout executor, and webhook worker expose these endpoints on `127.0.0.1:9090` by default:

- `GET /healthz`: process liveness
- `GET /readyz`: timeout-bounded PostgreSQL readiness
- `GET /metrics`: Prometheus operation results, duration, consecutive failures, last-success time, running state, and TRON node-pool state

```bash
export GATEWAY_OBSERVABILITY_ADDR='127.0.0.1:9090'
curl http://127.0.0.1:9090/healthz
curl http://127.0.0.1:9090/readyz
curl http://127.0.0.1:9090/metrics
```

Assign a different port to each worker when several run directly on the same host. Containers or Kubernetes may reuse the container-local port. If binding beyond loopback, restrict the metrics port with network policy. The repository provides alert rules but does not deploy Prometheus or Alertmanager.

The indexer, sweep signer, payout executor, and sweep executor also expose endpoint count, current preferred endpoint, failures by bounded reason, switches, and pool-exhaustion totals. Labels contain only the operation, endpoint number from 1 through 8, and a bounded reason; node URLs and API keys are never exposed. Alerts cover complete pool exhaustion, sustained backup preference, and frequent switching within 15 minutes.

Start the independent, read-only business-risk monitor:

```bash
export GATEWAY_DATABASE_URL='postgres://gateway:password@127.0.0.1:5432/gateway?sslmode=disable'
export GATEWAY_OBSERVABILITY_ADDR='127.0.0.1:9096'
export GATEWAY_MONITOR_REFRESH_INTERVAL='15s'
go run ./cmd/gateway-monitor
```

The process periodically captures a repeatable-read PostgreSQL snapshot. Prometheus scrapes in-memory gauges and therefore does not query the database at scrape frequency. Metrics cover open reconciliation cases, payout status and oldest creation time, inbound/outbound/pre-sweep screening backlog and risk decisions, expired sweep-screening leases and results, Outbox backlog and dead letters, indexer cursor activity, and the latest reconciliation and wallet-snapshot timestamps. They are observational only and never mutate ledger or business state.

Load [`configs/prometheus/alerts.yml`](configs/prometheus/alerts.yml) into Prometheus. The process-down rule assumes the scrape job is named `stablecoin-gateway-monitor`. The payout, node-switching, cursor, reconciliation, and wallet-snapshot thresholds are safe starting points and must be tuned to chain finality, scheduling frequency, and production SLAs. Cursor staleness currently detects stopped indexer activity; exact chain-head lag still requires later node-height sampling.

Import [`configs/grafana/dashboards/stablecoin-gateway.json`](configs/grafana/dashboards/stablecoin-gateway.json) into Grafana using the Classic JSON model; it uses no third-party panel plugins. Configure Prometheus scrapes for the business monitor and workers, then import the file and select your Prometheus data source. `Business job` defaults to `stablecoin-gateway-monitor`, `Business instance` selects one instance, and `Process jobs` supports multiple selections.

The 26 panels cover sampling health, payout/sweep/Outbox counts and ages, three screening directions, cursor activity, reconciliation and wallet-snapshot freshness, worker throughput and failures, node switching, and optional signer results and latency. Business snapshots use one monitor instance to avoid duplicate counts. They are hidden when scraping fails, no successful sample exists, or the sample exceeds `Max sample age` (120 seconds by default). NO DATA is not a health signal; check scrape status and sample age first. Zero timestamps are excluded from age calculations. Cursor inactivity is not chain-head lag, and worker throughput is not payment volume. Signer panels may remain empty when no signer metrics are enabled; AWS is not required.

For automatic loading, mount [`configs/grafana/provisioning/dashboards/stablecoin-gateway.yml`](configs/grafana/provisioning/dashboards/stablecoin-gateway.yml) under Grafana's `/etc/grafana/provisioning/dashboards/`, and mount `configs/grafana/dashboards/` at `/var/lib/grafana/dashboards/stablecoin-gateway/`. Operators configure the Prometheus data source separately. Provisioning updates dashboard UID `stablecoin-gateway-ops`; avoid collisions with existing dashboards using that UID.

Local checks and CI use the same command, requiring `jq` and Docker:

```bash
bash scripts/check-grafana-queries.sh
```

It extracts all 28 dashboard queries for validation by a pinned `promtool`, then verifies stale, unreachable, unsampled, and zero-timestamp behavior. A Go contract test also checks that every referenced project metric is exported by the actual collectors.

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

Inbound provider requests use `direction=inbound` with `deposit_screening_id`; outbound requests use `direction=outbound` with `payout_id`; sweep requests use `direction=sweep` with `sweep_screening_id`. Inbound and sweep requests also include `source_address`. All three include destination, network, asset, contract, and smallest-unit amount, and require provider-side idempotency through `Idempotency-Key`.

List sweep-screening tasks that have not produced a plan and require operator attention:

```bash
go run ./cmd/gateway-admin list-sweep-screenings --limit 100
```

The returned `state` is `pending`, `processing`, `lease_expired`, `result_expired`, or `blocked`. This read-only command provides no manual allow bypass; deny/review findings remain subject to the deployment's compliance process and cannot bypass the current-allow planning gate.

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
install -d -m 700 /secure/path
go run ./cmd/gateway-testnet-wallet --private-key-file /secure/path/nile-wallet.key
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

Use `gateway-kms-signer` for a production hot wallet. Its AWS KMS asymmetric key must use `ECC_SECG_P256K1` with `SIGN_VERIFY`, keeping the private key non-exportable. The signer sends only the 32-byte transaction digest to KMS with `ECDSA_SHA_256` and `DIGEST`. It converts the returned DER signature to TRON `r || s || v`, normalizes low-S, and recovers the public key to bind the signature to the configured owner address. It persists the result in PostgreSQL before responding, so one request UUID creates only one result across replicas.

Apply migrations first, then start the service through the AWS default credential chain, preferably with workload identity:

```bash
export GATEWAY_KMS_SIGNER_DATABASE_URL='postgres://signer_runtime:password@db/signer?sslmode=verify-full'
export GATEWAY_KMS_SIGNER_TLS_CERT_FILE='/run/secrets/signer-tls.crt'
export GATEWAY_KMS_SIGNER_TLS_KEY_FILE='/run/secrets/signer-tls.key'
export GATEWAY_KMS_SIGNER_BEARER_TOKEN='high-entropy-token-from-a-secret-manager'
export GATEWAY_KMS_SIGNER_NETWORK='tron-mainnet'
export GATEWAY_KMS_SIGNER_ALLOW_MAINNET='true'
export GATEWAY_KMS_SIGNER_FULL_NODE_URL='https://primary-full-node.example,https://backup-full-node.example'
export GATEWAY_KMS_SIGNER_OWNER_ADDRESS='TRON-hot-wallet-address-derived-from-the-KMS-public-key'
export GATEWAY_KMS_SIGNER_CONTRACTS='allowlisted-TRC20-contract-addresses'
export GATEWAY_KMS_SIGNER_MAX_AMOUNT='maximum-atomic-units-per-transfer'
export GATEWAY_KMS_SIGNER_AWS_REGION='ap-southeast-1'
export GATEWAY_KMS_SIGNER_AWS_KEY_ID='alias/stablecoin-gateway-tron'

go run ./cmd/gateway-kms-signer
```

The KMS signer uses `GATEWAY_OBSERVABILITY_ADDR` (default `127.0.0.1:9090`) for separate `/healthz`, `/readyz`, and `/metrics` endpoints. Readiness checks PostgreSQL plus the KMS public key's usage, algorithm, and address binding within a bounded timeout. It does not sign or prove `kms:Sign` permission. Each probe calls `GetPublicKey`, so operators should control its frequency. Name the Prometheus scrape job `stablecoin-gateway-kms-signer` to use the included signer availability, failure/timeout, and authorization alerts.

`stablecoin_gateway_signer_requests_total{operation,result}` and `stablecoin_gateway_signer_request_duration_seconds{operation}` cover HTTP processing, including authorization, idempotency lookup, construction, signing, and persistence. Logs contain only bounded result categories, authenticated request UUIDs, successful transaction IDs, and durations; operators must collect and retain them. Immutable successful results remain in `signer_requests`; failed-attempt logs are not a separate durable audit table. `GATEWAY_KMS_SIGNER_REQUEST_TIMEOUT` defaults to `25s` and is capped at `1m`. A timeout returns 503 and cancels downstream contexts; callers must reuse the same request UUID when retrying.

For deployments choosing AWS KMS, in addition to `GATEWAY_KMS_SIGNER_ALLOW_MAINNET=true`, a mainnet deployment should grant IAM only `kms:GetPublicKey` and `kms:Sign` on the selected key, limit the runtime database role to the required `signer_requests` access, and isolate the signer network with mTLS or workload identity. One process binds one KMS key and source address, which is suitable for hot-wallet payouts. Sweeping a large deposit-address set requires a later HSM/MPC backend or an audited multi-key router.

Validate a real KMS key in the target deployment using `GATEWAY_KMS_SIGNER_AWS_REGION`, `GATEWAY_KMS_SIGNER_AWS_KEY_ID`, and the independently registered `GATEWAY_KMS_SIGNER_OWNER_ADDRESS`. Provide workload identity through the AWS default credential chain:

```bash
# Read and check the public key and expected address without calling Sign
go run ./cmd/gateway-kms-acceptance

# Explicitly sign a random challenge to check Sign permission and TRON conversion
go run ./cmd/gateway-kms-acceptance --sign --timeout 30s
```

Use `--region`, `--key-id`, or `--expected-address` to override configuration; the total deadline is capped at `1m`. The `--sign` digest comes from a fixed acceptance domain and system randomness. The command accepts no arbitrary digest or transaction input and constructs or broadcasts no payment. Output contains only the mode, an address SHA-256 fingerprint, binding/verification results, and a UTC timestamp. Raw cloud errors, ARNs, addresses, signatures, and credentials are not printed. `signature_verified=false` in the default public-key mode means signing was not checked; only `signature_verified=true` in signature mode confirms that the actual signing round trip succeeded.

The command needs no database, TLS service, or wallet funds. Passing it does not validate HTTP authorization, idempotent recovery, or on-chain payouts. Repository tests use a local AWS protocol simulator. Only deployments choosing AWS KMS need to configure the target key and identity, run live cloud acceptance, and retain the report; this is not a v0.1 release gate.

Configure the FullNode and SolidityNode endpoints, then start the payout execution worker:

```bash
export GATEWAY_TRON_NETWORK='tron-nile'
export GATEWAY_PAYOUT_TRON_FULL_NODE_URL='https://primary-full-node.example,https://backup-full-node.example'
export GATEWAY_PAYOUT_TRON_SOLIDITY_NODE_URL='https://primary-solidity-node.example,https://backup-solidity-node.example'
export GATEWAY_TRON_API_KEY='injected-by-your-secrets-manager'
go run ./cmd/gateway-payout-execution-worker
```

Every TRON node URL environment variable accepts either one URL or up to eight comma-separated endpoints in priority order. Connection errors, response-read failures, oversized responses, invalid JSON, HTTP 408/425/429, and 5xx responses fail over to the next endpoint; the last successful endpoint remains preferred. Deterministic 4xx responses and node-level business rejection do not trigger blind failover. Broadcast failover submits only the same immutable signed payload stored in PostgreSQL, so the txID cannot change. All endpoints must serve the same network and be trusted by the deployer because the same node API key is sent to each endpoint that is actually contacted.

This configuration provides the testnet signing, broadcast, and confirmation path. On 2026-09-27, a fresh Nile-only wallet completed live test-asset checks: [`e7f6ee9…aef39`](https://nile.tronscan.org/#/transaction/e7f6ee9ba5f10f119454d8df0a33b633ca6915a31f2847b82a1d5d25859aef39) covered signer construction, signing, broadcast, and SolidityNode finalization; [`fa242e1b…cc82b`](https://nile.tronscan.org/#/transaction/fa242e1bdc988f926582b169944ddf6eeb64cac55c7e83e79d38e371385cc82b) covered Indexer ingestion of a real Transfer, Deposit Worker intent matching, double-entry posting, and creation of the `deposit.confirmed` Outbox event; and [`86283b5f…e9ac`](https://nile.tronscan.org/#/transaction/86283b5fb90fc9aaf01162aff936df9c2c2b5ad7a89856ee972d691e87f0e9ac) covered merchant API payout creation, atomic freezing, manual approval, a private-CA signer, broadcast, finalization, and ledger settlement. On 2026-10-12, [`6fd465a…073da648`](https://nile.tronscan.org/#/transaction/6fd465a762b3d48bd01f5a4c18e6b83322bece9b9045d5072d481aef073da648) completed a live 1,000 USDT sweep from a dedicated deposit address to the hot wallet, with finalized before-and-after snapshots matching the custody ledger. A later external `0.000001 USDT` dust transfer was identified by scoped reconciliation as an on-chain excess case and was not credited to any merchant ledger. The same test cycle also verified automatic failure and atomic fund release when a transaction safely expired without reaching the chain. This is functional testnet evidence rather than production acceptance; production deployments still need independent node failover and monitoring.

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

- Nile now covers live deposit posting, a merchant-API-driven payout loop, a dedicated-address-to-hot-wallet sweep, safe-expiry recovery, and before-and-after custody reconciliation.
- Automated recovery exercises cover payout and sweep worker crashes during broadcast and confirmation. A stale lease cannot advance state, the replacement worker reuses only the original signed transaction and txID, payout settlement and its terminal event happen once, and a sweep never changes merchant ledgers.
- On 2026-10-13, a live read-only Nile preflight succeeded with a deliberately unreachable preferred endpoint and Nile as the backup. Local fault injection additionally covers same-txID broadcast failover after a primary-node 5xx and no failover for deterministic 4xx responses or business rejection.
- The explicit node-churn soak test repeatedly moves availability across three local simulated nodes while concurrently verifying finalized reads, immutable same-transaction broadcasts, sticky endpoint switching, and zero pool exhaustion. It does not access the internet, a database, or a wallet.
- Database disaster recovery now has a real `pg_dump`, isolated `pg_restore`, core-table row-count comparison, and read-only fund-integrity audit. A restored database passes only at the current clean migration version and with no critical fund finding.
- Mainnet canarying starts only after sustained testnet operation, three-way reconciliation, monitoring and alerting, disaster-recovery exercises, and an external security audit pass.
- Mainnet is never a general test environment. Every mainnet canary requires a defined loss limit, dual approval, and an emergency stop.

Run the fund-execution recovery exercises against an isolated PostgreSQL database that has the latest migrations:

```bash
export GATEWAY_TEST_DATABASE_URL='postgres://gateway:password@127.0.0.1:5432/gateway_recovery_test?sslmode=disable'

go test -tags=integration ./internal/payout \
  -run '^TestStoreExecutionLeaseTakeoverRecoversOriginalTransaction$' -count=1
go test -tags=integration ./internal/sweep \
  -run '^TestStoreExecutionLeaseTakeoverRecoversOriginalTransaction$' -count=1
```

These exercises create database-only test assets and simulated signed transactions. They do not access a node, broadcast, or read wallet keys.

Run the local node-churn soak test, which is excluded from default test runs:

```bash
go test -tags=soak -race ./internal/chain/tron/nodehttp \
  -run '^TestFailoverSoakUnderEndpointChurn$' -count=1
```

The test concurrently accesses three in-process HTTP node simulators, repeatedly moves the only available endpoint, and verifies that every broadcast reuses the identical payload and txID.

Restore a backup into an isolated PostgreSQL instance before starting any API or worker. The backup must come from a consistent snapshot, with the recovery point, backup checksum, PostgreSQL version, and encryption-key versions retained as audit evidence. After restoration and core-table row-count comparison, run:

```bash
export GATEWAY_DATABASE_URL='postgres://gateway:password@127.0.0.1:5432/gateway_restored?sslmode=disable'
go run ./cmd/gateway-recovery-audit > recovery-audit.json
```

The command runs in one PostgreSQL repeatable-read, read-only transaction and creates no reconciliation run or business-state mutation. It exits non-zero for a mismatched or dirty migration version, ledger/business-reference corruption, custody shortfall, missing wallet checkpoint, or open critical reconciliation case. On-chain excess, open warning cases, and reclaimable expired leases remain warnings. Passing the audit is not permission to shift traffic by itself: production recovery must also validate the RPO/RTO, the chain-rescan range after the recovery point, node failover, and signer and encryption-key availability.

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
- The repository's file-key signer is testnet-only; production signing uses the independent remote-signer interface with the deployer's chosen backend. AWS KMS is optional, and multi-address sweeps still require an audited key router.
- Mainnet capabilities must be explicitly enabled and pass a release checklist.
- Deployers are responsible for address screening, KYC, AML, and licensing obligations in their jurisdictions.
- Do not process real funds before completing a security audit, disaster-recovery exercises, and reconciliation acceptance tests.

## License

Apache-2.0. See [LICENSE](LICENSE).
