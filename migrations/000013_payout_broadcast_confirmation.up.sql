DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM payouts WHERE status = 'ready_for_broadcast') THEN
        RAISE EXCEPTION 'complete or clear ready_for_broadcast payouts before migration 13';
    END IF;
END;
$$;

DROP TRIGGER payouts_execution_guard ON payouts;
DROP FUNCTION protect_payout_execution();

ALTER TABLE payouts
    DROP CONSTRAINT payouts_review_state_check,
    DROP CONSTRAINT payouts_execution_lease_check,
    DROP CONSTRAINT payouts_status_check,
    ADD COLUMN transaction_expires_at TIMESTAMPTZ,
    ADD COLUMN broadcast_attempts INTEGER NOT NULL DEFAULT 0 CHECK (broadcast_attempts >= 0),
    ADD COLUMN broadcasted_at TIMESTAMPTZ,
    ADD COLUMN broadcast_result TEXT,
    ADD COLUMN confirmation_attempts INTEGER NOT NULL DEFAULT 0 CHECK (confirmation_attempts >= 0),
    ADD COLUMN next_confirmation_at TIMESTAMPTZ,
    ADD COLUMN confirmed_at TIMESTAMPTZ,
    ADD COLUMN failed_at TIMESTAMPTZ,
    ADD COLUMN failure_reason TEXT,
    ADD COLUMN settlement_transaction_id UUID UNIQUE REFERENCES journal_transactions (id),
    ADD CONSTRAINT payouts_status_check CHECK (
        status IN (
            'pending_review', 'approved', 'ready_for_broadcast', 'confirming',
            'succeeded', 'failed', 'rejected'
        )
    ),
    ADD CONSTRAINT payouts_broadcast_result_check CHECK (
        broadcast_result IS NULL OR broadcast_result IN ('accepted', 'unknown')
    ),
    ADD CONSTRAINT payouts_execution_lease_check CHECK (
        (execution_lease_owner IS NULL AND execution_lease_until IS NULL)
        OR
        (
            status IN ('approved', 'ready_for_broadcast', 'confirming')
            AND char_length(execution_lease_owner) BETWEEN 1 AND 128
            AND execution_lease_until IS NOT NULL
        )
    ),
    ADD CONSTRAINT payouts_lifecycle_check CHECK (
        (
            status = 'pending_review'
            AND reviewed_by IS NULL
            AND review_reason IS NULL
            AND reviewed_at IS NULL
            AND transaction_id IS NULL
            AND signed_transaction IS NULL
            AND transaction_expires_at IS NULL
            AND broadcasted_at IS NULL
            AND broadcast_result IS NULL
            AND confirmed_at IS NULL
            AND failed_at IS NULL
            AND failure_reason IS NULL
            AND settlement_transaction_id IS NULL
            AND unfreeze_transaction_id IS NULL
        )
        OR
        (
            status = 'approved'
            AND char_length(reviewed_by) BETWEEN 1 AND 128
            AND char_length(review_reason) BETWEEN 1 AND 512
            AND reviewed_at IS NOT NULL
            AND transaction_id IS NULL
            AND signed_transaction IS NULL
            AND transaction_expires_at IS NULL
            AND broadcasted_at IS NULL
            AND broadcast_result IS NULL
            AND confirmed_at IS NULL
            AND failed_at IS NULL
            AND failure_reason IS NULL
            AND settlement_transaction_id IS NULL
            AND unfreeze_transaction_id IS NULL
        )
        OR
        (
            status = 'ready_for_broadcast'
            AND char_length(reviewed_by) BETWEEN 1 AND 128
            AND char_length(review_reason) BETWEEN 1 AND 512
            AND reviewed_at IS NOT NULL
            AND transaction_id IS NOT NULL
            AND signed_transaction IS NOT NULL
            AND transaction_expires_at IS NOT NULL
            AND broadcasted_at IS NULL
            AND broadcast_result IS NULL
            AND next_confirmation_at IS NULL
            AND confirmed_at IS NULL
            AND failed_at IS NULL
            AND failure_reason IS NULL
            AND settlement_transaction_id IS NULL
            AND unfreeze_transaction_id IS NULL
        )
        OR
        (
            status = 'confirming'
            AND char_length(reviewed_by) BETWEEN 1 AND 128
            AND char_length(review_reason) BETWEEN 1 AND 512
            AND reviewed_at IS NOT NULL
            AND transaction_id IS NOT NULL
            AND signed_transaction IS NOT NULL
            AND transaction_expires_at IS NOT NULL
            AND broadcasted_at IS NOT NULL
            AND broadcast_result IS NOT NULL
            AND next_confirmation_at IS NOT NULL
            AND confirmed_at IS NULL
            AND failed_at IS NULL
            AND failure_reason IS NULL
            AND settlement_transaction_id IS NULL
            AND unfreeze_transaction_id IS NULL
        )
        OR
        (
            status = 'succeeded'
            AND char_length(reviewed_by) BETWEEN 1 AND 128
            AND char_length(review_reason) BETWEEN 1 AND 512
            AND reviewed_at IS NOT NULL
            AND transaction_id IS NOT NULL
            AND broadcasted_at IS NOT NULL
            AND confirmed_at IS NOT NULL
            AND failed_at IS NULL
            AND failure_reason IS NULL
            AND settlement_transaction_id IS NOT NULL
            AND unfreeze_transaction_id IS NULL
            AND execution_lease_owner IS NULL
            AND execution_lease_until IS NULL
        )
        OR
        (
            status = 'failed'
            AND char_length(reviewed_by) BETWEEN 1 AND 128
            AND char_length(review_reason) BETWEEN 1 AND 512
            AND reviewed_at IS NOT NULL
            AND transaction_id IS NOT NULL
            AND broadcasted_at IS NOT NULL
            AND failed_at IS NOT NULL
            AND confirmed_at IS NULL
            AND char_length(failure_reason) BETWEEN 1 AND 128
            AND settlement_transaction_id IS NULL
            AND unfreeze_transaction_id IS NOT NULL
            AND execution_lease_owner IS NULL
            AND execution_lease_until IS NULL
        )
        OR
        (
            status = 'rejected'
            AND char_length(reviewed_by) BETWEEN 1 AND 128
            AND char_length(review_reason) BETWEEN 1 AND 512
            AND reviewed_at IS NOT NULL
            AND transaction_id IS NULL
            AND signed_transaction IS NULL
            AND transaction_expires_at IS NULL
            AND broadcasted_at IS NULL
            AND broadcast_result IS NULL
            AND confirmed_at IS NULL
            AND failed_at IS NULL
            AND failure_reason IS NULL
            AND settlement_transaction_id IS NULL
            AND unfreeze_transaction_id IS NOT NULL
        )
    );

