DROP TRIGGER IF EXISTS sweep_plans_immutable_guard ON sweep_plans;
DROP FUNCTION IF EXISTS protect_sweep_plan();
DROP TABLE IF EXISTS sweep_plans;

ALTER TABLE wallet_balance_snapshots
    DROP CONSTRAINT IF EXISTS wallet_balance_snapshots_sweep_amount_unique;

ALTER TABLE wallet_balance_snapshot_runs
    DROP CONSTRAINT IF EXISTS wallet_balance_snapshot_runs_sweep_identity_unique;
