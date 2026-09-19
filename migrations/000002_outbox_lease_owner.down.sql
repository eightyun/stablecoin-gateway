ALTER TABLE outbox_events
    DROP CONSTRAINT IF EXISTS outbox_events_lease_check;

ALTER TABLE outbox_events
    DROP COLUMN IF EXISTS lease_owner;
