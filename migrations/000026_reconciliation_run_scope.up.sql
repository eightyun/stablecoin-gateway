ALTER TABLE reconciliation_runs
    ADD COLUMN asset_id TEXT REFERENCES assets (id),
    ADD CONSTRAINT reconciliation_runs_asset_scope_check CHECK (
        kind = 'wallet_assets' OR asset_id IS NULL
    );

CREATE INDEX reconciliation_runs_kind_asset_snapshot_idx
    ON reconciliation_runs (kind, asset_id, snapshot_at DESC);
