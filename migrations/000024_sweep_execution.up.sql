DROP TRIGGER sweep_executions_guard ON sweep_executions;
DROP FUNCTION protect_sweep_execution();

ALTER TABLE sweep_executions DROP CONSTRAINT sweep_executions_status_check;
ALTER TABLE sweep_executions DROP CONSTRAINT sweep_executions_lease_check;
ALTER TABLE sweep_executions DROP CONSTRAINT sweep_executions_state_check;

ALTER TABLE sweep_executions
    ADD COLUMN broadcast_attempts INTEGER NOT NULL DEFAULT 0 CHECK (broadcast_attempts >= 0),
    ADD COLUMN broadcasted_at TIMESTAMPTZ,
    ADD COLUMN broadcast_result TEXT CHECK (broadcast_result IN ('accepted', 'unknown')),
    ADD COLUMN confirmation_attempts INTEGER NOT NULL DEFAULT 0 CHECK (confirmation_attempts >= 0),
    ADD COLUMN next_confirmation_at TIMESTAMPTZ,
    ADD COLUMN confirmed_at TIMESTAMPTZ,
    ADD COLUMN failed_at TIMESTAMPTZ,
    ADD COLUMN failure_reason TEXT CHECK (
        failure_reason IS NULL OR failure_reason ~ '^[a-z0-9][a-z0-9_.-]{0,127}$'
    ),
    ADD CONSTRAINT sweep_executions_status_check CHECK (
        status IN ('planned', 'ready_for_broadcast', 'confirming', 'succeeded', 'failed')
    ),
    ADD CONSTRAINT sweep_executions_lease_check CHECK (
        (lease_owner IS NULL AND lease_until IS NULL)
        OR
        (
            status IN ('planned', 'ready_for_broadcast', 'confirming')
            AND char_length(lease_owner) BETWEEN 1 AND 128
            AND lease_until IS NOT NULL
        )
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
            AND broadcasted_at IS NULL
            AND broadcast_result IS NULL
            AND next_confirmation_at IS NULL
            AND confirmed_at IS NULL
            AND failed_at IS NULL
            AND failure_reason IS NULL
            AND broadcast_attempts = 0
            AND confirmation_attempts = 0
        )
        OR
        (
            status IN ('ready_for_broadcast', 'confirming', 'succeeded', 'failed')
            AND observed_balance > 0
            AND observed_block_height > 0
            AND observed_block_hash ~ '^[0-9a-f]{64}$'
            AND transaction_id ~ '^[0-9a-f]{64}$'
            AND octet_length(signed_transaction) BETWEEN 1 AND 1048576
            AND transaction_expires_at IS NOT NULL
            AND signing_attempts > 0
            AND (
                (
                    status = 'ready_for_broadcast'
                    AND broadcasted_at IS NULL
                    AND broadcast_result IS NULL
                    AND next_confirmation_at IS NULL
                    AND confirmed_at IS NULL
                    AND failed_at IS NULL
                    AND failure_reason IS NULL
                    AND confirmation_attempts = 0
                    AND last_error IS NULL
                )
                OR
                (
                    status = 'confirming'
                    AND broadcasted_at IS NOT NULL
                    AND broadcast_result IS NOT NULL
                    AND next_confirmation_at IS NOT NULL
                    AND confirmed_at IS NULL
                    AND failed_at IS NULL
                    AND failure_reason IS NULL
                    AND broadcast_attempts > 0
                )
                OR
                (
                    status = 'succeeded'
                    AND lease_owner IS NULL
                    AND lease_until IS NULL
                    AND broadcasted_at IS NOT NULL
                    AND broadcast_result IS NOT NULL
                    AND next_confirmation_at IS NULL
                    AND confirmed_at IS NOT NULL
                    AND failed_at IS NULL
                    AND failure_reason IS NULL
                    AND broadcast_attempts > 0
                    AND confirmation_attempts > 0
                )
                OR
                (
                    status = 'failed'
                    AND lease_owner IS NULL
                    AND lease_until IS NULL
                    AND broadcasted_at IS NOT NULL
                    AND broadcast_result IS NOT NULL
                    AND next_confirmation_at IS NULL
                    AND confirmed_at IS NULL
                    AND failed_at IS NOT NULL
                    AND failure_reason IS NOT NULL
                    AND broadcast_attempts > 0
                    AND confirmation_attempts > 0
                )
            )
        )
    );

CREATE INDEX sweep_executions_broadcast_claim_idx
    ON sweep_executions (updated_at, plan_id)
    WHERE status = 'ready_for_broadcast';

