CREATE TABLE sweep_executions (
    plan_id UUID PRIMARY KEY REFERENCES sweep_plans (id),
    status TEXT NOT NULL DEFAULT 'planned' CHECK (status IN ('planned', 'ready_for_broadcast')),
    lease_owner TEXT,
    lease_until TIMESTAMPTZ,
    lease_epoch BIGINT NOT NULL DEFAULT 0 CHECK (lease_epoch >= 0),
    signing_attempts INTEGER NOT NULL DEFAULT 0 CHECK (signing_attempts >= 0),
    observed_balance NUMERIC(78, 0),
    observed_block_height BIGINT,
    observed_block_hash CHAR(64),
    transaction_id CHAR(64),
    signed_transaction BYTEA,
    transaction_expires_at TIMESTAMPTZ,
    last_error TEXT CHECK (last_error IS NULL OR char_length(last_error) BETWEEN 1 AND 128),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT sweep_executions_lease_check CHECK (
        (lease_owner IS NULL AND lease_until IS NULL)
        OR
        (status = 'planned' AND char_length(lease_owner) BETWEEN 1 AND 128 AND lease_until IS NOT NULL)
    ),
    CONSTRAINT sweep_executions_state_check CHECK (
        (
            status = 'planned'
            AND observed_balance IS NULL
            AND observed_block_height IS NULL
            AND observed_block_hash IS NULL
            AND transaction_id IS NULL
            AND signed_transaction IS NULL
            AND transaction_expires_at IS NULL
        )
        OR
        (
            status = 'ready_for_broadcast'
            AND lease_owner IS NULL
            AND lease_until IS NULL
            AND observed_balance IS NOT NULL
            AND observed_balance > 0
            AND observed_block_height IS NOT NULL
            AND observed_block_height > 0
            AND observed_block_hash ~ '^[0-9a-f]{64}$'
            AND transaction_id ~ '^[0-9a-f]{64}$'
            AND octet_length(signed_transaction) BETWEEN 1 AND 1048576
            AND transaction_expires_at IS NOT NULL
            AND signing_attempts > 0
            AND last_error IS NULL
        )
    )
);

CREATE INDEX sweep_executions_signing_claim_idx
    ON sweep_executions (updated_at, plan_id)
    WHERE status = 'planned';

CREATE UNIQUE INDEX sweep_executions_transaction_id_unique_idx
    ON sweep_executions (transaction_id)
    WHERE transaction_id IS NOT NULL;

INSERT INTO sweep_executions (plan_id)
SELECT id FROM sweep_plans;

CREATE FUNCTION create_sweep_execution()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    INSERT INTO sweep_executions (plan_id) VALUES (NEW.id);
    RETURN NEW;
END;
$$;

CREATE TRIGGER sweep_plans_create_execution
AFTER INSERT ON sweep_plans
FOR EACH ROW
EXECUTE FUNCTION create_sweep_execution();

CREATE FUNCTION protect_sweep_execution()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'sweep executions are immutable';
    END IF;

    IF OLD.plan_id IS DISTINCT FROM NEW.plan_id
        OR OLD.created_at IS DISTINCT FROM NEW.created_at
        OR NEW.lease_epoch < OLD.lease_epoch
        OR NEW.signing_attempts < OLD.signing_attempts
    THEN
        RAISE EXCEPTION 'sweep execution identity is immutable';
    END IF;

    IF OLD.transaction_id IS NOT NULL
        AND (
            OLD.transaction_id IS DISTINCT FROM NEW.transaction_id
            OR OLD.signed_transaction IS DISTINCT FROM NEW.signed_transaction
            OR OLD.transaction_expires_at IS DISTINCT FROM NEW.transaction_expires_at
        )
    THEN
        RAISE EXCEPTION 'signed sweep transaction is immutable';
    END IF;

    IF OLD.status = 'planned' AND NEW.status = 'planned'
        AND (
            (
                OLD.lease_owner IS NOT NULL
                AND NEW.lease_owner IS NULL
                AND NEW.lease_until IS NULL
                AND NEW.lease_epoch = OLD.lease_epoch
                AND NEW.signing_attempts = OLD.signing_attempts
                AND NEW.last_error IS NOT NULL
            )
            OR
            (
                (OLD.lease_owner IS NULL OR OLD.lease_until <= clock_timestamp())
                AND NEW.lease_owner IS NOT NULL
                AND NEW.lease_until > clock_timestamp()
                AND NEW.lease_epoch = OLD.lease_epoch + 1
                AND NEW.signing_attempts = OLD.signing_attempts + 1
                AND NEW.last_error IS NULL
            )
        )
        AND NEW.observed_balance IS NULL
        AND NEW.observed_block_height IS NULL
        AND NEW.observed_block_hash IS NULL
        AND NEW.transaction_id IS NULL
        AND NEW.signed_transaction IS NULL
        AND NEW.transaction_expires_at IS NULL
        AND NEW.updated_at >= OLD.updated_at
    THEN
        RETURN NEW;
    END IF;

    IF OLD.status = 'planned' AND NEW.status = 'ready_for_broadcast'
        AND OLD.lease_owner IS NOT NULL
        AND NEW.lease_owner IS NULL
        AND NEW.lease_until IS NULL
        AND NEW.lease_epoch = OLD.lease_epoch
        AND NEW.signing_attempts = OLD.signing_attempts
        AND NEW.updated_at >= OLD.updated_at
    THEN
        RETURN NEW;
    END IF;

    RAISE EXCEPTION 'invalid sweep execution transition: % -> %', OLD.status, NEW.status;
END;
$$;

CREATE TRIGGER sweep_executions_guard
BEFORE UPDATE OR DELETE ON sweep_executions
FOR EACH ROW
EXECUTE FUNCTION protect_sweep_execution();
