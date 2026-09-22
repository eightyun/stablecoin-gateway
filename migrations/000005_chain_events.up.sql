ALTER TABLE chain_scan_cursors
    ADD COLUMN tracked_contract TEXT CHECK (tracked_contract <> '');

CREATE TABLE chain_events (
    network TEXT NOT NULL REFERENCES chain_scan_cursors (network),
    contract TEXT NOT NULL CHECK (contract <> ''),
    transaction_id TEXT NOT NULL CHECK (transaction_id <> ''),
    log_index BIGINT NOT NULL CHECK (log_index BETWEEN 0 AND 4294967295),
    block_height BIGINT NOT NULL CHECK (block_height > 0),
    block_hash TEXT NOT NULL CHECK (block_hash <> ''),
    from_address TEXT NOT NULL CHECK (from_address <> ''),
    to_address TEXT NOT NULL CHECK (to_address <> ''),
    amount NUMERIC(78, 0) NOT NULL CHECK (
        amount > 0
        AND amount < 115792089237316195423570985008687907853269984665640564039457584007913129639936
    ),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (network, contract, transaction_id, log_index)
);

CREATE INDEX chain_events_block_idx ON chain_events (network, block_height);

CREATE FUNCTION protect_chain_event()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'chain events are immutable';
END;
$$;

CREATE TRIGGER chain_events_immutable_guard
BEFORE UPDATE OR DELETE ON chain_events
FOR EACH ROW
EXECUTE FUNCTION protect_chain_event();