CREATE INDEX sweep_executions_confirmation_claim_idx
    ON sweep_executions (next_confirmation_at, plan_id)
    WHERE status = 'confirming';

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
        OR NEW.broadcast_attempts < OLD.broadcast_attempts
        OR NEW.confirmation_attempts < OLD.confirmation_attempts
    THEN
        RAISE EXCEPTION 'sweep execution identity is immutable';
    END IF;

    IF OLD.transaction_id IS NOT NULL
        AND (
            OLD.observed_balance IS DISTINCT FROM NEW.observed_balance
            OR OLD.observed_block_height IS DISTINCT FROM NEW.observed_block_height
            OR OLD.observed_block_hash IS DISTINCT FROM NEW.observed_block_hash
            OR OLD.transaction_id IS DISTINCT FROM NEW.transaction_id
            OR OLD.signed_transaction IS DISTINCT FROM NEW.signed_transaction
            OR OLD.transaction_expires_at IS DISTINCT FROM NEW.transaction_expires_at
        )
    THEN
        RAISE EXCEPTION 'signed sweep transaction is immutable';
    END IF;

    IF OLD.broadcasted_at IS NOT NULL
        AND (
            OLD.broadcasted_at IS DISTINCT FROM NEW.broadcasted_at
            OR OLD.broadcast_result IS DISTINCT FROM NEW.broadcast_result
        )
    THEN
        RAISE EXCEPTION 'sweep broadcast result is immutable';
    END IF;

    IF OLD.status = 'planned' AND NEW.status = 'planned'
        AND (
            (
                OLD.lease_owner IS NOT NULL
                AND NEW.lease_owner IS NULL
                AND NEW.lease_until IS NULL
                AND NEW.lease_epoch = OLD.lease_epoch
                AND NEW.signing_attempts = OLD.signing_attempts
                AND NEW.broadcast_attempts = OLD.broadcast_attempts
                AND NEW.confirmation_attempts = OLD.confirmation_attempts
                AND NEW.last_error IS NOT NULL
            )
            OR
            (
                (OLD.lease_owner IS NULL OR OLD.lease_until <= clock_timestamp())
                AND NEW.lease_owner IS NOT NULL
                AND NEW.lease_until > clock_timestamp()
                AND NEW.lease_epoch = OLD.lease_epoch + 1
                AND NEW.signing_attempts = OLD.signing_attempts + 1
                AND NEW.broadcast_attempts = OLD.broadcast_attempts
                AND NEW.confirmation_attempts = OLD.confirmation_attempts
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
        AND NEW.broadcast_attempts = OLD.broadcast_attempts
        AND NEW.confirmation_attempts = OLD.confirmation_attempts
        AND NEW.updated_at >= OLD.updated_at
    THEN
        RETURN NEW;
    END IF;

    IF OLD.status = 'ready_for_broadcast' AND NEW.status = 'ready_for_broadcast'
        AND (OLD.lease_owner IS NULL OR OLD.lease_until <= clock_timestamp())
        AND NEW.lease_owner IS NOT NULL
        AND NEW.lease_until > clock_timestamp()
        AND NEW.lease_epoch = OLD.lease_epoch + 1
        AND NEW.signing_attempts = OLD.signing_attempts
        AND NEW.broadcast_attempts = OLD.broadcast_attempts + 1
        AND NEW.confirmation_attempts = OLD.confirmation_attempts
        AND NEW.updated_at >= OLD.updated_at
    THEN
        RETURN NEW;
    END IF;

    IF OLD.status = 'ready_for_broadcast' AND NEW.status = 'confirming'
        AND OLD.lease_owner IS NOT NULL
        AND NEW.lease_owner IS NULL
        AND NEW.lease_until IS NULL
        AND NEW.lease_epoch = OLD.lease_epoch
        AND NEW.signing_attempts = OLD.signing_attempts
        AND NEW.broadcast_attempts = OLD.broadcast_attempts
        AND NEW.confirmation_attempts = OLD.confirmation_attempts
        AND NEW.updated_at >= OLD.updated_at
    THEN
        RETURN NEW;
    END IF;

    IF OLD.status = 'confirming' AND NEW.status = 'confirming'
        AND (
            (
                OLD.lease_owner IS NOT NULL
                AND NEW.lease_owner IS NULL
                AND NEW.lease_until IS NULL
                AND NEW.lease_epoch = OLD.lease_epoch
                AND NEW.signing_attempts = OLD.signing_attempts
                AND NEW.broadcast_attempts = OLD.broadcast_attempts
                AND NEW.confirmation_attempts = OLD.confirmation_attempts
            )
            OR
            (
                (OLD.lease_owner IS NULL OR OLD.lease_until <= clock_timestamp())
                AND NEW.lease_owner IS NOT NULL
                AND NEW.lease_until > clock_timestamp()
                AND NEW.lease_epoch = OLD.lease_epoch + 1
                AND NEW.signing_attempts = OLD.signing_attempts
                AND NEW.broadcast_attempts = OLD.broadcast_attempts
                AND NEW.confirmation_attempts = OLD.confirmation_attempts + 1
            )
        )
        AND NEW.updated_at >= OLD.updated_at
    THEN
        RETURN NEW;
    END IF;

    IF OLD.status = 'confirming' AND NEW.status IN ('succeeded', 'failed')
        AND OLD.lease_owner IS NOT NULL
        AND NEW.lease_owner IS NULL
        AND NEW.lease_until IS NULL
        AND NEW.lease_epoch = OLD.lease_epoch
        AND NEW.signing_attempts = OLD.signing_attempts
        AND NEW.broadcast_attempts = OLD.broadcast_attempts
        AND NEW.confirmation_attempts = OLD.confirmation_attempts
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
