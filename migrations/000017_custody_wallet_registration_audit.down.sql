DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM custody_wallet_registration_audits) THEN
        RAISE EXCEPTION 'cannot roll back custody wallet registration audit while audits exist';
    END IF;
END;
$$;

DROP TRIGGER custody_wallet_registration_audit_required ON custody_wallets;
DROP FUNCTION require_custody_wallet_registration_audit();
DROP TRIGGER custody_wallet_registration_audits_immutable_guard ON custody_wallet_registration_audits;
DROP FUNCTION protect_custody_wallet_registration_audit();
DROP TABLE custody_wallet_registration_audits;
