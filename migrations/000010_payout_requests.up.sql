CREATE TABLE payouts (
    id UUID PRIMARY KEY,
    merchant_id UUID NOT NULL REFERENCES merchants (id),
    asset_id TEXT NOT NULL REFERENCES assets (id),
    idempotency_key TEXT NOT NULL CHECK (char_length(idempotency_key) BETWEEN 1 AND 128),
    merchant_reference TEXT NOT NULL CHECK (char_length(merchant_reference) BETWEEN 1 AND 128),
    request_hash CHAR(64) NOT NULL,
    destination_address TEXT NOT NULL CHECK (char_length(destination_address) BETWEEN 1 AND 128),
    amount NUMERIC(78, 0) NOT NULL CHECK (amount > 0 AND amount <= 9223372036854775807),
    status TEXT NOT NULL CHECK (status IN ('pending_review')),
    freeze_transaction_id UUID NOT NULL UNIQUE REFERENCES journal_transactions (id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (merchant_id, idempotency_key),
    UNIQUE (merchant_id, merchant_reference)
);

CREATE INDEX payouts_status_created_idx
    ON payouts (status, created_at, id);
