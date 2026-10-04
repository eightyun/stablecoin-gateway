DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM deposit_event_matches
        WHERE reason IN ('screening_denied', 'screening_review')
    ) THEN
        RAISE EXCEPTION 'screening review matches must be reconciled before reverting migration 21';
    END IF;
END;
$$;

DROP TRIGGER deposit_screening_jobs_guard ON deposit_screening_jobs;
DROP FUNCTION protect_deposit_screening_job();
DROP TRIGGER deposit_screening_results_immutable_guard ON deposit_screening_results;
DROP FUNCTION protect_deposit_screening_result();
ALTER TABLE deposit_screening_jobs
    DROP CONSTRAINT deposit_screening_jobs_current_result_fk;
DROP TABLE deposit_screening_results;
DROP TABLE deposit_screening_jobs;

ALTER TABLE deposit_event_matches
    DROP CONSTRAINT deposit_event_matches_reason_check,
    ADD CONSTRAINT deposit_event_matches_reason_check CHECK (reason IN (
        'no_intent', 'before_intent', 'after_expiry', 'missing_block_time',
        'asset_disabled', 'merchant_inactive', 'address_retired', 'overpaid',
        'ledger_account_unavailable', 'amount_out_of_range'
    ));
