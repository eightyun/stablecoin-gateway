ALTER TABLE payouts
    DROP CONSTRAINT payouts_status_check,
    ADD COLUMN reviewed_by TEXT,
    ADD COLUMN review_reason TEXT,
    ADD COLUMN reviewed_at TIMESTAMPTZ,
    ADD COLUMN unfreeze_transaction_id UUID UNIQUE REFERENCES journal_transactions (id),
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
