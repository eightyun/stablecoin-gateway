ALTER TABLE outbox_events
    ADD COLUMN lease_owner TEXT;

UPDATE outbox_events
SET status = 'pending',
    lease_until = NULL,
    available_at = LEAST(available_at, CURRENT_TIMESTAMP),
    updated_at = CURRENT_TIMESTAMP
WHERE status = 'processing';

ALTER TABLE outbox_events
    ADD CONSTRAINT outbox_events_lease_check CHECK (
        (status = 'processing' AND lease_owner IS NOT NULL AND lease_until IS NOT NULL)
        OR
        (status <> 'processing' AND lease_owner IS NULL AND lease_until IS NULL)
    );
