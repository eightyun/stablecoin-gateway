ALTER TABLE journal_transactions
    ADD COLUMN status TEXT,
    ADD COLUMN posted_at TIMESTAMPTZ,
    ADD COLUMN reversed_at TIMESTAMPTZ;

UPDATE journal_transactions
SET status = 'posted',
    posted_at = created_at;

ALTER TABLE journal_transactions
    ALTER COLUMN status SET DEFAULT 'draft',
    ALTER COLUMN status SET NOT NULL,
    ADD CONSTRAINT journal_transactions_status_check CHECK (
        (status = 'draft' AND posted_at IS NULL AND reversed_at IS NULL)
        OR
        (status = 'posted' AND posted_at IS NOT NULL AND reversed_at IS NULL)
        OR
        (status = 'reversed' AND posted_at IS NOT NULL AND reversed_at IS NOT NULL)
    );

DO $$
DECLARE
    invalid_transaction_id UUID;
BEGIN
    SELECT transaction_id
    INTO invalid_transaction_id
    FROM (
        SELECT journal.id AS transaction_id
        FROM journal_transactions AS journal
        LEFT JOIN journal_entries AS entry
            ON entry.transaction_id = journal.id
        GROUP BY journal.id
        HAVING COUNT(entry.transaction_id) < 2

        UNION

        SELECT entry.transaction_id
        FROM journal_entries AS entry
        GROUP BY entry.transaction_id, entry.asset_id
        HAVING SUM(entry.amount) FILTER (WHERE entry.side = 'D')
            IS DISTINCT FROM SUM(entry.amount) FILTER (WHERE entry.side = 'C')
    ) AS invalid_transactions
    LIMIT 1;

    IF invalid_transaction_id IS NOT NULL THEN
        RAISE EXCEPTION 'existing journal transaction % is not balanced', invalid_transaction_id;
    END IF;
END;
$$;

CREATE FUNCTION assert_journal_transaction_balanced()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    current_status TEXT;
    entry_count BIGINT;
    unbalanced_asset TEXT;
BEGIN
    SELECT status
    INTO current_status
    FROM journal_transactions
    WHERE id = NEW.id;

    IF current_status = 'draft' THEN
        RAISE EXCEPTION 'journal transaction % cannot remain draft at commit', NEW.id;
    END IF;

    SELECT COUNT(*)
    INTO entry_count
    FROM journal_entries
    WHERE transaction_id = NEW.id;

    IF entry_count < 2 THEN
        RAISE EXCEPTION 'journal transaction % requires at least two entries', NEW.id;
    END IF;

    SELECT asset_id
    INTO unbalanced_asset
    FROM journal_entries
    WHERE transaction_id = NEW.id
    GROUP BY asset_id
    HAVING SUM(amount) FILTER (WHERE side = 'D')
        IS DISTINCT FROM SUM(amount) FILTER (WHERE side = 'C')
    LIMIT 1;

    IF unbalanced_asset IS NOT NULL THEN
        RAISE EXCEPTION 'journal transaction % is unbalanced for asset %', NEW.id, unbalanced_asset;
    END IF;

    RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER journal_transactions_balance_guard
AFTER INSERT OR UPDATE ON journal_transactions
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
EXECUTE FUNCTION assert_journal_transaction_balanced();

CREATE FUNCTION protect_journal_entry()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    parent_status TEXT;
BEGIN
    IF TG_OP <> 'INSERT' THEN
        RAISE EXCEPTION 'posted journal entries are immutable';
    END IF;

    SELECT status
    INTO parent_status
    FROM journal_transactions
    WHERE id = NEW.transaction_id
    FOR UPDATE;

    IF parent_status IS DISTINCT FROM 'draft' THEN
        RAISE EXCEPTION 'journal entries can only be inserted into draft transactions';
    END IF;

    RETURN NEW;
END;
$$;

CREATE TRIGGER journal_entries_immutable_guard
BEFORE INSERT OR UPDATE OR DELETE ON journal_entries
FOR EACH ROW
EXECUTE FUNCTION protect_journal_entry();

CREATE FUNCTION protect_journal_transaction()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'journal transactions are immutable';
    END IF;

    IF OLD.id IS DISTINCT FROM NEW.id
        OR OLD.requester_type IS DISTINCT FROM NEW.requester_type
        OR OLD.requester_id IS DISTINCT FROM NEW.requester_id
        OR OLD.idempotency_key IS DISTINCT FROM NEW.idempotency_key
        OR OLD.reference_type IS DISTINCT FROM NEW.reference_type
        OR OLD.reference_id IS DISTINCT FROM NEW.reference_id
        OR OLD.request_hash IS DISTINCT FROM NEW.request_hash
        OR OLD.created_at IS DISTINCT FROM NEW.created_at
    THEN
        RAISE EXCEPTION 'journal transaction identity is immutable';
    END IF;

    IF OLD.status = 'draft'
        AND NEW.status = 'posted'
        AND NEW.posted_at IS NOT NULL
        AND NEW.reversed_at IS NULL
    THEN
        RETURN NEW;
    END IF;

    IF OLD.status = 'posted'
        AND NEW.status = 'reversed'
        AND NEW.posted_at = OLD.posted_at
        AND NEW.reversed_at IS NOT NULL
    THEN
        RETURN NEW;
    END IF;

    RAISE EXCEPTION 'invalid journal transaction state transition: % -> %', OLD.status, NEW.status;
END;
$$;

CREATE TRIGGER journal_transactions_immutable_guard
BEFORE UPDATE OR DELETE ON journal_transactions
FOR EACH ROW
EXECUTE FUNCTION protect_journal_transaction();
