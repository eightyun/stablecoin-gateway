ALTER TABLE deposit_event_matches
    DROP CONSTRAINT deposit_event_matches_reason_check,
    ADD CONSTRAINT deposit_event_matches_reason_check CHECK (reason IN (
        'no_intent', 'before_intent', 'after_expiry', 'missing_block_time',
        'asset_disabled', 'merchant_inactive', 'address_retired'
    ));

ALTER TABLE deposit_intents
    DROP CONSTRAINT deposit_intents_credit_check,
    DROP CONSTRAINT deposit_intents_ledger_transaction_unique,
    DROP COLUMN credited_at,
    DROP COLUMN credited_amount,
    DROP COLUMN ledger_transaction_id;
