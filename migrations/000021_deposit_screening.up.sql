ALTER TABLE deposit_event_matches
    DROP CONSTRAINT deposit_event_matches_reason_check,
    ADD CONSTRAINT deposit_event_matches_reason_check CHECK (reason IN (
        'no_intent', 'before_intent', 'after_expiry', 'missing_block_time',
        'asset_disabled', 'merchant_inactive', 'address_retired', 'overpaid',
        'ledger_account_unavailable', 'amount_out_of_range',
        'screening_denied', 'screening_review'
    ));

CREATE TABLE deposit_screening_jobs (
    id UUID PRIMARY KEY,
    network TEXT NOT NULL,
    contract TEXT NOT NULL,
    transaction_id TEXT NOT NULL,
    log_index BIGINT NOT NULL CHECK (log_index BETWEEN 0 AND 4294967295),
    source_address TEXT NOT NULL CHECK (source_address <> ''),
    destination_address TEXT NOT NULL CHECK (destination_address <> ''),
    status TEXT NOT NULL CHECK (status IN ('pending', 'processing', 'completed', 'closed')),
    current_result_id UUID,
    lease_owner TEXT,
    lease_until TIMESTAMPTZ,
    lease_epoch BIGINT NOT NULL DEFAULT 0 CHECK (lease_epoch >= 0),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    last_error TEXT CHECK (last_error IS NULL OR char_length(last_error) BETWEEN 1 AND 128),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (network, contract, transaction_id, log_index),
    UNIQUE (id, network, contract, transaction_id, log_index),
    FOREIGN KEY (network, contract, transaction_id, log_index)
        REFERENCES chain_events (network, contract, transaction_id, log_index),
    CONSTRAINT deposit_screening_jobs_lease_check CHECK (
        (status = 'processing' AND lease_owner IS NOT NULL
            AND char_length(lease_owner) BETWEEN 1 AND 128 AND lease_until IS NOT NULL)
        OR
        (status <> 'processing' AND lease_owner IS NULL AND lease_until IS NULL)
    ),
    CONSTRAINT deposit_screening_jobs_result_check CHECK (
        status <> 'completed' OR current_result_id IS NOT NULL
    )
);

CREATE TABLE deposit_screening_results (
    id UUID PRIMARY KEY,
    job_id UUID NOT NULL REFERENCES deposit_screening_jobs (id),
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
    CONSTRAINT deposit_screening_results_time_check CHECK (valid_until > checked_at)
);

ALTER TABLE deposit_screening_jobs
    ADD CONSTRAINT deposit_screening_jobs_current_result_fk
    FOREIGN KEY (current_result_id, id)
    REFERENCES deposit_screening_results (id, job_id);

CREATE INDEX deposit_screening_jobs_claim_idx
    ON deposit_screening_jobs (updated_at, id)
    WHERE status IN ('pending', 'processing', 'completed');

CREATE INDEX deposit_screening_results_job_created_idx
    ON deposit_screening_results (job_id, created_at DESC);

CREATE FUNCTION protect_deposit_screening_result()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'deposit screening results are immutable';
END;
$$;

CREATE TRIGGER deposit_screening_results_immutable_guard
BEFORE UPDATE OR DELETE ON deposit_screening_results
FOR EACH ROW
EXECUTE FUNCTION protect_deposit_screening_result();

CREATE FUNCTION protect_deposit_screening_job()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'deposit screening jobs are immutable';
    END IF;

    IF OLD.id IS DISTINCT FROM NEW.id
        OR OLD.network IS DISTINCT FROM NEW.network
        OR OLD.contract IS DISTINCT FROM NEW.contract
        OR OLD.transaction_id IS DISTINCT FROM NEW.transaction_id
        OR OLD.log_index IS DISTINCT FROM NEW.log_index
        OR OLD.source_address IS DISTINCT FROM NEW.source_address
        OR OLD.destination_address IS DISTINCT FROM NEW.destination_address
        OR OLD.created_at IS DISTINCT FROM NEW.created_at
        OR NEW.lease_epoch < OLD.lease_epoch
        OR NEW.attempts < OLD.attempts
    THEN
        RAISE EXCEPTION 'deposit screening job identity is immutable';
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

    IF OLD.status IN ('pending', 'processing', 'completed')
        AND NEW.status = 'closed'
        AND NEW.lease_epoch = OLD.lease_epoch
        AND NEW.attempts = OLD.attempts
        AND NEW.current_result_id IS NOT DISTINCT FROM OLD.current_result_id
        AND NEW.last_error IS NOT DISTINCT FROM OLD.last_error
        AND NEW.updated_at >= OLD.updated_at
    THEN
        RETURN NEW;
    END IF;

    RAISE EXCEPTION 'invalid deposit screening job transition: % -> %', OLD.status, NEW.status;
END;
$$;

CREATE TRIGGER deposit_screening_jobs_guard
BEFORE UPDATE OR DELETE ON deposit_screening_jobs
FOR EACH ROW
EXECUTE FUNCTION protect_deposit_screening_job();
