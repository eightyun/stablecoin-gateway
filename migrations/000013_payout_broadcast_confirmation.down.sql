DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM payouts WHERE status IN ('confirming', 'succeeded', 'failed')) THEN
        RAISE EXCEPTION 'cannot roll back payout confirmation schema while executing payouts exist';
    END IF;
END;
$$;

DROP TRIGGER payouts_lifecycle_guard ON payouts;
DROP FUNCTION protect_payout_lifecycle();
DROP INDEX payouts_confirmation_claim_idx;
DROP INDEX payouts_broadcast_claim_idx;

ALTER TABLE payouts
    DROP CONSTRAINT payouts_lifecycle_check,
    DROP CONSTRAINT payouts_execution_lease_check,
    DROP CONSTRAINT payouts_broadcast_result_check,
    DROP CONSTRAINT payouts_status_check,
    DROP COLUMN settlement_transaction_id,
    DROP COLUMN failure_reason,
    DROP COLUMN failed_at,
    DROP COLUMN confirmed_at,
    DROP COLUMN next_confirmation_at,
    DROP COLUMN confirmation_attempts,
    DROP COLUMN broadcast_result,
    DROP COLUMN broadcasted_at,
    DROP COLUMN broadcast_attempts,
    DROP COLUMN transaction_expires_at,
    ADD CONSTRAINT payouts_status_check CHECK (
        status IN ('pending_review', 'approved', 'ready_for_broadcast', 'rejected')
    ),
    ADD CONSTRAINT payouts_execution_lease_check CHECK (
        (execution_lease_owner IS NULL AND execution_lease_until IS NULL)
        OR
        (
            status = 'approved'
            AND char_length(execution_lease_owner) BETWEEN 1 AND 128
            AND execution_lease_until IS NOT NULL
        )
    ),
    ADD CONSTRAINT payouts_review_state_check CHECK (
        (
            status = 'pending_review'
            AND reviewed_by IS NULL
            AND review_reason IS NULL
            AND reviewed_at IS NULL
            AND unfreeze_transaction_id IS NULL
            AND transaction_id IS NULL
            AND signed_transaction IS NULL
            AND execution_lease_epoch = 0
            AND signing_attempts = 0
            AND last_execution_error IS NULL
        )
        OR
        (
            status = 'approved'
            AND char_length(reviewed_by) BETWEEN 1 AND 128
            AND char_length(review_reason) BETWEEN 1 AND 512
            AND reviewed_at IS NOT NULL
            AND unfreeze_transaction_id IS NULL
            AND transaction_id IS NULL
            AND signed_transaction IS NULL
        )
        OR
        (
            status = 'ready_for_broadcast'
            AND char_length(reviewed_by) BETWEEN 1 AND 128
            AND char_length(review_reason) BETWEEN 1 AND 512
            AND reviewed_at IS NOT NULL
            AND unfreeze_transaction_id IS NULL
            AND transaction_id ~ '^[0-9a-f]{64}$'
            AND octet_length(signed_transaction) BETWEEN 1 AND 1048576
            AND signing_attempts > 0
            AND execution_lease_owner IS NULL
            AND execution_lease_until IS NULL
            AND last_execution_error IS NULL
        )
        OR
        (
            status = 'rejected'
            AND char_length(reviewed_by) BETWEEN 1 AND 128
            AND char_length(review_reason) BETWEEN 1 AND 512
            AND reviewed_at IS NOT NULL
            AND unfreeze_transaction_id IS NOT NULL
            AND transaction_id IS NULL
            AND signed_transaction IS NULL
            AND execution_lease_epoch = 0
            AND signing_attempts = 0
            AND last_execution_error IS NULL
        )
    );

CREATE FUNCTION protect_payout_execution()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'payouts are immutable';
    END IF;

    IF OLD.id IS DISTINCT FROM NEW.id
        OR OLD.merchant_id IS DISTINCT FROM NEW.merchant_id
        OR OLD.asset_id IS DISTINCT FROM NEW.asset_id
        OR OLD.idempotency_key IS DISTINCT FROM NEW.idempotency_key
        OR OLD.merchant_reference IS DISTINCT FROM NEW.merchant_reference
        OR OLD.request_hash IS DISTINCT FROM NEW.request_hash
        OR OLD.destination_address IS DISTINCT FROM NEW.destination_address
        OR OLD.amount IS DISTINCT FROM NEW.amount
        OR OLD.freeze_transaction_id IS DISTINCT FROM NEW.freeze_transaction_id
        OR OLD.created_at IS DISTINCT FROM NEW.created_at
    THEN
        RAISE EXCEPTION 'payout identity is immutable';
    END IF;

    IF OLD.status = 'pending_review'
        AND NEW.status = 'approved'
        AND NEW.execution_lease_owner IS NULL
        AND NEW.execution_lease_until IS NULL
        AND NEW.execution_lease_epoch = 0
        AND NEW.signing_attempts = 0
        AND NEW.transaction_id IS NULL
        AND NEW.signed_transaction IS NULL
        AND NEW.last_execution_error IS NULL
        AND NEW.updated_at >= OLD.updated_at
    THEN
        RETURN NEW;
    END IF;

    IF OLD.status = 'pending_review'
        AND NEW.status = 'rejected'
        AND NEW.updated_at >= OLD.updated_at
    THEN
        RETURN NEW;
    END IF;

    IF OLD.status = 'approved'
        AND NEW.status = 'approved'
        AND NEW.reviewed_by = OLD.reviewed_by
        AND NEW.review_reason = OLD.review_reason
        AND NEW.reviewed_at = OLD.reviewed_at
        AND NEW.unfreeze_transaction_id IS NOT DISTINCT FROM OLD.unfreeze_transaction_id
        AND NEW.transaction_id IS NULL
        AND NEW.signed_transaction IS NULL
        AND (
            (
                NEW.execution_lease_epoch = OLD.execution_lease_epoch
                AND NEW.signing_attempts = OLD.signing_attempts
            )
            OR
            (
                NEW.execution_lease_epoch = OLD.execution_lease_epoch + 1
                AND NEW.signing_attempts = OLD.signing_attempts + 1
            )
        )
        AND NEW.updated_at >= OLD.updated_at
    THEN
        RETURN NEW;
    END IF;

    IF OLD.status = 'approved'
        AND NEW.status = 'ready_for_broadcast'
        AND NEW.reviewed_by = OLD.reviewed_by
        AND NEW.review_reason = OLD.review_reason
        AND NEW.reviewed_at = OLD.reviewed_at
        AND NEW.unfreeze_transaction_id IS NOT DISTINCT FROM OLD.unfreeze_transaction_id
        AND NEW.execution_lease_epoch = OLD.execution_lease_epoch
        AND NEW.signing_attempts = OLD.signing_attempts
        AND NEW.updated_at >= OLD.updated_at
    THEN
        RETURN NEW;
    END IF;

    RAISE EXCEPTION 'invalid payout state transition: % -> %', OLD.status, NEW.status;
END;
$$;

CREATE TRIGGER payouts_execution_guard
BEFORE UPDATE OR DELETE ON payouts
FOR EACH ROW
EXECUTE FUNCTION protect_payout_execution();
