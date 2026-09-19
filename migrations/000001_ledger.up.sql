CREATE TABLE assets (
    id TEXT PRIMARY KEY,
    network TEXT NOT NULL,
    contract_address TEXT NOT NULL,
    symbol TEXT NOT NULL,
    decimals SMALLINT NOT NULL CHECK (decimals BETWEEN 0 AND 36),
    status TEXT NOT NULL CHECK (status IN ('active', 'disabled')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (network, contract_address)
);

CREATE TABLE merchants (
    id UUID PRIMARY KEY,
    name TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending', 'active', 'suspended', 'closed')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE ledger_accounts (
    id UUID PRIMARY KEY,
    owner_type TEXT NOT NULL CHECK (owner_type IN ('platform', 'merchant')),
    owner_id TEXT NOT NULL,
    asset_id TEXT NOT NULL REFERENCES assets (id),
    code TEXT NOT NULL,
    normal_side CHAR(1) NOT NULL CHECK (normal_side IN ('D', 'C')),
    status TEXT NOT NULL CHECK (status IN ('active', 'locked', 'closed')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (owner_type, owner_id, asset_id, code),
    UNIQUE (id, asset_id)
);

CREATE TABLE journal_transactions (
    id UUID PRIMARY KEY,
    requester_type TEXT NOT NULL,
    requester_id TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    reference_type TEXT NOT NULL,
    reference_id TEXT NOT NULL,
    request_hash CHAR(64) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (requester_type, requester_id, idempotency_key),
    UNIQUE (requester_type, requester_id, reference_type, reference_id)
);

CREATE TABLE journal_entries (
    transaction_id UUID NOT NULL REFERENCES journal_transactions (id),
    line_no SMALLINT NOT NULL CHECK (line_no > 0),
    account_id UUID NOT NULL,
    asset_id TEXT NOT NULL REFERENCES assets (id),
    side CHAR(1) NOT NULL CHECK (side IN ('D', 'C')),
    amount NUMERIC(78, 0) NOT NULL CHECK (amount > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (transaction_id, line_no),
    FOREIGN KEY (account_id, asset_id) REFERENCES ledger_accounts (id, asset_id)
);

CREATE INDEX journal_entries_account_created_idx
    ON journal_entries (account_id, created_at, transaction_id);

CREATE TABLE outbox_events (
    id UUID PRIMARY KEY,
    topic TEXT NOT NULL,
    aggregate_type TEXT NOT NULL,
    aggregate_id TEXT NOT NULL,
    payload JSONB NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending', 'processing', 'succeeded', 'dead')),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    available_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    lease_until TIMESTAMPTZ,
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX outbox_events_dispatch_idx
    ON outbox_events (status, available_at)
    WHERE status IN ('pending', 'processing');
