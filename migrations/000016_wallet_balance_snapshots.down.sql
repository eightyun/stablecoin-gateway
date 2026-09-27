DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM wallet_balance_snapshot_runs)
        OR EXISTS (SELECT 1 FROM custody_wallets WHERE role <> 'deposit')
    THEN
        RAISE EXCEPTION 'cannot roll back wallet snapshot schema while wallet data exists';
    END IF;
END;
$$;

DROP TRIGGER wallet_balance_snapshots_immutable_guard ON wallet_balance_snapshots;
DROP TRIGGER wallet_balance_snapshot_runs_immutable_guard ON wallet_balance_snapshot_runs;
DROP FUNCTION protect_wallet_balance_snapshot();
DROP TRIGGER custody_wallets_guard ON custody_wallets;
DROP FUNCTION protect_custody_wallet();
DROP TABLE wallet_balance_snapshots;
DROP TABLE wallet_balance_snapshot_runs;
ALTER TABLE deposit_addresses DROP CONSTRAINT deposit_addresses_custody_wallet_fk;
DROP TRIGGER deposit_addresses_register_custody_wallet ON deposit_addresses;
DROP FUNCTION register_deposit_custody_wallet();
DROP TABLE custody_wallets;
