DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM reconciliation_runs WHERE asset_id IS NOT NULL) THEN
        RAISE EXCEPTION 'cannot roll back reconciliation run scope while scoped runs exist';
    END IF;
END;
$$;

DROP INDEX reconciliation_runs_kind_asset_snapshot_idx;

ALTER TABLE reconciliation_runs
    DROP CONSTRAINT reconciliation_runs_asset_scope_check,
    DROP COLUMN asset_id;
