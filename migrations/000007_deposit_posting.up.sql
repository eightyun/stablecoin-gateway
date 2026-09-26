DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM deposit_intents
        WHERE status IN ('paid', 'overpaid')
    ) THEN
        RAISE EXCEPTION 'paid deposit intents must be reconciled before applying deposit posting migration';
    END IF;
END;
$$;

ALTER TABLE deposit_intents
    ADD COLUMN ledger_transaction_id UUID REFERENCES journal_transactions (id),
    ADD COLUMN credited_amount NUMERIC(78, 0) NOT NULL DEFAULT 0 CHECK (credited_amount >= 0),
    ADD COLUMN credited_at TIMESTAMPTZ,
    ADD CONSTRAINT deposit_intents_ledger_transaction_unique UNIQUE (ledger_transaction_id),
    ADD CONSTRAINT deposit_intents_credit_check CHECK (
        (ledger_transaction_id IS NULL AND credited_amount = 0 AND credited_at IS NULL)
        OR
        (ledger_transaction_id IS NOT NULL AND credited_amount = expected_amount AND credited_at IS NOT NULL)
    );

ALTER TABLE deposit_event_matches
    DROP CONSTRAINT deposit_event_matches_reason_check,
    ADD CONSTRAINT deposit_event_matches_reason_check CHECK (reason IN (
        'no_intent', 'before_intent', 'after_expiry', 'missing_block_time',
        'asset_disabled', 'merchant_inactive', 'address_retired', 'overpaid',
        'ledger_account_unavailable', 'amount_out_of_range'
    ));
