CREATE TABLE chain_scan_cursors (
    network TEXT PRIMARY KEY CHECK (network <> ''),
    start_height BIGINT NOT NULL CHECK (start_height > 0),
    anchor_hash TEXT NOT NULL CHECK (anchor_hash <> ''),
    next_height BIGINT NOT NULL CHECK (next_height >= start_height),
    previous_hash TEXT NOT NULL CHECK (previous_hash <> ''),
    lease_owner TEXT,
    lease_until TIMESTAMPTZ,
    lease_epoch BIGINT NOT NULL DEFAULT 0 CHECK (lease_epoch >= 0),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT chain_scan_cursors_lease_check CHECK (
        (lease_owner IS NULL AND lease_until IS NULL)
        OR
        (lease_owner IS NOT NULL AND lease_owner <> '' AND lease_until IS NOT NULL)
    )
);
