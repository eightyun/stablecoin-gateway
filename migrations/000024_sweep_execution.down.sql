DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM sweep_executions
        WHERE status IN ('confirming', 'succeeded', 'failed')
           OR broadcast_attempts > 0
           OR confirmation_attempts > 0
    ) THEN
        RAISE EXCEPTION 'complete or clear sweep execution state before migration 24 rollback';
    END IF;
END;
$$;

DROP TRIGGER sweep_executions_guard ON sweep_executions;
DROP FUNCTION protect_sweep_execution();
DROP INDEX sweep_executions_confirmation_claim_idx;
DROP INDEX sweep_executions_broadcast_claim_idx;

ALTER TABLE sweep_executions DROP CONSTRAINT sweep_executions_state_check;
ALTER TABLE sweep_executions DROP CONSTRAINT sweep_executions_lease_check;
ALTER TABLE sweep_executions DROP CONSTRAINT sweep_executions_status_check;

ALTER TABLE sweep_executions
    DROP COLUMN failure_reason,
    DROP COLUMN failed_at,
    DROP COLUMN confirmed_at,
    DROP COLUMN next_confirmation_at,
    DROP COLUMN confirmation_attempts,
    DROP COLUMN broadcast_result,
    DROP COLUMN broadcasted_at,
    DROP COLUMN broadcast_attempts,
    ADD CONSTRAINT sweep_executions_status_check CHECK (status IN ('planned', 'ready_for_broadcast')),
    ADD CONSTRAINT sweep_executions_lease_check CHECK (
        (lease_owner IS NULL AND lease_until IS NULL)
        OR
        (status = 'planned' AND char_length(lease_owner) BETWEEN 1 AND 128 AND lease_until IS NOT NULL)
    ),
    ADD CONSTRAINT sweep_executions_state_check CHECK (
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
    );

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
