CREATE TABLE custody_wallets (
    id UUID PRIMARY KEY,
    asset_id TEXT NOT NULL REFERENCES assets (id),
    address TEXT NOT NULL CHECK (address <> ''),
    role TEXT NOT NULL CHECK (role IN ('deposit', 'hot', 'cold', 'fee')),
    status TEXT NOT NULL CHECK (status IN ('active', 'retired')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    retired_at TIMESTAMPTZ,
    UNIQUE (asset_id, address),
    UNIQUE (id, asset_id, address),
    UNIQUE (id, asset_id),
    CONSTRAINT custody_wallets_lifecycle_check CHECK (
        (status = 'active' AND retired_at IS NULL)
        OR (status = 'retired' AND retired_at IS NOT NULL)
    )
);

INSERT INTO custody_wallets (id, asset_id, address, role, status, created_at, retired_at)
SELECT id, asset_id, address, 'deposit', 'active', created_at, NULL
FROM deposit_addresses;

CREATE FUNCTION register_deposit_custody_wallet()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    INSERT INTO custody_wallets (id, asset_id, address, role, status, created_at)
    VALUES (NEW.id, NEW.asset_id, NEW.address, 'deposit', 'active', NEW.created_at)
    ON CONFLICT DO NOTHING;
    RETURN NEW;
END;
$$;

CREATE TRIGGER deposit_addresses_register_custody_wallet
BEFORE INSERT ON deposit_addresses
FOR EACH ROW
EXECUTE FUNCTION register_deposit_custody_wallet();

ALTER TABLE deposit_addresses
    ADD CONSTRAINT deposit_addresses_custody_wallet_fk
    FOREIGN KEY (id, asset_id, address)
    REFERENCES custody_wallets (id, asset_id, address);

CREATE TABLE wallet_balance_snapshot_runs (
    id UUID PRIMARY KEY,
    asset_id TEXT NOT NULL REFERENCES assets (id),
    block_height BIGINT NOT NULL CHECK (block_height >= 0),
    block_hash CHAR(64) NOT NULL CHECK (block_hash ~ '^[0-9a-f]{64}$'),
    block_time TIMESTAMPTZ NOT NULL,
    wallet_count INTEGER NOT NULL CHECK (wallet_count > 0),
    total_balance NUMERIC(78, 0) NOT NULL CHECK (total_balance >= 0),
    captured_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (id, asset_id)
);

CREATE INDEX wallet_balance_snapshot_runs_asset_height_idx
    ON wallet_balance_snapshot_runs (asset_id, block_height DESC, captured_at DESC);

CREATE TABLE wallet_balance_snapshots (
    run_id UUID NOT NULL,
    wallet_id UUID NOT NULL,
    asset_id TEXT NOT NULL,
    balance NUMERIC(78, 0) NOT NULL CHECK (balance >= 0),
    PRIMARY KEY (run_id, wallet_id),
    FOREIGN KEY (run_id, asset_id)
        REFERENCES wallet_balance_snapshot_runs (id, asset_id),
    FOREIGN KEY (wallet_id, asset_id)
        REFERENCES custody_wallets (id, asset_id)
);

CREATE INDEX wallet_balance_snapshots_wallet_idx
    ON wallet_balance_snapshots (wallet_id, run_id);

CREATE FUNCTION protect_custody_wallet()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'custody wallets are immutable';
    END IF;

    IF OLD.id IS DISTINCT FROM NEW.id
        OR OLD.asset_id IS DISTINCT FROM NEW.asset_id
        OR OLD.address IS DISTINCT FROM NEW.address
        OR OLD.role IS DISTINCT FROM NEW.role
        OR OLD.created_at IS DISTINCT FROM NEW.created_at
    THEN
        RAISE EXCEPTION 'custody wallet identity is immutable';
    END IF;

    IF OLD.status = 'active'
        AND NEW.status = 'retired'
        AND OLD.retired_at IS NULL
        AND NEW.retired_at IS NOT NULL
    THEN
        RETURN NEW;
    END IF;

    IF OLD.status = NEW.status AND OLD.retired_at IS NOT DISTINCT FROM NEW.retired_at THEN
        RETURN NEW;
    END IF;

    RAISE EXCEPTION 'invalid custody wallet transition: % -> %', OLD.status, NEW.status;
END;
$$;

CREATE TRIGGER custody_wallets_guard
BEFORE UPDATE OR DELETE ON custody_wallets
FOR EACH ROW
EXECUTE FUNCTION protect_custody_wallet();

CREATE FUNCTION protect_wallet_balance_snapshot()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION '% rows are immutable', TG_TABLE_NAME;
END;
$$;

CREATE TRIGGER wallet_balance_snapshot_runs_immutable_guard
BEFORE UPDATE OR DELETE ON wallet_balance_snapshot_runs
FOR EACH ROW
EXECUTE FUNCTION protect_wallet_balance_snapshot();

CREATE TRIGGER wallet_balance_snapshots_immutable_guard
BEFORE UPDATE OR DELETE ON wallet_balance_snapshots
FOR EACH ROW
EXECUTE FUNCTION protect_wallet_balance_snapshot();
