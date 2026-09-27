CREATE INDEX CONCURRENTLY outbox_events_monitoring_status_created_idx
    ON outbox_events (status, created_at)
    WHERE status IN ('pending', 'processing', 'dead');
