DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM sweep_screening_jobs)
        OR EXISTS (SELECT 1 FROM sweep_screening_results)
    THEN
        RAISE EXCEPTION 'cannot roll back sweep screening while screening evidence exists';
    END IF;
END;
$$;

DROP TRIGGER sweep_screening_jobs_guard ON sweep_screening_jobs;
DROP FUNCTION protect_sweep_screening_job();
DROP TRIGGER sweep_screening_results_immutable_guard ON sweep_screening_results;
DROP FUNCTION protect_sweep_screening_result();

ALTER TABLE sweep_screening_jobs
    DROP CONSTRAINT sweep_screening_jobs_current_result_fk;

DROP TABLE sweep_screening_results;
DROP TABLE sweep_screening_jobs;
