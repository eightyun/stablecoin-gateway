CREATE TABLE signer_requests (
    request_id UUID PRIMARY KEY,
    request_hash CHAR(64) NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    transaction_id CHAR(64) NOT NULL UNIQUE CHECK (transaction_id ~ '^[0-9a-f]{64}$'),
    signed_transaction BYTEA NOT NULL CHECK (
        octet_length(signed_transaction) BETWEEN 1 AND 2097152
    ),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE FUNCTION protect_signer_request()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'signer requests are immutable';
END;
$$;

CREATE TRIGGER signer_requests_immutable_guard
BEFORE UPDATE OR DELETE ON signer_requests
FOR EACH ROW
EXECUTE FUNCTION protect_signer_request();
