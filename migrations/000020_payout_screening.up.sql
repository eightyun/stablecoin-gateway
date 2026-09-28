DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM payouts
        WHERE status IN ('approved', 'ready_for_broadcast', 'confirming')
    ) THEN
        RAISE EXCEPTION 'drain approved and in-flight payouts before migration 20';
    END IF;
END;
$$;

CREATE TABLE payout_screening_jobs (
    payout_id UUID PRIMARY KEY REFERENCES payouts (id),
    network TEXT NOT NULL CHECK (network <> ''),
    address TEXT NOT NULL CHECK (address <> ''),
    status TEXT NOT NULL CHECK (status IN ('pending', 'processing', 'completed', 'closed')),
    current_result_id UUID,
    lease_owner TEXT,
    lease_until TIMESTAMPTZ,
    lease_epoch BIGINT NOT NULL DEFAULT 0 CHECK (lease_epoch >= 0),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    last_error TEXT CHECK (last_error IS NULL OR char_length(last_error) BETWEEN 1 AND 128),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT payout_screening_jobs_lease_check CHECK (
        (status = 'processing' AND lease_owner IS NOT NULL
            AND char_length(lease_owner) BETWEEN 1 AND 128 AND lease_until IS NOT NULL)
        OR
        (status <> 'processing' AND lease_owner IS NULL AND lease_until IS NULL)
    ),
    CONSTRAINT payout_screening_jobs_result_check CHECK (
        status <> 'completed' OR current_result_id IS NOT NULL
    )
);

CREATE TABLE payout_screening_results (
    id UUID PRIMARY KEY,
    payout_id UUID NOT NULL REFERENCES payout_screening_jobs (payout_id),
    attempt BIGINT NOT NULL CHECK (attempt > 0),
    provider TEXT NOT NULL CHECK (char_length(provider) BETWEEN 1 AND 128),
    decision TEXT NOT NULL CHECK (decision IN ('allow', 'deny', 'review')),
    reason_codes JSONB NOT NULL CHECK (jsonb_typeof(reason_codes) = 'array'),
    provider_reference TEXT NOT NULL CHECK (char_length(provider_reference) BETWEEN 1 AND 256),
    response_hash CHAR(64) NOT NULL CHECK (response_hash ~ '^[0-9a-f]{64}$'),
    checked_at TIMESTAMPTZ NOT NULL,
    valid_until TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (payout_id, attempt),
    UNIQUE (id, payout_id),
    CONSTRAINT payout_screening_results_time_check CHECK (valid_until > checked_at)
);

ALTER TABLE payout_screening_jobs
    ADD CONSTRAINT payout_screening_jobs_current_result_fk
    FOREIGN KEY (current_result_id, payout_id)
    REFERENCES payout_screening_results (id, payout_id);

INSERT INTO payout_screening_jobs (payout_id, network, address, status)
SELECT payout.id, asset.network, payout.destination_address, 'pending'
FROM payouts AS payout
JOIN assets AS asset ON asset.id = payout.asset_id
WHERE payout.status = 'pending_review';

CREATE INDEX payout_screening_jobs_claim_idx
    ON payout_screening_jobs (updated_at, payout_id)
    WHERE status IN ('pending', 'processing', 'completed');

CREATE INDEX payout_screening_results_payout_created_idx
    ON payout_screening_results (payout_id, created_at DESC);

CREATE FUNCTION protect_payout_screening_result()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'payout screening results are immutable';
END;
$$;

CREATE TRIGGER payout_screening_results_immutable_guard
BEFORE UPDATE OR DELETE ON payout_screening_results
FOR EACH ROW
EXECUTE FUNCTION protect_payout_screening_result();

CREATE FUNCTION protect_payout_screening_job()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'payout screening jobs are immutable';
    END IF;

    IF OLD.payout_id IS DISTINCT FROM NEW.payout_id
        OR OLD.network IS DISTINCT FROM NEW.network
        OR OLD.address IS DISTINCT FROM NEW.address
        OR OLD.created_at IS DISTINCT FROM NEW.created_at
        OR NEW.lease_epoch < OLD.lease_epoch
        OR NEW.attempts < OLD.attempts
    THEN
        RAISE EXCEPTION 'payout screening job identity is immutable';
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

    RAISE EXCEPTION 'invalid payout screening job transition: % -> %', OLD.status, NEW.status;
END;
$$;

CREATE TRIGGER payout_screening_jobs_guard
BEFORE UPDATE OR DELETE ON payout_screening_jobs
FOR EACH ROW
EXECUTE FUNCTION protect_payout_screening_job();
