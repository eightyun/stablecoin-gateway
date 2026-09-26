ALTER TABLE chain_events
    ADD COLUMN block_time TIMESTAMPTZ;

ALTER TABLE chain_events
    ADD CONSTRAINT chain_events_block_time_required
    CHECK (block_time IS NOT NULL) NOT VALID;

CREATE INDEX chain_events_deposit_match_idx
    ON chain_events (network, contract, to_address, block_height, transaction_id, log_index);

CREATE TABLE deposit_addresses (
    id UUID PRIMARY KEY,
    merchant_id UUID NOT NULL REFERENCES merchants (id),
    asset_id TEXT NOT NULL REFERENCES assets (id),
    address TEXT NOT NULL CHECK (address <> ''),
    status TEXT NOT NULL CHECK (status IN ('active', 'retired')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    retired_at TIMESTAMPTZ,
    UNIQUE (asset_id, address),
    UNIQUE (id, merchant_id, asset_id),
    CONSTRAINT deposit_addresses_lifecycle_check CHECK (
        (status = 'active' AND retired_at IS NULL)
        OR (status = 'retired' AND retired_at IS NOT NULL)
    )
);

CREATE TABLE deposit_intents (
    id UUID PRIMARY KEY,
    merchant_id UUID NOT NULL REFERENCES merchants (id),
    asset_id TEXT NOT NULL REFERENCES assets (id),
    deposit_address_id UUID NOT NULL,
    idempotency_key TEXT NOT NULL CHECK (idempotency_key <> ''),
    merchant_reference TEXT NOT NULL CHECK (merchant_reference <> ''),
    request_hash CHAR(64) NOT NULL,
    expected_amount NUMERIC(78, 0) NOT NULL CHECK (expected_amount > 0),
    received_amount NUMERIC NOT NULL DEFAULT 0 CHECK (received_amount >= 0),
    status TEXT NOT NULL DEFAULT 'pending' CHECK (
        status IN ('pending', 'partially_paid', 'paid', 'overpaid', 'expired', 'underpaid')
    ),
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (merchant_id, idempotency_key),
    UNIQUE (merchant_id, merchant_reference),
    UNIQUE (deposit_address_id),
    FOREIGN KEY (deposit_address_id, merchant_id, asset_id)
        REFERENCES deposit_addresses (id, merchant_id, asset_id),
    CONSTRAINT deposit_intents_amount_status_check CHECK (
        (status IN ('pending', 'expired') AND received_amount = 0)
        OR (status IN ('partially_paid', 'underpaid') AND received_amount > 0 AND received_amount < expected_amount)
        OR (status = 'paid' AND received_amount = expected_amount)
        OR (status = 'overpaid' AND received_amount > expected_amount)
    )
);

CREATE INDEX deposit_intents_expiry_idx
    ON deposit_intents (expires_at, id)
    WHERE status IN ('pending', 'partially_paid');

CREATE TABLE deposit_event_matches (
    network TEXT NOT NULL,
    contract TEXT NOT NULL,
    transaction_id TEXT NOT NULL,
    log_index BIGINT NOT NULL,
    deposit_address_id UUID NOT NULL REFERENCES deposit_addresses (id),
    deposit_intent_id UUID REFERENCES deposit_intents (id),
    status TEXT NOT NULL CHECK (status IN ('matched', 'review')),
    reason TEXT CHECK (reason IN (
        'no_intent', 'before_intent', 'after_expiry', 'missing_block_time',
        'asset_disabled', 'merchant_inactive', 'address_retired'
    )),
    amount NUMERIC(78, 0) NOT NULL CHECK (amount > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (network, contract, transaction_id, log_index),
    FOREIGN KEY (network, contract, transaction_id, log_index)
        REFERENCES chain_events (network, contract, transaction_id, log_index),
    CONSTRAINT deposit_event_matches_result_check CHECK (
        (status = 'matched' AND deposit_intent_id IS NOT NULL AND reason IS NULL)
        OR (status = 'review' AND reason IS NOT NULL)
    )
);

CREATE INDEX deposit_event_matches_intent_idx
    ON deposit_event_matches (deposit_intent_id, created_at)
    WHERE deposit_intent_id IS NOT NULL;

CREATE FUNCTION protect_deposit_event_match()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'deposit event matches are immutable';
END;
$$;

CREATE TRIGGER deposit_event_matches_immutable_guard
BEFORE UPDATE OR DELETE ON deposit_event_matches
FOR EACH ROW
EXECUTE FUNCTION protect_deposit_event_match();
