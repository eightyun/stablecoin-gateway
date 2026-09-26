CREATE TABLE merchant_api_keys (
    id UUID PRIMARY KEY,
    merchant_id UUID NOT NULL REFERENCES merchants (id),
    key_id TEXT NOT NULL UNIQUE CHECK (char_length(key_id) BETWEEN 4 AND 128),
    name TEXT NOT NULL CHECK (char_length(name) BETWEEN 1 AND 128),
    secret_ciphertext BYTEA NOT NULL CHECK (octet_length(secret_ciphertext) >= 32),
    secret_nonce BYTEA NOT NULL CHECK (octet_length(secret_nonce) = 12),
    encryption_key_version TEXT NOT NULL CHECK (encryption_key_version <> ''),
    status TEXT NOT NULL CHECK (status IN ('active', 'revoked')),
    expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    revoked_at TIMESTAMPTZ,
    CONSTRAINT merchant_api_keys_lifecycle_check CHECK (
        (status = 'active' AND revoked_at IS NULL)
        OR (status = 'revoked' AND revoked_at IS NOT NULL)
    )
);

CREATE INDEX merchant_api_keys_merchant_idx
    ON merchant_api_keys (merchant_id, created_at);

CREATE TABLE merchant_api_nonces (
    api_key_id UUID NOT NULL REFERENCES merchant_api_keys (id),
    nonce TEXT NOT NULL CHECK (char_length(nonce) BETWEEN 16 AND 128),
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (api_key_id, nonce)
);

CREATE INDEX merchant_api_nonces_expiry_idx
    ON merchant_api_nonces (expires_at);
