CREATE TABLE sweep_screening_jobs (
    id UUID PRIMARY KEY,
    asset_id TEXT NOT NULL REFERENCES assets (id),
    source_wallet_id UUID NOT NULL,
    source_address TEXT NOT NULL CHECK (source_address <> ''),
    destination_wallet_id UUID NOT NULL,
    destination_address TEXT NOT NULL CHECK (destination_address <> ''),
    snapshot_run_id UUID NOT NULL,
    snapshot_block_height BIGINT NOT NULL CHECK (snapshot_block_height > 0),
    amount NUMERIC(78, 0) NOT NULL CHECK (amount > 0),
    status TEXT NOT NULL CHECK (status IN ('pending', 'processing', 'completed')),
    current_result_id UUID,
    lease_owner TEXT,
    lease_until TIMESTAMPTZ,
    lease_epoch BIGINT NOT NULL DEFAULT 0 CHECK (lease_epoch >= 0),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    last_error TEXT CHECK (last_error IS NULL OR char_length(last_error) BETWEEN 1 AND 128),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (source_wallet_id, snapshot_run_id),
    UNIQUE (id, source_wallet_id, snapshot_run_id),
    FOREIGN KEY (source_wallet_id, asset_id, source_address)
        REFERENCES custody_wallets (id, asset_id, address),
    FOREIGN KEY (destination_wallet_id, asset_id, destination_address)
        REFERENCES custody_wallets (id, asset_id, address),
    FOREIGN KEY (snapshot_run_id, asset_id, snapshot_block_height)
        REFERENCES wallet_balance_snapshot_runs (id, asset_id, block_height),
    FOREIGN KEY (snapshot_run_id, source_wallet_id, asset_id, amount)
        REFERENCES wallet_balance_snapshots (run_id, wallet_id, asset_id, balance),
    CONSTRAINT sweep_screening_jobs_distinct_wallets_check
        CHECK (source_wallet_id <> destination_wallet_id),
    CONSTRAINT sweep_screening_jobs_lease_check CHECK (
        (status = 'processing' AND lease_owner IS NOT NULL
            AND char_length(lease_owner) BETWEEN 1 AND 128 AND lease_until IS NOT NULL)
        OR
        (status <> 'processing' AND lease_owner IS NULL AND lease_until IS NULL)
    ),
    CONSTRAINT sweep_screening_jobs_result_check CHECK (
        status <> 'completed' OR current_result_id IS NOT NULL
    )
);

CREATE TABLE sweep_screening_results (
    id UUID PRIMARY KEY,
    job_id UUID NOT NULL REFERENCES sweep_screening_jobs (id),
    attempt BIGINT NOT NULL CHECK (attempt > 0),
    provider TEXT NOT NULL CHECK (char_length(provider) BETWEEN 1 AND 128),
    decision TEXT NOT NULL CHECK (decision IN ('allow', 'deny', 'review')),
    reason_codes JSONB NOT NULL CHECK (jsonb_typeof(reason_codes) = 'array'),
    provider_reference TEXT NOT NULL CHECK (char_length(provider_reference) BETWEEN 1 AND 256),
    response_hash CHAR(64) NOT NULL CHECK (response_hash ~ '^[0-9a-f]{64}$'),
    checked_at TIMESTAMPTZ NOT NULL,
    valid_until TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (job_id, attempt),
    UNIQUE (id, job_id),
    CONSTRAINT sweep_screening_results_time_check CHECK (valid_until > checked_at)
);

ALTER TABLE sweep_screening_jobs
    ADD CONSTRAINT sweep_screening_jobs_current_result_fk
    FOREIGN KEY (current_result_id, id)
    REFERENCES sweep_screening_results (id, job_id);

CREATE INDEX sweep_screening_jobs_claim_idx
    ON sweep_screening_jobs (updated_at, id)
    WHERE status IN ('pending', 'processing', 'completed');

CREATE INDEX sweep_screening_results_job_created_idx
    ON sweep_screening_results (job_id, created_at DESC);

CREATE FUNCTION protect_sweep_screening_result()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'sweep screening results are immutable';
END;
$$;

CREATE TRIGGER sweep_screening_results_immutable_guard
BEFORE UPDATE OR DELETE ON sweep_screening_results
FOR EACH ROW
EXECUTE FUNCTION protect_sweep_screening_result();

CREATE FUNCTION protect_sweep_screening_job()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'sweep screening jobs are immutable';
    END IF;

    IF OLD.id IS DISTINCT FROM NEW.id
        OR OLD.asset_id IS DISTINCT FROM NEW.asset_id
        OR OLD.source_wallet_id IS DISTINCT FROM NEW.source_wallet_id
        OR OLD.source_address IS DISTINCT FROM NEW.source_address
        OR OLD.destination_wallet_id IS DISTINCT FROM NEW.destination_wallet_id
        OR OLD.destination_address IS DISTINCT FROM NEW.destination_address
        OR OLD.snapshot_run_id IS DISTINCT FROM NEW.snapshot_run_id
        OR OLD.snapshot_block_height IS DISTINCT FROM NEW.snapshot_block_height
        OR OLD.amount IS DISTINCT FROM NEW.amount
        OR OLD.created_at IS DISTINCT FROM NEW.created_at
        OR NEW.lease_epoch < OLD.lease_epoch
        OR NEW.attempts < OLD.attempts
    THEN
        RAISE EXCEPTION 'sweep screening job identity is immutable';
    END IF;

    IF OLD.status IN ('pending', 'processing', 'completed')
        AND NEW.status = 'processing'
        AND NEW.lease_epoch = OLD.lease_epoch + 1
        AND NEW.attempts = OLD.attempts + 1
        AND NEW.current_result_id IS NOT DISTINCT FROM OLD.current_result_id
        AND NEW.last_error IS NULL
        AND NEW.updated_at >= OLD.updated_at
    THEN
        RETURN NEW;
    END IF;

    IF OLD.status = 'processing'
        AND NEW.status = 'pending'
        AND NEW.lease_epoch = OLD.lease_epoch
        AND NEW.attempts = OLD.attempts
        AND NEW.current_result_id IS NOT DISTINCT FROM OLD.current_result_id
        AND NEW.last_error IS NOT NULL
        AND NEW.updated_at >= OLD.updated_at
    THEN
        RETURN NEW;
    END IF;

    IF OLD.status = 'processing'
        AND NEW.status = 'completed'
        AND NEW.lease_epoch = OLD.lease_epoch
        AND NEW.attempts = OLD.attempts
        AND NEW.current_result_id IS NOT NULL
        AND NEW.current_result_id IS DISTINCT FROM OLD.current_result_id
        AND NEW.last_error IS NULL
        AND NEW.updated_at >= OLD.updated_at
    THEN
        RETURN NEW;
    END IF;

    RAISE EXCEPTION 'invalid sweep screening job transition: % -> %', OLD.status, NEW.status;
END;
$$;

CREATE TRIGGER sweep_screening_jobs_guard
BEFORE UPDATE OR DELETE ON sweep_screening_jobs
FOR EACH ROW
EXECUTE FUNCTION protect_sweep_screening_job();
