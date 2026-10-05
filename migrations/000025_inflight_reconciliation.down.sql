DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM wallet_balance_snapshot_runs
        WHERE payout_in_flight_count > 0 OR sweep_in_flight_count > 0
    ) THEN
        RAISE EXCEPTION 'export in-flight reconciliation evidence before migration 25 rollback';
    END IF;
END;
$$;

DROP INDEX sweep_executions_in_flight_idx;
DROP INDEX payouts_asset_in_flight_idx;

ALTER TABLE wallet_balance_snapshot_runs
    DROP CONSTRAINT wallet_snapshot_sweep_in_flight_check,
    DROP CONSTRAINT wallet_snapshot_payout_in_flight_check,
    DROP COLUMN sweep_in_flight_amount,
    DROP COLUMN sweep_in_flight_count,
    DROP COLUMN payout_in_flight_amount,
    DROP COLUMN payout_in_flight_count;
