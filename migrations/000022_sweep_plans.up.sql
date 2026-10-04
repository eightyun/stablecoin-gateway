ALTER TABLE wallet_balance_snapshot_runs
    ADD CONSTRAINT wallet_balance_snapshot_runs_sweep_identity_unique
    UNIQUE (id, asset_id, block_height);

ALTER TABLE wallet_balance_snapshots
    ADD CONSTRAINT wallet_balance_snapshots_sweep_amount_unique
    UNIQUE (run_id, wallet_id, asset_id, balance);

CREATE TABLE sweep_plans (
    id UUID PRIMARY KEY,
    asset_id TEXT NOT NULL REFERENCES assets (id),
    source_wallet_id UUID NOT NULL,
    source_address TEXT NOT NULL CHECK (source_address <> ''),
    destination_wallet_id UUID NOT NULL,
    destination_address TEXT NOT NULL CHECK (destination_address <> ''),
    snapshot_run_id UUID NOT NULL,
    snapshot_block_height BIGINT NOT NULL CHECK (snapshot_block_height > 0),
    amount NUMERIC(78, 0) NOT NULL CHECK (amount > 0),
    minimum_amount NUMERIC(78, 0) NOT NULL CHECK (minimum_amount > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (source_wallet_id, snapshot_run_id),
    FOREIGN KEY (source_wallet_id, asset_id, source_address)
        REFERENCES custody_wallets (id, asset_id, address),
    FOREIGN KEY (destination_wallet_id, asset_id, destination_address)
        REFERENCES custody_wallets (id, asset_id, address),
    FOREIGN KEY (snapshot_run_id, asset_id, snapshot_block_height)
        REFERENCES wallet_balance_snapshot_runs (id, asset_id, block_height),
    FOREIGN KEY (snapshot_run_id, source_wallet_id, asset_id, amount)
        REFERENCES wallet_balance_snapshots (run_id, wallet_id, asset_id, balance),
    CONSTRAINT sweep_plans_distinct_wallets_check CHECK (source_wallet_id <> destination_wallet_id),
    CONSTRAINT sweep_plans_threshold_check CHECK (amount >= minimum_amount)
);

CREATE INDEX sweep_plans_source_created_idx
    ON sweep_plans (source_wallet_id, created_at DESC);

CREATE INDEX sweep_plans_asset_created_idx
    ON sweep_plans (asset_id, created_at DESC);

CREATE FUNCTION protect_sweep_plan()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'sweep plans are immutable';
END;
$$;

CREATE TRIGGER sweep_plans_immutable_guard
BEFORE UPDATE OR DELETE ON sweep_plans
FOR EACH ROW
EXECUTE FUNCTION protect_sweep_plan();
