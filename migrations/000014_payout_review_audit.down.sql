DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM payout_review_audits) THEN
        RAISE EXCEPTION 'cannot roll back payout review audit schema while audits exist';
    END IF;
END;
$$;

DROP TRIGGER payout_review_audits_immutable_guard ON payout_review_audits;
DROP FUNCTION protect_payout_review_audit();
DROP TABLE payout_review_audits;
