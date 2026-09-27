DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM wallet_balance_snapshot_runs WHERE ledger_account_id IS NOT NULL
    ) OR EXISTS (
        SELECT 1 FROM reconciliation_runs WHERE kind = 'wallet_assets'
    ) THEN
        RAISE EXCEPTION 'cannot roll back wallet ledger reconciliation while checkpoint data exists';
    END IF;
END;
$$;

ALTER TABLE reconciliation_runs
    DROP CONSTRAINT reconciliation_runs_kind_check,
    ADD CONSTRAINT reconciliation_runs_kind_check
        CHECK (kind IN ('ledger_integrity'));

ALTER TABLE wallet_balance_snapshot_runs
    DROP CONSTRAINT wallet_balance_snapshot_runs_ledger_checkpoint_check,
    DROP CONSTRAINT wallet_balance_snapshot_runs_ledger_account_fk,
    DROP COLUMN ledger_balance,
    DROP COLUMN ledger_entry_count,
    DROP COLUMN ledger_account_id;