CREATE INDEX payouts_broadcast_claim_idx
    ON payouts (updated_at, id)
    WHERE status = 'ready_for_broadcast';

CREATE INDEX payouts_confirmation_claim_idx
    ON payouts (next_confirmation_at, id)
    WHERE status = 'confirming';

CREATE FUNCTION protect_payout_lifecycle()
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

    IF OLD.reviewed_at IS NOT NULL
        AND (
            OLD.reviewed_by IS DISTINCT FROM NEW.reviewed_by
            OR OLD.review_reason IS DISTINCT FROM NEW.review_reason
            OR OLD.reviewed_at IS DISTINCT FROM NEW.reviewed_at
        )
    THEN
        RAISE EXCEPTION 'payout review is immutable';
    END IF;

    IF OLD.transaction_id IS NOT NULL
        AND (
            OLD.transaction_id IS DISTINCT FROM NEW.transaction_id
            OR OLD.signed_transaction IS DISTINCT FROM NEW.signed_transaction
            OR OLD.transaction_expires_at IS DISTINCT FROM NEW.transaction_expires_at
        )
    THEN
        RAISE EXCEPTION 'signed payout transaction is immutable';
    END IF;

    IF OLD.status = 'pending_review' AND NEW.status IN ('approved', 'rejected') THEN
        RETURN NEW;
    END IF;

    IF OLD.status = 'approved' AND NEW.status = 'ready_for_broadcast' THEN
        RETURN NEW;
    END IF;

    IF OLD.status = 'ready_for_broadcast' AND NEW.status = 'confirming' THEN
        RETURN NEW;
    END IF;

    IF OLD.status = 'confirming' AND NEW.status IN ('succeeded', 'failed') THEN
        RETURN NEW;
    END IF;

    IF OLD.status = NEW.status AND OLD.status IN ('approved', 'ready_for_broadcast', 'confirming')
        AND NEW.reviewed_by IS NOT DISTINCT FROM OLD.reviewed_by
        AND NEW.review_reason IS NOT DISTINCT FROM OLD.review_reason
        AND NEW.reviewed_at IS NOT DISTINCT FROM OLD.reviewed_at
        AND NEW.transaction_id IS NOT DISTINCT FROM OLD.transaction_id
        AND NEW.signed_transaction IS NOT DISTINCT FROM OLD.signed_transaction
        AND NEW.transaction_expires_at IS NOT DISTINCT FROM OLD.transaction_expires_at
        AND NEW.broadcasted_at IS NOT DISTINCT FROM OLD.broadcasted_at
        AND NEW.broadcast_result IS NOT DISTINCT FROM OLD.broadcast_result
        AND NEW.confirmed_at IS NOT DISTINCT FROM OLD.confirmed_at
        AND NEW.failed_at IS NOT DISTINCT FROM OLD.failed_at
        AND NEW.failure_reason IS NOT DISTINCT FROM OLD.failure_reason
        AND NEW.settlement_transaction_id IS NOT DISTINCT FROM OLD.settlement_transaction_id
        AND NEW.unfreeze_transaction_id IS NOT DISTINCT FROM OLD.unfreeze_transaction_id
        AND NEW.execution_lease_epoch IN (OLD.execution_lease_epoch, OLD.execution_lease_epoch + 1)
        AND NEW.signing_attempts >= OLD.signing_attempts
        AND NEW.broadcast_attempts >= OLD.broadcast_attempts
        AND NEW.confirmation_attempts >= OLD.confirmation_attempts
    THEN
        RETURN NEW;
    END IF;

    RAISE EXCEPTION 'invalid payout state transition: % -> %', OLD.status, NEW.status;
END;
$$;

CREATE TRIGGER payouts_lifecycle_guard
BEFORE UPDATE OR DELETE ON payouts
FOR EACH ROW
EXECUTE FUNCTION protect_payout_lifecycle();
