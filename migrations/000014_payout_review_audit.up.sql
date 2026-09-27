CREATE TABLE payout_review_audits (
    payout_id UUID PRIMARY KEY REFERENCES payouts (id),
    decision TEXT NOT NULL CHECK (decision IN ('approve', 'reject')),
    resulting_status TEXT NOT NULL CHECK (resulting_status IN ('approved', 'rejected')),
    reviewer TEXT NOT NULL CHECK (char_length(reviewer) BETWEEN 1 AND 128),
    reason TEXT NOT NULL CHECK (char_length(reason) BETWEEN 1 AND 512),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT payout_review_audits_decision_status_check CHECK (
        (decision = 'approve' AND resulting_status = 'approved')
        OR (decision = 'reject' AND resulting_status = 'rejected')
    )
);

INSERT INTO payout_review_audits (
    payout_id, decision, resulting_status, reviewer, reason, created_at
)
SELECT
    id,
    CASE WHEN status = 'rejected' THEN 'reject' ELSE 'approve' END,
    CASE WHEN status = 'rejected' THEN 'rejected' ELSE 'approved' END,
    reviewed_by,
    review_reason,
    reviewed_at
FROM payouts
WHERE reviewed_at IS NOT NULL;

CREATE INDEX payout_review_audits_created_idx
    ON payout_review_audits (created_at, payout_id);

CREATE FUNCTION protect_payout_review_audit()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'payout review audits are immutable';
END;
$$;

CREATE TRIGGER payout_review_audits_immutable_guard
BEFORE UPDATE OR DELETE ON payout_review_audits
FOR EACH ROW
EXECUTE FUNCTION protect_payout_review_audit();
