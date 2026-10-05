ALTER TABLE wallet_balance_snapshot_runs
    ADD COLUMN payout_in_flight_count INTEGER NOT NULL DEFAULT 0
        CHECK (payout_in_flight_count >= 0),
    ADD COLUMN payout_in_flight_amount NUMERIC(78, 0) NOT NULL DEFAULT 0
        CHECK (payout_in_flight_amount >= 0),
    ADD COLUMN sweep_in_flight_count INTEGER NOT NULL DEFAULT 0
        CHECK (sweep_in_flight_count >= 0),
    ADD COLUMN sweep_in_flight_amount NUMERIC(78, 0) NOT NULL DEFAULT 0
        CHECK (sweep_in_flight_amount >= 0),
    ADD CONSTRAINT wallet_snapshot_payout_in_flight_check CHECK (
        (payout_in_flight_count = 0 AND payout_in_flight_amount = 0)
        OR (payout_in_flight_count > 0 AND payout_in_flight_amount > 0)
    ),
    ADD CONSTRAINT wallet_snapshot_sweep_in_flight_check CHECK (
        (sweep_in_flight_count = 0 AND sweep_in_flight_amount = 0)
        OR (sweep_in_flight_count > 0 AND sweep_in_flight_amount > 0)
    );

CREATE INDEX payouts_asset_in_flight_idx
    ON payouts (asset_id, status)
    INCLUDE (amount, broadcast_attempts)
    WHERE status IN ('ready_for_broadcast', 'confirming');

CREATE INDEX sweep_executions_in_flight_idx
    ON sweep_executions (status, plan_id)
    INCLUDE (broadcast_attempts)
    WHERE status IN ('ready_for_broadcast', 'confirming');
