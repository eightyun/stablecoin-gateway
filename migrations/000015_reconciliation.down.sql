DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM reconciliation_runs)
        OR EXISTS (SELECT 1 FROM reconciliation_cases)
        OR EXISTS (SELECT 1 FROM reconciliation_observations)
        OR EXISTS (SELECT 1 FROM reconciliation_case_actions)
    THEN
        RAISE EXCEPTION 'cannot roll back reconciliation schema while reconciliation data exists';
    END IF;
END;
$$;

DROP TRIGGER reconciliation_cases_guard ON reconciliation_cases;
DROP FUNCTION protect_reconciliation_case();
DROP TRIGGER reconciliation_case_actions_immutable_guard ON reconciliation_case_actions;
DROP TRIGGER reconciliation_observations_immutable_guard ON reconciliation_observations;
DROP TRIGGER reconciliation_runs_immutable_guard ON reconciliation_runs;
DROP FUNCTION protect_reconciliation_immutable();
DROP TABLE reconciliation_case_actions;
DROP TABLE reconciliation_observations;
DROP TABLE reconciliation_cases;
DROP TABLE reconciliation_runs;
