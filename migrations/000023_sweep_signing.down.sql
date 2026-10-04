DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM sweep_executions WHERE status = 'ready_for_broadcast') THEN
        RAISE EXCEPTION 'complete or clear ready_for_broadcast sweep executions before migration 23 rollback';
    END IF;
END;
$$;

DROP TRIGGER IF EXISTS sweep_executions_guard ON sweep_executions;
DROP FUNCTION IF EXISTS protect_sweep_execution();
DROP TRIGGER IF EXISTS sweep_plans_create_execution ON sweep_plans;
DROP FUNCTION IF EXISTS create_sweep_execution();
DROP TABLE IF EXISTS sweep_executions;
