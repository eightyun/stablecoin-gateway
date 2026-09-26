CREATE TABLE merchant_webhook_endpoints (
    id UUID PRIMARY KEY,
    merchant_id UUID NOT NULL REFERENCES merchants (id),
    name TEXT NOT NULL CHECK (char_length(name) BETWEEN 1 AND 128),
    url TEXT NOT NULL CHECK (char_length(url) BETWEEN 1 AND 2048),
    secret_ciphertext BYTEA NOT NULL CHECK (octet_length(secret_ciphertext) >= 32),
    secret_nonce BYTEA NOT NULL CHECK (octet_length(secret_nonce) = 12),
    encryption_key_version TEXT NOT NULL CHECK (encryption_key_version <> ''),
    status TEXT NOT NULL CHECK (status IN ('active', 'disabled')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    disabled_at TIMESTAMPTZ,
    CONSTRAINT merchant_webhook_endpoints_lifecycle_check CHECK (
        (status = 'active' AND disabled_at IS NULL)
        OR (status = 'disabled' AND disabled_at IS NOT NULL)
    )
);

CREATE UNIQUE INDEX merchant_webhook_endpoints_active_url_idx
    ON merchant_webhook_endpoints (merchant_id, url)
    WHERE status = 'active';

CREATE INDEX merchant_webhook_endpoints_merchant_idx
    ON merchant_webhook_endpoints (merchant_id, created_at);

CREATE TABLE webhook_delivery_attempts (
    outbox_event_id UUID NOT NULL REFERENCES outbox_events (id),
    endpoint_id UUID NOT NULL REFERENCES merchant_webhook_endpoints (id),
    attempt INTEGER NOT NULL CHECK (attempt > 0),
    request_timestamp BIGINT NOT NULL CHECK (request_timestamp > 0),
    response_status INTEGER CHECK (response_status BETWEEN 100 AND 599),
    error TEXT CHECK (error IS NULL OR char_length(error) BETWEEN 1 AND 2048),
    duration_ms BIGINT NOT NULL CHECK (duration_ms >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (outbox_event_id, endpoint_id, attempt),
    CONSTRAINT webhook_delivery_attempts_result_check CHECK (
        (response_status BETWEEN 200 AND 299 AND error IS NULL)
        OR (response_status IS NULL AND error IS NOT NULL)
        OR (response_status IS NOT NULL AND response_status NOT BETWEEN 200 AND 299 AND error IS NOT NULL)
    )
);

CREATE INDEX webhook_delivery_attempts_endpoint_idx
    ON webhook_delivery_attempts (endpoint_id, created_at DESC);

CREATE FUNCTION protect_webhook_delivery_attempt()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'webhook delivery attempts are immutable';
END;
$$;

CREATE TRIGGER webhook_delivery_attempts_immutable_guard
BEFORE UPDATE OR DELETE ON webhook_delivery_attempts
FOR EACH ROW
EXECUTE FUNCTION protect_webhook_delivery_attempt();
