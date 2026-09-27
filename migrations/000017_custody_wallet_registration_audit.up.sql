CREATE TABLE custody_wallet_registration_audits (
    id UUID PRIMARY KEY,
    wallet_id UUID NOT NULL UNIQUE REFERENCES custody_wallets (id),
    actor TEXT NOT NULL CHECK (char_length(actor) BETWEEN 1 AND 128),
    reason TEXT NOT NULL CHECK (char_length(reason) BETWEEN 1 AND 512),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO custody_wallet_registration_audits (id, wallet_id, actor, reason, created_at)
SELECT id, id, 'system:migration', 'backfilled existing non-deposit custody wallet', created_at
FROM custody_wallets
WHERE role <> 'deposit';

CREATE FUNCTION protect_custody_wallet_registration_audit()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'custody wallet registration audits are immutable';
END;
$$;

CREATE TRIGGER custody_wallet_registration_audits_immutable_guard
BEFORE UPDATE OR DELETE ON custody_wallet_registration_audits
FOR EACH ROW
EXECUTE FUNCTION protect_custody_wallet_registration_audit();

CREATE FUNCTION require_custody_wallet_registration_audit()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.role <> 'deposit'
        AND NOT EXISTS (
            SELECT 1
            FROM custody_wallet_registration_audits
            WHERE wallet_id = NEW.id
        )
    THEN
        RAISE EXCEPTION 'non-deposit custody wallet requires registration audit';
    END IF;
    RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER custody_wallet_registration_audit_required
AFTER INSERT ON custody_wallets
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
EXECUTE FUNCTION require_custody_wallet_registration_audit();
