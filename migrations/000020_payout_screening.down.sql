DROP TRIGGER payout_screening_jobs_guard ON payout_screening_jobs;
DROP FUNCTION protect_payout_screening_job();
DROP TRIGGER payout_screening_results_immutable_guard ON payout_screening_results;
DROP FUNCTION protect_payout_screening_result();
ALTER TABLE payout_screening_jobs
    DROP CONSTRAINT payout_screening_jobs_current_result_fk;
DROP TABLE payout_screening_results;
DROP TABLE payout_screening_jobs;
