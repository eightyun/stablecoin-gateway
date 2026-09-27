CREATE TABLE reconciliation_runs (
    id UUID PRIMARY KEY,
    kind TEXT NOT NULL CHECK (kind IN ('ledger_integrity')),
    snapshot_at TIMESTAMPTZ NOT NULL,
    checked_items BIGINT NOT NULL CHECK (checked_items >= 0),
    finding_count INTEGER NOT NULL CHECK (finding_count >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE reconciliation_cases (
    id UUID PRIMARY KEY,
    fingerprint CHAR(64) NOT NULL,
    rule_code TEXT NOT NULL CHECK (char_length(rule_code) BETWEEN 1 AND 128),
    severity TEXT NOT NULL CHECK (severity IN ('warning', 'critical')),
    resource_type TEXT NOT NULL CHECK (char_length(resource_type) BETWEEN 1 AND 64),
    resource_id TEXT NOT NULL CHECK (char_length(resource_id) BETWEEN 1 AND 256),
    asset_id TEXT REFERENCES assets (id),
    status TEXT NOT NULL CHECK (status IN ('open', 'resolved')),
    first_run_id UUID NOT NULL REFERENCES reconciliation_runs (id),
    last_run_id UUID NOT NULL REFERENCES reconciliation_runs (id),
    occurrence_count BIGINT NOT NULL CHECK (occurrence_count > 0),
    opened_at TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL,
    resolved_at TIMESTAMPTZ,
    resolved_by TEXT,
    resolution_reason TEXT,
    CONSTRAINT reconciliation_cases_lifecycle_check CHECK (
        (
            status = 'open'
            AND resolved_at IS NULL
            AND resolved_by IS NULL
            AND resolution_reason IS NULL
        )
        OR
        (
            status = 'resolved'
            AND resolved_at IS NOT NULL
            AND char_length(resolved_by) BETWEEN 1 AND 128
            AND char_length(resolution_reason) BETWEEN 1 AND 512
        )
    ),
    CONSTRAINT reconciliation_cases_time_check CHECK (last_seen_at >= opened_at)
);

CREATE UNIQUE INDEX reconciliation_cases_open_fingerprint_idx
    ON reconciliation_cases (fingerprint)
    WHERE status = 'open';

CREATE INDEX reconciliation_cases_status_seen_idx
    ON reconciliation_cases (status, last_seen_at DESC, id);

CREATE TABLE reconciliation_observations (
    run_id UUID NOT NULL REFERENCES reconciliation_runs (id),
    case_id UUID NOT NULL REFERENCES reconciliation_cases (id),
    evidence JSONB NOT NULL CHECK (jsonb_typeof(evidence) = 'object'),
    observed_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (run_id, case_id)
);

CREATE TABLE reconciliation_case_actions (
    id UUID PRIMARY KEY,
    case_id UUID NOT NULL REFERENCES reconciliation_cases (id),
    action TEXT NOT NULL CHECK (action IN ('resolved')),
    actor TEXT NOT NULL CHECK (char_length(actor) BETWEEN 1 AND 128),
    reason TEXT NOT NULL CHECK (char_length(reason) BETWEEN 1 AND 512),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX reconciliation_case_actions_case_idx
    ON reconciliation_case_actions (case_id, created_at, id);

CREATE FUNCTION protect_reconciliation_immutable()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION '% rows are immutable', TG_TABLE_NAME;
END;
$$;

CREATE TRIGGER reconciliation_runs_immutable_guard
BEFORE UPDATE OR DELETE ON reconciliation_runs
FOR EACH ROW
EXECUTE FUNCTION protect_reconciliation_immutable();

CREATE TRIGGER reconciliation_observations_immutable_guard
BEFORE UPDATE OR DELETE ON reconciliation_observations
FOR EACH ROW
EXECUTE FUNCTION protect_reconciliation_immutable();

CREATE TRIGGER reconciliation_case_actions_immutable_guard
BEFORE UPDATE OR DELETE ON reconciliation_case_actions
FOR EACH ROW
EXECUTE FUNCTION protect_reconciliation_immutable();

CREATE FUNCTION protect_reconciliation_case()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'reconciliation cases are immutable';
    END IF;

    IF OLD.id IS DISTINCT FROM NEW.id
        OR OLD.fingerprint IS DISTINCT FROM NEW.fingerprint
        OR OLD.rule_code IS DISTINCT FROM NEW.rule_code
        OR OLD.severity IS DISTINCT FROM NEW.severity
        OR OLD.resource_type IS DISTINCT FROM NEW.resource_type
        OR OLD.resource_id IS DISTINCT FROM NEW.resource_id
        OR OLD.asset_id IS DISTINCT FROM NEW.asset_id
        OR OLD.first_run_id IS DISTINCT FROM NEW.first_run_id
        OR OLD.opened_at IS DISTINCT FROM NEW.opened_at
    THEN
        RAISE EXCEPTION 'reconciliation case identity is immutable';
    END IF;

    IF OLD.status = 'open'
        AND NEW.status = 'open'
        AND NEW.last_run_id IS DISTINCT FROM OLD.last_run_id
        AND NEW.occurrence_count = OLD.occurrence_count + 1
        AND NEW.last_seen_at >= OLD.last_seen_at
        AND NEW.resolved_at IS NULL
        AND NEW.resolved_by IS NULL
        AND NEW.resolution_reason IS NULL
    THEN
        RETURN NEW;
    END IF;

    IF OLD.status = 'open'
        AND NEW.status = 'resolved'
        AND NEW.last_run_id = OLD.last_run_id
        AND NEW.occurrence_count = OLD.occurrence_count
        AND NEW.last_seen_at = OLD.last_seen_at
        AND NEW.resolved_at IS NOT NULL
        AND char_length(NEW.resolved_by) BETWEEN 1 AND 128
        AND char_length(NEW.resolution_reason) BETWEEN 1 AND 512
    THEN
        RETURN NEW;
    END IF;

    RAISE EXCEPTION 'invalid reconciliation case transition: % -> %', OLD.status, NEW.status;
END;
$$;

CREATE TRIGGER reconciliation_cases_guard
BEFORE UPDATE OR DELETE ON reconciliation_cases
FOR EACH ROW
EXECUTE FUNCTION protect_reconciliation_case();
