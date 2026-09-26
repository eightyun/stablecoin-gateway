DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM payouts WHERE status <> 'pending_review') THEN
        RAISE EXCEPTION 'cannot roll back payout review schema while reviewed payouts exist';
    END IF;
END;
$$;

DROP TRIGGER payouts_review_guard ON payouts;
DROP FUNCTION protect_payout_review();

ALTER TABLE payouts
    DROP CONSTRAINT payouts_review_state_check,
    DROP CONSTRAINT payouts_status_check,
    DROP COLUMN unfreeze_transaction_id,
    DROP COLUMN reviewed_at,
    DROP COLUMN review_reason,
    DROP COLUMN reviewed_by,
    ADD CONSTRAINT payouts_status_check CHECK (status IN ('pending_review'));
