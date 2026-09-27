ALTER TABLE wallet_balance_snapshot_runs
    ADD COLUMN ledger_account_id UUID,
    ADD COLUMN ledger_entry_count BIGINT,
    ADD COLUMN ledger_balance NUMERIC(78, 0),
    ADD CONSTRAINT wallet_balance_snapshot_runs_ledger_account_fk
        FOREIGN KEY (ledger_account_id, asset_id)
        REFERENCES ledger_accounts (id, asset_id),
    ADD CONSTRAINT wallet_balance_snapshot_runs_ledger_checkpoint_check CHECK (
        (
            ledger_account_id IS NULL
            AND ledger_entry_count IS NULL
            AND ledger_balance IS NULL
        )
        OR
        (
            ledger_account_id IS NOT NULL
            AND ledger_entry_count IS NOT NULL
            AND ledger_balance IS NOT NULL
            AND ledger_entry_count >= 0
            AND ledger_balance >= 0
        )
    );

ALTER TABLE reconciliation_runs
    DROP CONSTRAINT reconciliation_runs_kind_check,
    ADD CONSTRAINT reconciliation_runs_kind_check
        CHECK (kind IN ('ledger_integrity', 'wallet_assets'));
