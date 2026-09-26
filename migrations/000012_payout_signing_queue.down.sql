DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM payouts WHERE status = 'ready_for_broadcast') THEN
        RAISE EXCEPTION 'cannot roll back payout signing schema while signed payouts exist';
    END IF;
END;
$$;

DROP TRIGGER payouts_execution_guard ON payouts;
DROP FUNCTION protect_payout_execution();
DROP INDEX payouts_transaction_id_unique_idx;
DROP INDEX payouts_signing_claim_idx;

ALTER TABLE payouts
    DROP CONSTRAINT payouts_review_state_check,
    DROP CONSTRAINT payouts_execution_lease_check,
    DROP CONSTRAINT payouts_status_check;

UPDATE payouts
SET status = 'ready_for_broadcast'
WHERE status = 'approved';

ALTER TABLE payouts
    DROP COLUMN last_execution_error,
    DROP COLUMN signed_transaction,
    DROP COLUMN transaction_id,
    DROP COLUMN signing_attempts,
    DROP COLUMN execution_lease_epoch,
    DROP COLUMN execution_lease_until,
    DROP COLUMN execution_lease_owner,
    ADD CONSTRAINT payouts_status_check CHECK (
        status IN ('pending_review', 'ready_for_broadcast', 'rejected')
    ),
    ADD CONSTRAINT payouts_review_state_check CHECK (
        (
            status = 'pending_review'
            AND reviewed_by IS NULL
            AND review_reason IS NULL
            AND reviewed_at IS NULL
            AND unfreeze_transaction_id IS NULL
        )
        OR
        (
            status = 'ready_for_broadcast'
            AND char_length(reviewed_by) BETWEEN 1 AND 128
            AND char_length(review_reason) BETWEEN 1 AND 512
            AND reviewed_at IS NOT NULL
            AND unfreeze_transaction_id IS NULL
        )
        OR
        (
            status = 'rejected'
            AND char_length(reviewed_by) BETWEEN 1 AND 128
            AND char_length(review_reason) BETWEEN 1 AND 512
            AND reviewed_at IS NOT NULL
            AND unfreeze_transaction_id IS NOT NULL
        )
    );

CREATE FUNCTION protect_payout_review()
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
        AND NEW.status IN ('ready_for_broadcast', 'rejected')
        AND NEW.updated_at >= OLD.updated_at
    THEN
        RETURN NEW;
    END IF;

    RAISE EXCEPTION 'invalid payout state transition: % -> %', OLD.status, NEW.status;
END;
$$;

CREATE TRIGGER payouts_review_guard
BEFORE UPDATE OR DELETE ON payouts
FOR EACH ROW
EXECUTE FUNCTION protect_payout_review();
